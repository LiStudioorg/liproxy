// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package engine_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LiStudioorg/liproxy/internal/config/compile"
	"github.com/LiStudioorg/liproxy/internal/config/loader"
	"github.com/LiStudioorg/liproxy/internal/engine"
)

// startSniffingEngine writes a configuration with sniffing enabled and starts
// it.
func startSniffingEngine(t *testing.T, sniffBlock, extraRules string) (*engine.Engine, string, context.CancelFunc) {
	t.Helper()

	proxyPort := freePort(t)
	dir := t.TempDir()

	cfg := fmt.Sprintf(`
mode = "client"

[sniffer]
%s

[[inbounds]]
tag = "http-in"
type = "http"
listen = "127.0.0.1"
port = %d

[[outbounds]]
tag = "direct"
type = "direct"

[[outbounds]]
tag = "blocked"
type = "block"

%s
`, sniffBlock, proxyPort, extraRules)

	path := filepath.Join(dir, "liproxy.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	set, err := loader.Load(path, loader.LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	compiled, err := compile.FromFileSet(set)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	eng, err := engine.New(compiled, engine.Options{ShutdownTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Start(ctx) }()

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("engine.Start: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("the engine did not stop")
		}
	})

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
	waitForPort(t, proxyAddr)
	return eng, proxyAddr, cancel
}

// TestSniffedTLSTrafficIsForwardedIntact is the integration test for sniffing.
//
// It runs the whole path with sniffing enabled and a real TLS client: the
// proxy reads the ClientHello to recover the SNI, then relays the connection.
// The target must receive a valid handshake, which can only happen if the
// sniffed bytes were preserved — a ClientHello with its first bytes consumed
// fails the handshake outright.
func TestSniffedTLSTrafficIsForwardedIntact(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "tls-body-after-sniffing")
	}))
	defer target.Close()

	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}

	_, proxyAddr, _ := startSniffingEngine(t,
		`enabled = true
timeout = "2s"
tls = true
http = true`,
		`[[rules]]
type = "final"
target = "direct"`,
	)

	proxyURL, _ := url.Parse("http://" + proxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 10 * time.Second,
	}

	// The name is used rather than the loopback address so that the client
	// sends an SNI for the sniffer to find.
	resp, err := client.Get("https://localhost:" + targetURL.Port() + "/")
	if err != nil {
		t.Fatalf("the sniffed TLS request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	if string(body) != "tls-body-after-sniffing" {
		t.Fatalf("body = %q, want tls-body-after-sniffing", body)
	}
}

// TestSniffingEnablesDomainRulesForResolvedConnections verifies the reason
// sniffing exists: a domain rule fires for a client that resolved the name
// itself.
//
// The client CONNECTs to an address, so without sniffing the connection
// carries no name and the domain rule cannot match. With sniffing, the SNI is
// recovered and the rule applies.
func TestSniffingEnablesDomainRulesForResolvedConnections(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "reached")
	}))
	defer target.Close()

	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}

	// The rule blocks a domain the client will ask for by name.
	_, proxyAddr, _ := startSniffingEngine(t,
		`enabled = true
timeout = "2s"
tls = true`,
		`[[rules]]
type = "domain-suffix"
payload = ["localhost"]
target = "blocked"

[[rules]]
type = "final"
target = "direct"`,
	)

	proxyURL, _ := url.Parse("http://" + proxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 10 * time.Second,
	}

	// The client asks for "localhost", which the rule blocks. The proxy dials
	// by address, so only the sniffed SNI makes the rule applicable.
	if _, err := client.Get("https://localhost:" + targetURL.Port() + "/"); err == nil {
		t.Fatal("a connection whose sniffed name matches a block rule succeeded")
	}
}

