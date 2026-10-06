// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package http

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/metadata"
	"github.com/LiStudioorg/liproxy/internal/registry"
)

// recorder is a Handler that records what it was asked to serve.
type recorder struct {
	mu     sync.Mutex
	metas  []metadata.Metadata
	serve  func(net.Conn, metadata.Metadata) error
	handle func(context.Context, net.Conn, metadata.Metadata) error
}

// Route makes the recorder usable wherever a Router is expected, so tests that
// only care about the handler do not need a second stub.
func (r *recorder) Route(metadata.Metadata) (string, error) { return "direct", nil }

func (r *recorder) Handle(ctx context.Context, client net.Conn, meta metadata.Metadata) error {
	r.mu.Lock()
	r.metas = append(r.metas, meta)
	r.mu.Unlock()

	if r.handle != nil {
		return r.handle(ctx, client, meta)
	}
	return nil
}

// last returns the most recent metadata the handler received.
func (r *recorder) last(t *testing.T) metadata.Metadata {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.metas) == 0 {
		t.Fatal("the handler was never called")
	}
	return r.metas[len(r.metas)-1]
}

// startInbound starts an HTTP inbound on a random port and returns its address.
func startInbound(t *testing.T, h Handler, port int) (net.Addr, *Inbound) {
	t.Helper()

	built, err := New(ir.Inbound{Tag: "http-in", Type: "http", Listen: "127.0.0.1", Port: port}, registry.Deps{Handler: h})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// New returns the interface; the test needs the concrete type to read the
	// bound address and to drive Close directly.
	in, ok := built.(*Inbound)
	if !ok {
		t.Fatalf("New returned %T, want *Inbound", built)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan net.Addr, 1)
	go func() {
		// Start blocks until the context is cancelled; wrapping it lets the
		// test learn the bound address once the listener exists.
		if err := in.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if addr := in.Addr(); addr != nil {
			ready <- addr
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Cleanup(func() {
		cancel()
		_ = in.Close()
	})

	select {
	case addr := <-ready:
		return addr, in
	case <-time.After(3 * time.Second):
		t.Fatal("the inbound never bound a listener")
		return nil, nil
	}
}

// dialProxy connects to the proxy.
func dialProxy(t *testing.T, addr net.Addr) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr.String(), 3*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// --- CONNECT --------------------------------------------------------------

// TestConnectTunnelCarriesData verifies the full CONNECT path: a 200 response,
// then raw bytes in both directions.
func TestConnectTunnelCarriesData(t *testing.T) {
	rec := &recorder{
		handle: func(_ context.Context, client net.Conn, _ metadata.Metadata) error {
			// Echo whatever the client sends, which proves the tunnel is
			// bidirectional rather than a one-way pipe.
			buf := make([]byte, 5)
			if _, err := io.ReadFull(client, buf); err != nil {
				return err
			}
			_, err := client.Write(buf)
			return err
		},
	}
	addr, _ := startInbound(t, rec, 0)

	conn := dialProxy(t, addr)
	br := bufio.NewReader(conn)

	if _, err := fmt.Fprintf(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"); err != nil {
		t.Fatalf("writing CONNECT: %v", err)
	}

	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading the status line: %v", err)
	}
	if !strings.Contains(status, "200") {
		t.Fatalf("status line = %q, want a 200 response", status)
	}
	// Consume the blank line ending the response headers.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading response headers: %v", err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("writing through the tunnel: %v", err)
	}
	echo := make([]byte, 5)
	if _, err := io.ReadFull(br, echo); err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if string(echo) != "hello" {
		t.Fatalf("echo = %q, want hello", echo)
	}

	meta := rec.last(t)
	if meta.Domain != "example.com" {
		t.Errorf("Domain = %q, want example.com", meta.Domain)
	}
	if meta.InboundTag != "http-in" {
		t.Errorf("InboundTag = %q, want http-in", meta.InboundTag)
	}
	if meta.Network != "tcp" {
		t.Errorf("Network = %q, want tcp", meta.Network)
	}
	if meta.DestAddr == nil {
		t.Fatal("DestAddr is nil")
	}
	if port := meta.DestAddr.(*net.TCPAddr).Port; port != 443 {
		t.Errorf("DestAddr port = %d, want 443", port)
	}
}

