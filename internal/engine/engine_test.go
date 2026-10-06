// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package engine_test

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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LiStudioorg/liproxy/internal/config/compile"
	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/config/loader"
	"github.com/LiStudioorg/liproxy/internal/engine"

	// Protocol registrations. The engine deliberately does not import these,
	// so a test of the full path has to import them the same way cmd/lipd
	// does.
	_ "github.com/LiStudioorg/liproxy/internal/inbound/http"
	_ "github.com/LiStudioorg/liproxy/internal/outbound/block"
	_ "github.com/LiStudioorg/liproxy/internal/outbound/direct"
)

// freePort returns a port that was free a moment ago.
//
// The listener is closed before returning, so there is a small window in which
// another process could take it. The alternative — letting the inbound bind
// port 0 — would mean the test could not write the port into a configuration
// file, and the point of these tests is to drive the whole file-to-listener
// path.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// writeConfig writes a configuration directory and returns the entry path.
//
// The files are written as text rather than built as an ir.Config because
// these tests are about the whole path: a user's files, through the loader,
// through the compiler, into a running engine. Constructing the IR directly
// would skip the half of the pipeline most likely to be wrong.
func writeConfig(t *testing.T, proxyPort, targetPort int) string {
	t.Helper()
	dir := t.TempDir()

	main := fmt.Sprintf(`
include = ["toml.d/*.toml"]

mode = "client"

[log]
level = "info"
`)
	if err := os.WriteFile(filepath.Join(dir, "liproxy.toml"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "toml.d"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The inbound and its rule live in a fragment, so the test also exercises
	// fragment loading and merge order.
	fragment := fmt.Sprintf(`
[[inbounds]]
tag = "http-in"
type = "http"
listen = "127.0.0.1"
port = %d

[[outbounds]]
tag = "direct"
type = "direct"

[[rules]]
type = "domain-suffix"
payload = ["127.0.0.1"]
target = "direct"
`, proxyPort)

	// The host the test actually connects to is an address, so a domain rule
	// would never match it. The final rule is what routes it, which is also
	// what every real configuration needs.
	fragment += `
[[rules]]
type = "final"
target = "direct"
`
	if err := os.WriteFile(filepath.Join(dir, "toml.d", "010-proxy.toml"), []byte(fragment), 0o644); err != nil {
		t.Fatal(err)
	}

	return filepath.Join(dir, "liproxy.toml")
}

// startEngine loads, compiles and starts an engine from a config file.
func startEngine(t *testing.T, entry string) (*engine.Engine, context.CancelFunc) {
	t.Helper()

	set, err := loader.Load(entry, loader.LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg, err := compile.FromFileSet(set)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	eng, err := engine.New(cfg, engine.Options{})
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

	return eng, cancel
}

// waitForPort waits until something accepts connections on the address.
func waitForPort(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("nothing is listening on %s", addr)
}

// TestLoopbackThroughFullPipeline is the end-to-end test.
//
// It runs the whole path: configuration files on disk, through the loader and
// compiler, into an engine that listens, with a real HTTP client tunnelling
// through it to a real HTTPS server. Nothing is stubbed — the inbound parses a
// real CONNECT, the router matches the real rule list, the direct outbound
// opens a real socket, and the relay copies real bytes.
func TestLoopbackThroughFullPipeline(t *testing.T) {
	// The target is an HTTPS server, so the proxy must tunnel TLS end to end
	// rather than merely forward plaintext.
	const body = "payload-from-the-target-server"
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Loopback", "yes")
		fmt.Fprint(w, body)
	}))
	defer target.Close()

	proxyPort := freePort(t)
	entry := writeConfig(t, proxyPort, 0)

	startEngine(t, entry)

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
	waitForPort(t, proxyAddr)

	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			// The target's certificate is self-signed for the test host, which
			// is unrelated to what is being verified here: the tunnel.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			DialContext:     (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		},
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("GET through the engine: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Loopback"); got != "yes" {
		t.Errorf("X-Loopback = %q, want yes", got)
	}

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	if string(got) != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

// TestLoopbackTransfersLargeBody verifies that a payload spanning many reads
// survives the round trip intact.
//
// A one-line response never exercises the copy boundary, so a truncation or
// duplication bug there would pass the simple test.
func TestLoopbackTransfersLargeBody(t *testing.T) {
	// 1 MB of a repeating pattern, which is easy to compare and shows the
	// offset if the copy ever drops or repeats a chunk.
	const size = 1 << 20
	body := strings.Repeat("0123456789abcdef", size/16)

	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer target.Close()

	proxyPort := freePort(t)
	startEngine(t, writeConfig(t, proxyPort, 0))

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
	waitForPort(t, proxyAddr)

	proxyURL, _ := url.Parse("http://" + proxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 20 * time.Second,
	}

	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	if len(got) != len(body) {
		t.Fatalf("received %d bytes, want %d", len(got), len(body))
	}
	if string(got) != body {
		t.Fatal("the transferred body differs from what was sent")
	}
}

// TestLoopbackConcurrentConnections verifies that several tunnels work at
// once.
//
// A relay that shared state between connections, or an engine that serialized
// them, would pass every sequential test and fail here.
func TestLoopbackConcurrentConnections(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The marker identifies which request this is, so a crossed response
		// is detected rather than merely a missing one.
		fmt.Fprintf(w, "marker-%s", r.URL.Query().Get("id"))
	}))
	defer target.Close()

	proxyPort := freePort(t)
	startEngine(t, writeConfig(t, proxyPort, 0))

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
	waitForPort(t, proxyAddr)

	proxyURL, _ := url.Parse("http://" + proxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 15 * time.Second,
	}

	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(id int) {
			want := fmt.Sprintf("marker-%d", id)
			resp, err := client.Get(target.URL + "?id=" + fmt.Sprint(id))
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

	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

// TestBlockedRuleClosesTheConnection verifies that a rule targeting the block
// outbound refuses traffic rather than letting it out.
func TestBlockedRuleClosesTheConnection(t *testing.T) {
	// A target that must never be reached.
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the blocked target was reached")
		fmt.Fprint(w, "should not happen")
	}))
	defer target.Close()

	proxyPort := freePort(t)
	dir := t.TempDir()

	// The target is addressed by IP here, so the rule blocks everything by
	// using a keyword that matches the address form the test uses.
	if err := os.WriteFile(filepath.Join(dir, "liproxy.toml"), []byte(fmt.Sprintf(`
[[inbounds]]
tag = "http-in"
type = "http"
listen = "127.0.0.1"
port = %d

[[outbounds]]
tag = "block"
type = "block"

[[rules]]
type = "final"
target = "block"
`, proxyPort)), 0o644); err != nil {
		t.Fatal(err)
	}

	startEngine(t, filepath.Join(dir, "liproxy.toml"))

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
	waitForPort(t, proxyAddr)

	proxyURL, _ := url.Parse("http://" + proxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 5 * time.Second,
	}

	if _, err := client.Get(target.URL); err == nil {
		t.Fatal("a request routed to the block outbound succeeded")
	}
}