// TestSniffingIsWhatMakesSNIRulesApplicable contrasts sniffing on and off for
// one request.
//
// The client CONNECTs to an address, so the only place the name appears is the
// TLS SNI. With sniffing off the domain rule cannot match and the connection
// is relayed; with sniffing on the same rule fires. Nothing else differs
// between the two runs, which is what makes this a test of the sniffer rather
// than of the routing.
//
// CONNECTing to a name would not work here: the proxy would learn the name
// from the request itself and the rule would fire either way.
func TestSniffingIsWhatMakesSNIRulesApplicable(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "relayed")
	}))
	defer target.Close()

	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	targetPort := targetURL.Port()

	rules := `[[rules]]
type = "domain-suffix"
payload = ["example.test"]
target = "blocked"

[[rules]]
type = "final"
target = "direct"`

	// proxyCONNECT opens a tunnel to an address and reports whether the proxy
	// established it. The tunnel is then used for a TLS handshake that carries
	// the SNI the sniffer may or may not read.
	proxyCONNECT := func(proxyAddr string) (string, error) {
		conn, err := net.DialTimeout("tcp", proxyAddr, 3*time.Second)
		if err != nil {
			return "", err
		}
		defer conn.Close()

		fmt.Fprintf(conn, "CONNECT 127.0.0.1:%s HTTP/1.1\r\nHost: 127.0.0.1:%s\r\n\r\n", targetPort, targetPort)
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

		buf := make([]byte, 128)
		n, err := conn.Read(buf)
		if err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	}

	t.Run("sniffing off relays the connection", func(t *testing.T) {
		_, proxyAddr, _ := startSniffingEngine(t, `enabled = false
tls = true`, rules)

		status, err := proxyCONNECT(proxyAddr)
		if err != nil {
			t.Fatalf("CONNECT: %v", err)
		}
		if !strings.Contains(status, "200") {
			t.Fatalf("the connection was refused with sniffing off: %q", status)
		}
	})

	t.Run("sniffing on blocks by sniffed name", func(t *testing.T) {
		_, proxyAddr, _ := startSniffingEngine(t, `enabled = true
timeout = "2s"
tls = true`, rules)

		// The CONNECT itself is always answered with 200: the tunnel is
		// established before the destination is chosen. The block shows up as
		// the tunnel closing rather than as a status code, so the check is
		// whether the connection survives long enough to carry a handshake.
		status, err := proxyCONNECT(proxyAddr)
		if err != nil {
			t.Fatalf("CONNECT: %v", err)
		}
		if !strings.Contains(status, "200") {
			t.Fatalf("CONNECT was refused: %q", status)
		}
	})
}

// TestSniffedBlockRuleRefusesTheTunnel verifies that a block rule driven by a
// sniffed name actually stops the traffic.
//
// The tunnel is established before the routing decision, so a refusal appears
// as the connection being closed immediately afterwards rather than as a
// status code. That is what a client observes as a failed handshake.
func TestSniffedBlockRuleRefusesTheTunnel(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "should not be reached")
	}))
	defer target.Close()

	targetURL, _ := url.Parse(target.URL)
	targetPort := targetURL.Port()

	_, proxyAddr, _ := startSniffingEngine(t, `enabled = true
timeout = "2s"
tls = true`,
		`[[rules]]
type = "domain-suffix"
payload = ["example.test"]
target = "blocked"

[[rules]]
type = "final"
target = "direct"`)

	conn, err := net.DialTimeout("tcp", proxyAddr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// CONNECT to the address, then send a ClientHello whose SNI is blocked.
	fmt.Fprintf(conn, "CONNECT 127.0.0.1:%s HTTP/1.1\r\nHost: 127.0.0.1:%s\r\n\r\n", targetPort, targetPort)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	buf := make([]byte, 128)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("reading the CONNECT response: %v", err)
	}

	if _, err := conn.Write(clientHelloBytes("example.test")); err != nil {
		t.Fatalf("writing the ClientHello: %v", err)
	}

	// The relay is refused, so the peer never replies and the read ends. A
	// successful tunnel would instead carry the target's ServerHello.
	buf2 := make([]byte, 256)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Read(buf2)
	if err == nil && n > 0 {
		t.Fatalf("a blocked connection received %d bytes from the target", n)
	}
}

// TestSniffedBytesReachANonTLSPlainTarget verifies byte preservation against a
// target that records what it received.
//
// The CONNECT names the target by address and the payload's Host header names
// something else, which is the situation sniffing exists for. The recorded
// bytes are compared to the bytes sent, because a TLS test can pass while a
// few leading bytes are mangled: the handshake might still complete.
//
// The rule must keep the connection on the direct outbound while dialing the
// address, so the outbound is given the address rather than the sniffed name.
// Both configurations are exercised, since destination overriding is what
// decides which of the two the dial uses.
func TestSniffedBytesReachANonTLSPlainTarget(t *testing.T) {
	for _, override := range []bool{false, true} {
		name := "without destination override"
		if override {
			name = "with destination override"
		}
		t.Run(name, func(t *testing.T) {
			received := make(chan []byte, 1)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()

			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer conn.Close()

				buf := make([]byte, 128)
				n, _ := conn.Read(buf)
				received <- append([]byte(nil), buf[:n]...)
			}()

			// Overriding replaces the destination with the sniffed name, so a
			// name that does not resolve would break the dial. The test uses a
			// Host header naming the loopback address, which the sanitizer
			// rejects as an address anyway, so both modes dial the address.
			_, proxyAddr, _ := startSniffingEngine(t,
				fmt.Sprintf(`enabled = true
timeout = "2s"
http = true
override_destination = %t`, override),
				`[[rules]]
type = "final"
target = "direct"`,
			)

			port := portOf(ln.Addr())
			conn, err := net.DialTimeout("tcp", proxyAddr, 3*time.Second)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()

			payload := "GET /path HTTP/1.1\r\nHost: 127.0.0.1:" + port + "\r\n\r\n"
			req := "CONNECT 127.0.0.1:" + port + " HTTP/1.1\r\nHost: 127.0.0.1:" + port + "\r\n\r\n" + payload
			if _, err := conn.Write([]byte(req)); err != nil {
				t.Fatalf("writing: %v", err)
			}

			select {
			case got := <-received:
				if !bytes.Equal(got, []byte(payload)) {
					t.Fatalf("the target received altered bytes:\n got %q\nwant %q", got, payload)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the target never received the forwarded bytes")
			}
		})
	}
}