// TestConnectPreservesBufferedClientBytes verifies that bytes the client sends
// immediately after its headers are not dropped.
//
// A TLS ClientHello commonly arrives in the same TCP segment as the CONNECT
// request. Those bytes sit in the server's buffered reader, and discarding
// them truncates the client's first flight — which breaks every HTTPS request
// in a way that looks like a TLS problem rather than a proxy bug.
func TestConnectPreservesBufferedClientBytes(t *testing.T) {
	got := make(chan string, 1)
	rec := &recorder{
		handle: func(_ context.Context, client net.Conn, _ metadata.Metadata) error {
			buf := make([]byte, len("EARLYBYTES"))
			if _, err := io.ReadFull(client, buf); err != nil {
				got <- "error: " + err.Error()
				return err
			}
			got <- string(buf)
			return nil
		},
	}
	addr, _ := startInbound(t, rec, 0)

	conn := dialProxy(t, addr)
	// Client data is written in the same segment as the request, before the
	// proxy has replied, which is what puts it in the buffered reader.
	payload := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\nEARLYBYTES"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("writing request plus early bytes: %v", err)
	}

	select {
	case s := <-got:
		if s != "EARLYBYTES" {
			t.Fatalf("the handler received %q, want EARLYBYTES", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the handler never received the buffered bytes")
	}
}

// TestConnectRejectsBody verifies that a CONNECT carrying a body is refused.
func TestConnectRejectsBody(t *testing.T) {
	rec := &recorder{}
	addr, _ := startInbound(t, rec, 0)

	conn := dialProxy(t, addr)
	req := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nContent-Length: 5\r\n\r\nhello"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("writing: %v", err)
	}

	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading the status line: %v", err)
	}
	if !strings.Contains(status, "400") {
		t.Fatalf("status line = %q, want a 400", status)
	}
}

// TestConnectRejectsBadPort verifies that an unusable port is refused rather
// than dialed.
func TestConnectRejectsBadPort(t *testing.T) {
	rec := &recorder{}
	addr, _ := startInbound(t, rec, 0)

	cases := []string{
		"example.com:notaport",
		"example.com:99999",
		":443",
	}
	for _, target := range cases {
		t.Run(target, func(t *testing.T) {
			conn := dialProxy(t, addr)
			req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
			if _, err := conn.Write([]byte(req)); err != nil {
				t.Fatalf("writing: %v", err)
			}
			br := bufio.NewReader(conn)
			status, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("reading the status line: %v", err)
			}
			if !strings.Contains(status, "400") {
				t.Fatalf("status line = %q, want a 400 for %q", status, target)
			}
		})
	}
}

// TestConnectDefaultsMissingPort verifies the interop case where a client
// omits the port.
func TestConnectDefaultsMissingPort(t *testing.T) {
	rec := &recorder{}
	addr, _ := startInbound(t, rec, 0)

	conn := dialProxy(t, addr)
	if _, err := conn.Write([]byte("CONNECT example.com HTTP/1.1\r\nHost: example.com\r\n\r\n")); err != nil {
		t.Fatalf("writing: %v", err)
	}
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "200") {
		t.Fatalf("status line = %q, want a 200", status)
	}

	meta := rec.last(t)
	if port := meta.DestAddr.(*net.TCPAddr).Port; port != 443 {
		t.Errorf("port = %d, want the 443 default", port)
	}
}