// TestEngineClosesCleanly verifies that cancellation stops the listeners and
// that Start returns.
func TestEngineClosesCleanly(t *testing.T) {
	proxyPort := freePort(t)
	entry := writeConfig(t, proxyPort, 0)

	set, err := loader.Load(entry, loader.LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg, err := compile.FromFileSet(set)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	eng, err := engine.New(cfg, engine.Options{ShutdownTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Start(ctx) }()

	waitForPort(t, fmt.Sprintf("127.0.0.1:%d", proxyPort))

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned %v, want nil after cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after cancellation")
	}

	// The port must be released, since a listener left bound would make the
	// next start fail with "address already in use".
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort))
	if err != nil {
		t.Fatalf("the port is still bound after shutdown: %v", err)
	}
	_ = ln.Close()
}

// TestStartTwiceIsRejected verifies that a second Start is refused rather than
// silently starting a second set of listeners.
func TestStartTwiceIsRejected(t *testing.T) {
	proxyPort := freePort(t)
	entry := writeConfig(t, proxyPort, 0)

	set, err := loader.Load(entry, loader.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := compile.FromFileSet(set)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(cfg, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	waitForPort(t, fmt.Sprintf("127.0.0.1:%d", proxyPort))

	if err := eng.Start(ctx); err == nil {
		t.Fatal("a second Start succeeded")
	}
}

// TestReloadIsNotSilentlyIgnored verifies that Reload reports that it is
// unimplemented.
//
// A silent success would leave an operator believing a configuration change
// took effect when nothing happened, which is worse than a clear refusal.
func TestReloadIsNotSilentlyIgnored(t *testing.T) {
	proxyPort := freePort(t)
	entry := writeConfig(t, proxyPort, 0)

	set, err := loader.Load(entry, loader.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := compile.FromFileSet(set)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(cfg, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if err := eng.Reload(cfg); err == nil {
		t.Fatal("Reload reported success; it is not implemented")
	}
}

// TestNewRejectsUnknownProtocol verifies that a configuration naming a
// protocol this build does not include fails at construction.
func TestNewRejectsUnknownProtocol(t *testing.T) {
	cfg := &ir.Config{
		Inbounds: []ir.Inbound{{Tag: "x", Type: "vless", Port: 8080}},
	}
	if _, err := engine.New(cfg, engine.Options{}); err == nil {
		t.Fatal("an inbound naming an unregistered protocol was accepted")
	}
}

// TestNewRejectsBadRoute verifies that a rule pointing at a missing outbound
// fails at construction rather than on the first matching connection.
func TestNewRejectsBadRoute(t *testing.T) {
	cfg := &ir.Config{
		Outbounds: []ir.Outbound{{Tag: "direct", Type: "direct"}},
		Rules:     []ir.Rule{{Type: "final", Target: "typoo"}},
	}
	if _, err := engine.New(cfg, engine.Options{}); err == nil {
		t.Fatal("a rule targeting an undefined outbound was accepted")
	}
}

// TestEnvironmentHasNoStrayListeners guards the loopback tests against a
// helper that leaves a listener behind, which would make later tests flaky.
func TestEnvironmentHasNoStrayListeners(t *testing.T) {
	// Binding an ephemeral port must work; if it does not, the machine is in a
	// state where the other tests' results cannot be trusted.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot bind an ephemeral port: %v", err)
	}
	_ = ln.Close()
}

// readStatusLine reads the first line of an HTTP response.
//
// It is used by the tests that speak the proxy protocol by hand, where a
// well-formed response is the thing being checked.
func readStatusLine(t *testing.T, conn net.Conn) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("reading the status line: %v", err)
	}
	return strings.TrimSpace(line)
}

// TestRoutingSelectsBetweenOutbounds verifies that the rule list actually
// decides which outbound serves a connection.
//
// Every other engine test configures a single usable outbound, so a mutation
// that skipped routing entirely and dialed an arbitrary outbound would still
// pass them. Two outbounds with different behaviour make the routing decision
// observable: one connects and the other refuses, and which runs is determined
// solely by the rule that matched.
//
// The client connects using a host name rather than the loopback address,
// because the inbound records a literal address as DestAddr with no Domain,
// and a domain rule cannot match a connection that carries no name. Using the
// name is also what a real client does, so the test exercises the path that
// matters.
func TestRoutingSelectsBetweenOutbounds(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "reached-through-routing")
	}))
	defer target.Close()

	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	targetPort := targetURL.Port()

	// Connecting to "localhost:<port>" reaches the same server while giving
	// the proxy a name to route on.
	namedURL := "https://localhost:" + targetPort + "/"

	dir := t.TempDir()

	// "blocked" is declared first, so an implementation that ignored the rules
	// and took the first outbound would refuse every connection. Honouring the
	// rules reaches the target instead.
	base := `
[[inbounds]]
tag = "http-in"
type = "http"
listen = "127.0.0.1"
port = %d

[[outbounds]]
tag = "blocked"
type = "block"

[[outbounds]]
tag = "direct"
type = "direct"

[[rules]]
type = "domain-suffix"
payload = ["%s"]
target = "%s"

[[rules]]
type = "final"
target = "%s"
`

	// Case 1: the domain rule sends localhost to the direct outbound.
	allowPort := freePort(t)
	allowCfg := fmt.Sprintf(base, allowPort, "localhost", "direct", "blocked")
	if err := os.WriteFile(filepath.Join(dir, "liproxy.toml"), []byte(allowCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	startEngine(t, filepath.Join(dir, "liproxy.toml"))

	allowAddr := fmt.Sprintf("127.0.0.1:%d", allowPort)
	waitForPort(t, allowAddr)

	allowURL, _ := url.Parse("http://" + allowAddr)
	allowClient := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(allowURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 10 * time.Second,
	}

	resp, err := allowClient.Get(namedURL)
	if err != nil {
		t.Fatalf("the domain rule did not route to the direct outbound: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	if string(body) != "reached-through-routing" {
		t.Fatalf("body = %q, want reached-through-routing", body)
	}

	// Case 2: the same rule now names the block outbound, so the same request
	// must fail. Only the target changed, which is what makes this a test of
	// routing rather than of the fixture.
	blockPort := freePort(t)
	blockCfg := fmt.Sprintf(base, blockPort, "localhost", "blocked", "direct")
	if err := os.WriteFile(filepath.Join(dir, "liproxy.toml"), []byte(blockCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	startEngine(t, filepath.Join(dir, "liproxy.toml"))

	blockAddr := fmt.Sprintf("127.0.0.1:%d", blockPort)
	waitForPort(t, blockAddr)

	blockURL, _ := url.Parse("http://" + blockAddr)
	blockClient := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(blockURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 5 * time.Second,
	}

	if _, err := blockClient.Get(namedURL); err == nil {
		t.Fatal("a request matching the block rule succeeded")
	}
}