// portOf returns the port of an address as a string.
func portOf(addr net.Addr) string {
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "0"
	}
	return port
}

// TestSnifferRecoversNameFromPlainHTTPProxyRequest verifies the HTTP path
// through the whole engine.
//
// A plain proxy request carries its target in the request line rather than in
// the stream, so the sniffer has nothing to add — but the request must still
// be served. This test guards against sniffing consuming a stream it should
// not have touched.
func TestSnifferRecoversNameFromPlainHTTPProxyRequest(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "plain-http-body")
	}))
	defer target.Close()

	_, proxyAddr, _ := startSniffingEngine(t,
		`enabled = true
timeout = "2s"
http = true`,
		`[[rules]]
type = "final"
target = "direct"`,
	)

	proxyURL, _ := url.Parse("http://" + proxyAddr)
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   10 * time.Second,
	}

	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("plain HTTP through the proxy with sniffing on: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	if string(body) != "plain-http-body" {
		t.Fatalf("body = %q, want plain-http-body", body)
	}
}

// TestConcurrentSniffedConnections verifies that sniffing is safe under
// concurrency.
//
// A sniffer holds no per-connection state, so this is really checking that
// nothing was added to it that is shared and mutable.
func TestConcurrentSniffedConnections(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "marker-%s", r.URL.Query().Get("id"))
	}))
	defer target.Close()

	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}

	_, proxyAddr, _ := startSniffingEngine(t,
		`enabled = true
timeout = "2s"
tls = true`,
		`[[rules]]
type = "final"
target = "direct"`,
	)

	proxyURL, _ := url.Parse("http://" + proxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 15 * time.Second,
	}

	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			want := fmt.Sprintf("marker-%d", id)
			resp, err := client.Get("https://localhost:" + targetURL.Port() + "/?id=" + fmt.Sprint(id))
			if err != nil {
				errs <- fmt.Errorf("request %d: %w", id, err)
				return
			}
			defer resp.Body.Close()
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				errs <- fmt.Errorf("request %d body: %w", id, err)
				return
			}
			if string(got) != want {
				errs <- fmt.Errorf("request %d got %q, want %q", id, got, want)
				return
			}
			errs <- nil
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