// TestConnectToIPHasNoDomain verifies that an IP target is recorded as an
// address rather than being passed through as a domain.
//
// Keeping it as a domain would make the router match domain rules against an
// address, which is the accidental-match hazard the router guards against.
func TestConnectToIPHasNoDomain(t *testing.T) {
	rec := &recorder{}
	addr, _ := startInbound(t, rec, 0)

	conn := dialProxy(t, addr)
	if _, err := conn.Write([]byte("CONNECT 93.184.216.34:443 HTTP/1.1\r\nHost: 93.184.216.34:443\r\n\r\n")); err != nil {
		t.Fatalf("writing: %v", err)
	}
	br := bufio.NewReader(conn)
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatalf("reading the status line: %v", err)
	}

	meta := rec.last(t)
	if meta.Domain != "" {
		t.Errorf("Domain = %q, want empty for an IP target", meta.Domain)
	}
	tcp, ok := meta.DestAddr.(*net.TCPAddr)
	if !ok {
		t.Fatalf("DestAddr is %T, want *net.TCPAddr", meta.DestAddr)
	}
	if !tcp.IP.Equal(net.IPv4(93, 184, 216, 34)) {
		t.Errorf("DestAddr IP = %v, want 93.184.216.34", tcp.IP)
	}
}

// --- real HTTP through the proxy ------------------------------------------

// TestConnectToRealTLSServer verifies the inbound against a real TLS server
// through a real Go HTTP client configured to use the proxy.
//
// This is the test that proves the whole CONNECT path interoperates with a
// real client, rather than only with hand-written requests that encode the
// same assumptions the server does.
func TestConnectToRealTLSServer(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "through-proxy")
		fmt.Fprint(w, "body-from-target")
	}))
	defer target.Close()

	// The proxy tunnels to wherever the client says. It is wired to echo the
	// bytes to the real target so the client sees a real HTTP exchange.
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	targetAddr := targetURL.Host

	rec := &recorder{
		handle: func(ctx context.Context, client net.Conn, meta metadata.Metadata) error {
			upstream, err := net.DialTimeout("tcp", targetAddr, 3*time.Second)
			if err != nil {
				return err
			}
			defer upstream.Close()

			done := make(chan struct{}, 2)
			go func() {
				_, _ = io.Copy(upstream, client)
				done <- struct{}{}
			}()
			go func() {
				_, _ = io.Copy(client, upstream)
				done <- struct{}{}
			}()
			<-done
			return nil
		},
	}
	addr, _ := startInbound(t, rec, 0)

	proxyURL, err := url.Parse("http://" + addr.String())
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			DialContext:     (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		},
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("GET through the proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Test"); got != "through-proxy" {
		t.Errorf("X-Test header = %q, want through-proxy", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	if string(body) != "body-from-target" {
		t.Fatalf("body = %q, want body-from-target", body)
	}

	// httptest serves on 127.0.0.1, so the CONNECT target is a literal IP and
	// the inbound classifies it as an address rather than a name. Asserting a
	// domain here would be asserting the bug that leaves an address looking
	// like a name for domain rules to match against.
	meta := rec.last(t)
	if meta.Domain != "" {
		t.Errorf("Domain = %q, want empty for the loopback address", meta.Domain)
	}
	tcp, ok := meta.DestAddr.(*net.TCPAddr)
	if !ok {
		t.Fatalf("DestAddr is %T, want *net.TCPAddr", meta.DestAddr)
	}
	if !tcp.IP.IsLoopback() {
		t.Errorf("DestAddr IP = %v, want a loopback address", tcp.IP)
	}
	if wantPort := mustPort(t, targetURL.Port()); tcp.Port != wantPort {
		t.Errorf("DestAddr port = %d, want %d", tcp.Port, wantPort)
	}
}

// mustPort parses a URL port, failing the test if it is absent.
func mustPort(t *testing.T, s string) int {
	t.Helper()
	if s == "" {
		t.Fatal("the target URL has no port")
	}
	p, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("port %q is not a number: %v", s, err)
	}
	return p
}

// TestForwardAbsoluteURIRequest verifies the plain-HTTP proxy form.
func TestForwardAbsoluteURIRequest(t *testing.T) {
	rec := &recorder{}
	addr, _ := startInbound(t, rec, 0)

	conn := dialProxy(t, addr)
	// The absolute URI names a real host rather than the test server's
	// loopback address, because this path is about carrying a name through to
	// the engine. Nothing is dialed: the handler only records.
	req := "GET http://plain.example/ HTTP/1.1\r\nHost: plain.example\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("writing: %v", err)
	}

	// The handler is a recorder, so nothing replies; what matters here is that
	// the request was parsed and handed over with an absolute URI.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		n := len(rec.metas)
		rec.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	meta := rec.last(t)
	if meta.Domain != "plain.example" {
		t.Fatalf("Domain = %q, want plain.example", meta.Domain)
	}
	if meta.DestAddr == nil {
		t.Fatal("DestAddr is nil")
	}
	if port := meta.DestAddr.(*net.TCPAddr).Port; port != 80 {
		t.Errorf("port = %d, want the 80 default", port)
	}
}