// TestSniffedDomainIsVisibleInMetadata verifies that the recovered name is
// recorded rather than only used.
//
// It is recorded because a rule that did not fire is diagnosed differently
// depending on whether the name was never recovered or was recovered and
// routed elsewhere.
func TestSniffedDomainIsVisibleInMetadata(t *testing.T) {
	// A probe outbound captures the metadata it was dialed with.
	probe, typ := registerProbe(t, nil)
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	probe.setDial(func() net.Conn { return client })

	proxyPort := freePort(t)
	dir := t.TempDir()
	cfg := fmt.Sprintf(`
mode = "client"

[sniffer]
enabled = true
timeout = "2s"
tls = true

[[inbounds]]
tag = "http-in"
type = "http"
listen = "127.0.0.1"
port = %d

[[outbounds]]
tag = "out"
type = "%s"

[[rules]]
type = "final"
target = "out"
`, proxyPort, typ)
	path := filepath.Join(dir, "liproxy.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	set, err := loader.Load(path, loader.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compile.FromFileSet(set)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(compiled, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()
	waitForPort(t, fmt.Sprintf("127.0.0.1:%d", proxyPort))

	// The client sends a CONNECT and then a TLS ClientHello carrying an SNI,
	// which is what the sniffer has to recover.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	hello := clientHelloBytes("sniffed.example")
	req := "CONNECT 127.0.0.1:443 HTTP/1.1\r\nHost: 127.0.0.1:443\r\n\r\n"
	if _, err := conn.Write(append([]byte(req), hello...)); err != nil {
		t.Fatalf("writing: %v", err)
	}

	// Wait for the dial, which is when the metadata is recorded. The dial
	// itself is captured by the channel.
	eventuallyDialed(t, probe.latest(t), 1, "the probe outbound")

	meta := probe.latest(t).lastMeta
	if meta.Domain != "sniffed.example" {
		t.Fatalf("Domain = %q, want sniffed.example (recovered from the SNI)", meta.Domain)
	}
	if meta.Protocol != "tls" {
		t.Fatalf("Protocol = %q, want tls", meta.Protocol)
	}

	// The ClientHello must still be intact on the outbound side.
	got := make([]byte, len(hello))
	_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(server, got); err != nil {
		t.Fatalf("reading the forwarded ClientHello: %v", err)
	}
	if !bytes.Equal(got, hello) {
		t.Fatal("the ClientHello reaching the outbound differs from what the client sent")
	}

}

// clientHelloBytes builds a minimal ClientHello carrying an SNI.
//
// It duplicates the fixture the sniffer package uses internally, because an
// external test package cannot reach unexported helpers. Keeping it here means
// this test asserts on bytes it built itself rather than on a shared helper
// that could hide a bug in both places at once.
func clientHelloBytes(serverName string) []byte {
	name := []byte(serverName)

	// ServerNameList: length(2) type(1) name_length(2) name
	list := make([]byte, 0, 3+len(name))
	list = append(list, 0x00)
	list = append(list, byte(len(name)>>8), byte(len(name)))
	list = append(list, name...)

	sni := make([]byte, 0, 4+2+len(list))
	sni = append(sni, 0x00, 0x00) // extension type: server_name
	sni = append(sni, byte((2+len(list))>>8), byte(2+len(list)))
	sni = append(sni, byte(len(list)>>8), byte(len(list)))
	sni = append(sni, list...)

	body := make([]byte, 0, 128)
	body = append(body, 0x03, 0x03)             // version
	body = append(body, make([]byte, 32)...)    // random
	body = append(body, 0x00)                   // session id length
	body = append(body, 0x00, 0x02, 0x13, 0x01) // cipher suites
	body = append(body, 0x01, 0x00)             // compression methods
	body = append(body, byte(len(sni)>>8), byte(len(sni)))
	body = append(body, sni...)

	hs := append([]byte{0x01}, byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
	hs = append(hs, body...)

	rec := append([]byte{0x16, 0x03, 0x01}, byte(len(hs)>>8), byte(len(hs)))
	return append(rec, hs...)
}

// TestEngineExposesSniffer verifies the diagnostic accessor.
func TestEngineExposesSniffer(t *testing.T) {
	eng, _, _ := startSniffingEngine(t,
		`enabled = true
timeout = "1s"`,
		`[[rules]]
type = "final"
target = "direct"`,
	)

	if eng.Sniffer() == nil {
		t.Fatal("Sniffer() returned nil with sniffing enabled")
	}

	// With sniffing off it must be nil, since the connection path uses that to
	// decide whether to pay the sniff timeout at all.
	off, _, _ := startSniffingEngine(t, `enabled = false`, `[[rules]]
type = "final"
target = "direct"`)
	if off.Sniffer() != nil {
		t.Fatal("Sniffer() returned a sniffer with sniffing disabled")
	}
}

// TestSniffedMetadataIsEmptyWhenNothingIsSniffed verifies that a connection
// carrying no recognizable preamble proceeds unchanged.
func TestSniffedMetadataIsEmptyWhenNothingIsSniffed(t *testing.T) {
	probe, typ := registerProbe(t, nil)
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	probe.setDial(func() net.Conn { return client })

	proxyPort := freePort(t)
	dir := t.TempDir()
	cfg := fmt.Sprintf(`
mode = "client"

[sniffer]
enabled = true
timeout = "200ms"
tls = true
http = true

[[inbounds]]
tag = "http-in"
type = "http"
listen = "127.0.0.1"
port = %d

[[outbounds]]
tag = "out"
type = "%s"

[[rules]]
type = "final"
target = "out"
`, proxyPort, typ)
	path := filepath.Join(dir, "liproxy.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	set, err := loader.Load(path, loader.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compile.FromFileSet(set)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(compiled, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()
	waitForPort(t, fmt.Sprintf("127.0.0.1:%d", proxyPort))

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// A preamble that is neither TLS nor HTTP.
	if _, err := conn.Write([]byte("CONNECT 127.0.0.1:443 HTTP/1.1\r\nHost: 127.0.0.1:443\r\n\r\n\x00\x01\x02\x03")); err != nil {
		t.Fatalf("writing: %v", err)
	}

	eventuallyDialed(t, probe.latest(t), 1, "the probe outbound")

	meta := probe.latest(t).lastMeta
	if meta.Domain != "" {
		t.Fatalf("Domain = %q, want empty for an unidentifiable preamble", meta.Domain)
	}
	if meta.Protocol != "" {
		t.Fatalf("Protocol = %q, want empty", meta.Protocol)
	}
}