// TestForwardRejectsRelativeURI verifies that a request addressed to the proxy
// itself is refused rather than treated as a proxied request.
func TestForwardRejectsRelativeURI(t *testing.T) {
	rec := &recorder{}
	addr, _ := startInbound(t, rec, 0)

	conn := dialProxy(t, addr)
	if _, err := conn.Write([]byte("GET /local HTTP/1.1\r\nHost: proxy\r\n\r\n")); err != nil {
		t.Fatalf("writing: %v", err)
	}

	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading the status line: %v", err)
	}
	if !strings.Contains(status, "400") {
		t.Fatalf("status line = %q, want a 400", status)
	}
}

// --- construction ---------------------------------------------------------

// TestNewRequiresHandler verifies that a missing handler is caught at
// construction.
//
// The alternative is a proxy that accepts connections and then does nothing
// with them, which looks like a network problem rather than a wiring mistake.
func TestNewRequiresHandler(t *testing.T) {
	if _, err := New(ir.Inbound{Tag: "x", Type: "http", Port: 8080}, registry.Deps{}); err == nil {
		t.Fatal("an inbound with no handler was constructed")
	}
}

// TestNewValidatesPort verifies the port range check.
func TestNewValidatesPort(t *testing.T) {
	rec := &recorder{}
	// Port 0 is deliberately absent: it means "any free port" and is valid.
	for _, port := range []int{-1, 70000} {
		if _, err := New(ir.Inbound{Tag: "x", Type: "http", Port: port}, registry.Deps{Handler: rec}); err == nil {
			t.Errorf("port %d was accepted", port)
		}
	}
}

// TestCloseIsIdempotent verifies that Close may be called repeatedly, since
// the engine's shutdown path cannot know how far startup got.
func TestCloseIsIdempotent(t *testing.T) {
	rec := &recorder{}
	addr, in := startInbound(t, rec, 0)
	if addr == nil {
		t.Fatal("no listener")
	}

	for i := 0; i < 3; i++ {
		if err := in.Close(); err != nil {
			t.Fatalf("Close call %d: %v", i, err)
		}
	}
}

// TestCloseBeforeStartIsSafe verifies the other shutdown ordering.
func TestCloseBeforeStartIsSafe(t *testing.T) {
	rec := &recorder{}
	in, err := New(ir.Inbound{Tag: "x", Type: "http", Port: 8080}, registry.Deps{Handler: rec})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := in.Close(); err != nil {
		t.Fatalf("Close before Start: %v", err)
	}
}

// TestReadHeaderTimeoutFromOptions verifies that the header timeout is read
// from the inbound's options map.
func TestReadHeaderTimeoutFromOptions(t *testing.T) {
	cases := []struct {
		name string
		opts map[string]any
		want time.Duration
	}{
		{"unset uses the default", nil, defaultReadHeaderTimeout},
		{"string duration", map[string]any{"read_header_timeout": "3s"}, 3 * time.Second},
		{"malformed falls back", map[string]any{"read_header_timeout": "nonsense"}, defaultReadHeaderTimeout},
		{"non-positive falls back", map[string]any{"read_header_timeout": "0s"}, defaultReadHeaderTimeout},
		{"integer seconds", map[string]any{"read_header_timeout": int64(7)}, 7 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := readHeaderTimeout(ir.Inbound{Options: c.opts})
			if got != c.want {
				t.Errorf("readHeaderTimeout = %s, want %s", got, c.want)
			}
		})
	}
}

// TestSelfRegistration verifies that importing this package registers it.
func TestSelfRegistration(t *testing.T) {
	found := false
	for _, name := range registry.InboundTypes() {
		if name == "http" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("the http inbound did not register itself; have %v", registry.InboundTypes())
	}
}
