// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package engine_test

import (
	"context"
	"crypto/tls"
	"errors"
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/LiStudioorg/liproxy/internal/config/compile"
	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/config/loader"
	"github.com/LiStudioorg/liproxy/internal/engine"
	"github.com/LiStudioorg/liproxy/internal/metadata"
	"github.com/LiStudioorg/liproxy/internal/outbound"
	"github.com/LiStudioorg/liproxy/internal/outbound/outbounderr"
	"github.com/LiStudioorg/liproxy/internal/registry"
	"github.com/LiStudioorg/liproxy/internal/router"
)

// --- a fake outbound whose lifecycle is observable ------------------------

// probeOutbound is a direct outbound that counts what was done to it.
//
// It exists because "the old outbound is closed once its last connection ends"
// is not observable from the outside. The real direct outbound has no state a
// test can read, so a reload test using it could only check that the new rules
// route differently — it could not tell a drained-and-closed generation from
// one that was closed early, nor from one that leaked.
type probeOutbound struct {
	tag string

	mu     sync.Mutex
	closed bool
	dials  int
	// lastMeta records the metadata of the most recent dial, so a test can
	// assert which address the dial actually went to.
	lastMeta metadata.Metadata

	// onDial supplies the connection. It is read from the owning factory on
	// every dial, not captured at construction, because a reload builds new
	// instances from the same factory: capturing it once would leave the
	// post-reload instance without one, and the test would fail for a reason
	// that has nothing to do with reloading.
	owner *probeFactory
}

func (p *probeOutbound) Dial(_ context.Context, meta metadata.Metadata) (net.Conn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errors.New("probe outbound is closed")
	}
	p.dials++
	p.lastMeta = meta
	owner := p.owner
	p.mu.Unlock()

	if onDial := owner.dialFunction(); onDial != nil {
		c := onDial()
		if c == nil {
			return nil, outbounderr.ErrBlocked
		}
		return c, nil
	}
	return nil, errors.New("probe outbound has no dial function")
}

func (p *probeOutbound) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

func (p *probeOutbound) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *probeOutbound) dialCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dials
}

// probeFactory registers a probe outbound under a type name of its own, so a
// test configuration can ask for one by naming the type.
type probeFactory struct {
	typ string
	// made collects every instance this factory produced, in order.
	mu   sync.Mutex
	made []*probeOutbound
	// dialFn is installed on each new instance before it is returned.
	dialFn func() net.Conn
}

func (f *probeFactory) Type() string { return f.typ }

func (f *probeFactory) New(cfg ir.Outbound, _ registry.Deps) (outbound.Outbound, error) {
	p := &probeOutbound{tag: cfg.Tag, owner: f}
	f.mu.Lock()
	f.made = append(f.made, p)
	f.mu.Unlock()
	return p, nil
}

// dialFunction returns the connection supplier currently installed.
func (f *probeFactory) dialFunction() func() net.Conn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dialFn
}

// setDial installs the connection supplier for this factory, including
// instances built later by a reload.
func (f *probeFactory) setDial(fn func() net.Conn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dialFn = fn
}

// latest returns the most recently created instance.
//
// A reload builds a new instance of every outbound, so an assertion about what
// the running configuration dials has to look at the newest one. Indexing from
// the start reads a retired generation and reports zero dials, which looks like
// a broken reload when the reload in fact worked.
func (f *probeFactory) latest(t *testing.T) *probeOutbound {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.made) == 0 {
		t.Fatalf("factory %q produced no instances", f.typ)
	}
	return f.made[len(f.made)-1]
}

func (f *probeFactory) instances() []*probeOutbound {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*probeOutbound, len(f.made))
	copy(out, f.made)
	return out
}

// probeSeq makes each registered probe type name unique.
//
// The registry is global and rejects a duplicate registration, and a single
// test may legitimately register more than one probe. Deriving the name from
// the test alone would collide in exactly that case, so a counter is appended.
var probeSeq atomic.Int64

// registerProbe registers a probe factory under a unique type name and returns
// it along with the type name the configuration should use.
func registerProbe(t *testing.T, dialFn func() net.Conn) (*probeFactory, string) {
	t.Helper()
	typ := fmt.Sprintf("probe-%s-%d",
		strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()),
		probeSeq.Add(1))
	f := &probeFactory{typ: typ, dialFn: dialFn}
	registry.RegisterOutbound(f)
	return f, typ
}

// echoServer starts a TCP server that echoes what it receives.
//
// The reload tests need a target that stays up across the reload, so that a
// connection surviving the swap has something real to talk to.
func echoServer(t *testing.T) net.Addr {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	return ln.Addr()
}

// --- helpers --------------------------------------------------------------

// buildConfig compiles a configuration without touching the filesystem, for
// tests that need a specific outbound type the loader cannot express.
func buildConfig(t *testing.T, cfg *ir.Config) *engine.Engine {
	t.Helper()
	eng, err := engine.New(cfg, engine.Options{ShutdownTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

// writeReloadableConfig writes a configuration whose rule list can be swapped
// between two outbound types.
func writeReloadableConfig(t *testing.T, targetType, fallbackType string, proxyPort int) string {
	t.Helper()
	dir := t.TempDir()
	text := fmt.Sprintf(`
[[inbounds]]
tag = "http-in"
type = "http"
listen = "127.0.0.1"
port = %d

[[outbounds]]
tag = "primary"
type = "%s"

[[outbounds]]
tag = "fallback"
type = "%s"

[[rules]]
type = "final"
target = "primary"
`, proxyPort, targetType, fallbackType)
	path := filepath.Join(dir, "liproxy.toml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// loadConfig runs the real loader and compiler over a path.
func loadConfig(t *testing.T, path string) *ir.Config {
	t.Helper()
	set, err := loader.Load(path, loader.LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg, err := compile.FromFileSet(set)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return cfg
}

// --- reload tests ---------------------------------------------------------

// TestReloadChangesRouting verifies that a reload takes effect for new
// connections.
//
// The two configurations differ only in which outbound the final rule names,
// so a change in which outbound is dialed can only come from the reload.
func TestReloadChangesRouting(t *testing.T) {
	probeA, typeA := registerProbe(t, nil)
	probeB, typeB := registerProbe(t, nil)

	addr := echoServer(t)

	proxyPort := freePort(t)
	path := writeReloadableConfig(t, typeA, typeB, proxyPort)
	cfg := loadConfig(t, path)

	// Both probe types refuse to dial unless given a connection, so the
	// exchanges below are driven by a real echo server wired in per instance.
	connTo := func() net.Conn {
		c, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
		if err != nil {
			return nil
		}
		return c
	}
	probeA.setDial(connTo)
	probeB.setDial(connTo)

	eng := buildConfig(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- eng.Start(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
	waitForPort(t, proxyAddr)

	exchange := func() error {
		conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()

		req := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"
		if _, err := conn.Write([]byte(req)); err != nil {
			return err
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err != nil {
			return fmt.Errorf("reading the CONNECT response: %w", err)
		}
		if !strings.Contains(string(buf[:n]), "200") {
			return fmt.Errorf("CONNECT response = %q", buf[:n])
		}
		return nil
	}

	if err := exchange(); err != nil {
		t.Fatalf("first request: %v", err)
	}
	if got := probeA.latest(t).dialCount(); got != 1 {
		t.Fatalf("before reload the primary outbound was dialed %d times, want 1", got)
	}

	// Swap the final rule to the other outbound and reload.
	cfg2 := loadConfig(t, path)
	for i := range cfg2.Rules {
		cfg2.Rules[i].Target = "fallback"
	}
	if err := eng.Reload(cfg2); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// A new connection must now go through the fallback outbound.
	if err := exchange(); err != nil {
		t.Fatalf("request after reload: %v", err)
	}
	if got := probeB.latest(t).dialCount(); got != 1 {
		t.Fatalf("after reload the fallback outbound was dialed %d times, want 1", got)
	}
	// The retired generation's instance is the first one; it must not have
	// been used again, and it must have been closed.
	if got := probeA.instances()[0].dialCount(); got != 1 {
		t.Errorf("the retired primary outbound was dialed again after the reload (%d times)", got)
	}
	if !probeA.instances()[0].isClosed() {
		t.Error("the retired primary outbound was not closed")
	}
	if !probeB.instances()[0].isClosed() {
		t.Error("the retired fallback outbound was not closed")
	}
}

// TestFailedReloadLeavesRunningConfigIntact verifies that a rejected reload
// changes nothing.
//
// This is the property that makes the configuration file safe to edit: a
// mistake on disk must not take down a running service, and must not leave it
// half-configured.
func TestFailedReloadLeavesRunningConfigIntact(t *testing.T) {
	probe, typ := registerProbe(t, nil)
	addr := echoServer(t)
	probe.setDial(func() net.Conn {
		c, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
		if err != nil {
			return nil
		}
		return c
	})

	proxyPort := freePort(t)
	path := writeReloadableConfig(t, typ, "direct", proxyPort)
	eng := buildConfig(t, loadConfig(t, path))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- eng.Start(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	waitForPort(t, fmt.Sprintf("127.0.0.1:%d", proxyPort))

	before := eng.Config()

	// A rule naming an outbound that does not exist. The router rejects this
	// at construction, which is exactly the failure mode a typo in a config
	// file produces.
	bad := loadConfig(t, path)
	for i := range bad.Rules {
		bad.Rules[i].Target = "nonexistent-outbound"
	}
	if err := eng.Reload(bad); err == nil {
		t.Fatal("a reload naming an undefined outbound was accepted")
	}

	if eng.Config() != before {
		t.Fatal("a failed reload replaced the running configuration")
	}

	// The service must still work: the outbound was never replaced and never
	// closed.
	if probe.latest(t).isClosed() {
		t.Fatal("a failed reload closed the running outbound")
	}

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), 2*time.Second)
	if err != nil {
		t.Fatalf("the proxy stopped accepting after a failed reload: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")); err != nil {
		t.Fatalf("writing after a failed reload: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("reading after a failed reload: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "200") {
		t.Fatalf("response after a failed reload = %q", buf[:n])
	}
	if got := probe.latest(t).dialCount(); got != 1 {
		t.Errorf("the surviving outbound was dialed %d times, want 1", got)
	}
}

// TestReloadRejectsInboundChange verifies that a reload which would need to
// rebind a listener is refused rather than half-applied.
func TestReloadRejectsInboundChange(t *testing.T) {
	_, typ := registerProbe(t, nil)

	proxyPort := freePort(t)
	path := writeReloadableConfig(t, typ, "direct", proxyPort)
	eng := buildConfig(t, loadConfig(t, path))

	before := eng.Config()

	cfg := loadConfig(t, path)
	cfg.Inbounds[0].Port = proxyPort + 1

	err := eng.Reload(cfg)
	if err == nil {
		t.Fatal("a reload changing the listening port was accepted")
	}
	if !errors.Is(err, engine.ErrInboundChangeRequiresRestart) {
		t.Fatalf("err = %v, want ErrInboundChangeRequiresRestart", err)
	}
	// The message must name both sides, or the operator cannot tell which
	// listener the change refers to.
	msg := err.Error()
	if !strings.Contains(msg, fmt.Sprint(proxyPort)) || !strings.Contains(msg, fmt.Sprint(proxyPort+1)) {
		t.Errorf("error does not name both ports: %s", msg)
	}
	if eng.Config() != before {
		t.Fatal("a refused reload replaced the running configuration")
	}
}

// TestReloadAllowsNonListenerInboundChanges verifies that a change which does
// not affect binding is accepted.
//
// Refusing every inbound edit would force a restart to change a header
// timeout, which the running listener can adopt without rebinding.
func TestReloadAllowsNonListenerInboundChanges(t *testing.T) {
	_, typ := registerProbe(t, nil)

	proxyPort := freePort(t)
	path := writeReloadableConfig(t, typ, "direct", proxyPort)
	eng := buildConfig(t, loadConfig(t, path))

	cfg := loadConfig(t, path)
	cfg.Inbounds[0].Options = map[string]any{"read_header_timeout": "3s"}

	if err := eng.Reload(cfg); err != nil {
		t.Fatalf("a reload changing only an option was refused: %v", err)
	}
}

// TestRetiredOutboundClosedAfterLastConnection verifies the draining contract:
// the old generation is closed when its last connection finishes, not while
// one is still running and not never.
func TestRetiredOutboundClosedAfterLastConnection(t *testing.T) {
	// The probe hands out a connection whose lifetime the test controls, so
	// the connection stays open across the reload.
	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()
	defer clientSide.Close()

	probe, typ := registerProbe(t, func() net.Conn { return clientSide })

	proxyPort := freePort(t)
	path := writeReloadableConfig(t, typ, "direct", proxyPort)
	eng := buildConfig(t, loadConfig(t, path))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- eng.Start(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	waitForPort(t, fmt.Sprintf("127.0.0.1:%d", proxyPort))

	// Open one connection and keep it alive through the reload.
	client, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if _, err := client.Write([]byte("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")); err != nil {
		t.Fatalf("writing CONNECT: %v", err)
	}
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	if _, err := client.Read(buf); err != nil {
		t.Fatalf("reading the CONNECT response: %v", err)
	}

	// Wait until the dial has definitely happened, so the connection holds a
	// reference before the reload.
	old := probe.latest(t)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if old.dialCount() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if old.dialCount() != 1 {
		t.Fatal("the connection was never dialed")
	}

	// Reload while that connection is still open.
	cfg2 := loadConfig(t, path)
	for i := range cfg2.Rules {
		cfg2.Rules[i].Target = "fallback"
	}
	if err := eng.Reload(cfg2); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if old.isClosed() {
		t.Fatal("the retired outbound was closed while a connection was still using it")
	}

	// Ending the connection must release the last reference and close it.
	_ = client.Close()
	_ = clientSide.Close()

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if old.isClosed() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the retired outbound was never closed after its last connection ended")
}

// TestReloadWithNoConnectionsClosesImmediately verifies the other half of the
// draining rule: when nothing is using the old generation it is closed at
// once, not left open until some later event that may never come.
func TestReloadWithNoConnectionsClosesImmediately(t *testing.T) {
	probe, typ := registerProbe(t, nil)

	proxyPort := freePort(t)
	path := writeReloadableConfig(t, typ, "direct", proxyPort)
	eng := buildConfig(t, loadConfig(t, path))

	cfg2 := loadConfig(t, path)
	for i := range cfg2.Rules {
		cfg2.Rules[i].Target = "fallback"
	}
	if err := eng.Reload(cfg2); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// The pre-reload instance must be closed, and the post-reload instance
	// must not be: closing the new generation would retire the configuration
	// the reload just installed.
	instances := probe.instances()
	if len(instances) < 2 {
		t.Fatalf("expected a generation per configuration, got %d", len(instances))
	}
	if !instances[0].isClosed() {
		t.Error("the retired generation was not closed by the reload")
	}
	if instances[len(instances)-1].isClosed() {
		t.Error("the newly installed generation was closed")
	}
}

// TestConcurrentReloadIsRaceFree exercises reload alongside live traffic.
//
// Run under -race, this is what checks the reference accounting: a state
// released twice, or acquired outside the critical section that reads it,
// shows up here as a data race or as outbounds closed under a live dial.
func TestConcurrentReloadIsRaceFree(t *testing.T) {
	probe, typ := registerProbe(t, nil)
	addr := echoServer(t)
	probe.setDial(func() net.Conn {
		c, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
		if err != nil {
			return nil
		}
		return c
	})

	proxyPort := freePort(t)
	path := writeReloadableConfig(t, typ, "direct", proxyPort)
	eng := buildConfig(t, loadConfig(t, path))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- eng.Start(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	waitForPort(t, fmt.Sprintf("127.0.0.1:%d", proxyPort))

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)

	var wg sync.WaitGroup

	// Traffic in the background, so reloads land while connections are being
	// served and references are being taken and released.
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				conn, err := net.DialTimeout("tcp", proxyAddr, time.Second)
				if err != nil {
					continue
				}
				_, _ = conn.Write([]byte("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"))
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				_, _ = conn.Read(make([]byte, 64))
				_ = conn.Close()
			}
		}()
	}

	// Reloads from several goroutines at once, alternating the target so the
	// generations really do differ.
	var reloadErrs atomic.Int64
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				cfg := loadConfig(t, path)
				target := "primary"
				if (id+j)%2 == 0 {
					target = "fallback"
				}
				for k := range cfg.Rules {
					cfg.Rules[k].Target = target
				}
				if err := eng.Reload(cfg); err != nil {
					reloadErrs.Add(1)
				}
			}
		}(i)
	}

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()

	if n := reloadErrs.Load(); n > 0 {
		t.Fatalf("%d reloads failed during concurrent traffic", n)
	}
}

// TestReloadAfterCloseIsRejected verifies that reloading a stopped engine is
// refused rather than building state nobody will ever close.
func TestReloadAfterCloseIsRejected(t *testing.T) {
	_, typ := registerProbe(t, nil)

	proxyPort := freePort(t)
	path := writeReloadableConfig(t, typ, "direct", proxyPort)
	eng, err := engine.New(loadConfig(t, path), engine.Options{})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := eng.Reload(loadConfig(t, path)); err == nil {
		t.Fatal("Reload succeeded on a closed engine")
	}
}

// TestReloadRejectsNilConfig verifies the nil guard.
func TestReloadRejectsNilConfig(t *testing.T) {
	_, typ := registerProbe(t, nil)

	proxyPort := freePort(t)
	path := writeReloadableConfig(t, typ, "direct", proxyPort)
	eng := buildConfig(t, loadConfig(t, path))

	if err := eng.Reload(nil); err == nil {
		t.Fatal("a nil configuration was accepted")
	}
}

// TestReloadPreservesExistingConnection verifies that a reload does not
// interrupt traffic already in flight.
//
// Cutting a connection on reload would turn a configuration change into a
// visible outage for anyone downloading something, which is precisely what
// hot reload exists to avoid.
func TestReloadPreservesExistingConnection(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()

	// Only the type name matters here; the instances are not inspected.
	_, typ := registerProbe(t, func() net.Conn { return clientSide })

	proxyPort := freePort(t)
	path := writeReloadableConfig(t, typ, "direct", proxyPort)
	eng := buildConfig(t, loadConfig(t, path))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- eng.Start(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	waitForPort(t, fmt.Sprintf("127.0.0.1:%d", proxyPort))

	client, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if _, err := client.Write([]byte("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")); err != nil {
		t.Fatalf("writing CONNECT: %v", err)
	}
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	if _, err := client.Read(buf); err != nil {
		t.Fatalf("reading the CONNECT response: %v", err)
	}

	// Reload, then use the established tunnel. The bytes travel over the
	// pinned connection from before the swap.
	cfg2 := loadConfig(t, path)
	for i := range cfg2.Rules {
		cfg2.Rules[i].Target = "fallback"
	}
	if err := eng.Reload(cfg2); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// The relay is copying client -> serverSide, so writing on the client must
	// surface on the server side of the pipe even though a reload happened.
	go func() {
		_, _ = client.Write([]byte("still-connected"))
	}()

	got := make([]byte, len("still-connected"))
	_ = serverSide.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(serverSide, got); err != nil {
		t.Fatalf("the established tunnel stopped working after a reload: %v", err)
	}
	if string(got) != "still-connected" {
		t.Fatalf("received %q, want still-connected", got)
	}
}

// --- live end-to-end reload over the real pipeline ------------------------

// TestLiveReloadThroughRealPipeline drives a reload against a running engine
// built from actual configuration files, then makes a real request.
//
// The fake-outbound tests above can observe lifecycle but not the whole path.
// This one uses the real direct outbound and a real HTTPS target, so it proves
// that a reloaded configuration serves traffic end to end.
func TestLiveReloadThroughRealPipeline(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Generation", "current")
		fmt.Fprint(w, "served-after-reload")
	}))
	defer target.Close()

	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	namedURL := "https://localhost:" + targetURL.Port() + "/"

	proxyPort := freePort(t)
	dir := t.TempDir()

	// The first configuration blocks everything; the second routes to direct.
	// Reloading between them turns a refusing proxy into a working one, which
	// no cached or stale configuration could produce.
	blocked := fmt.Sprintf(`
[[inbounds]]
tag = "http-in"
type = "http"
listen = "127.0.0.1"
port = %d

[[outbounds]]
tag = "out"
type = "block"

[[rules]]
type = "final"
target = "out"
`, proxyPort)

	allowed := fmt.Sprintf(`
[[inbounds]]
tag = "http-in"
type = "http"
listen = "127.0.0.1"
port = %d

[[outbounds]]
tag = "out"
type = "direct"

[[rules]]
type = "final"
target = "out"
`, proxyPort)

	path := filepath.Join(dir, "liproxy.toml")
	if err := os.WriteFile(path, []byte(blocked), 0o644); err != nil {
		t.Fatal(err)
	}

	eng, err := engine.New(loadConfig(t, path), engine.Options{ShutdownTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- eng.Start(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

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

	if _, err := client.Get(namedURL); err == nil {
		t.Fatal("the blocking configuration let a request through")
	}

	if err := os.WriteFile(path, []byte(allowed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := eng.Reload(loadConfig(t, path)); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	resp, err := client.Get(namedURL)
	if err != nil {
		t.Fatalf("the reloaded configuration did not serve the request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	if string(body) != "served-after-reload" {
		t.Fatalf("body = %q, want served-after-reload", body)
	}
}

// TestRoutingFollowsReloadedRules verifies that a rule change, not just an
// outbound change, takes effect.
//
// The router is the piece that has to be rebuilt on reload, so a test that
// only swapped outbounds would not notice a router left behind.
func TestRoutingFollowsReloadedRules(t *testing.T) {
	probe, typ := registerProbe(t, nil)
	addr := echoServer(t)
	probe.setDial(func() net.Conn {
		c, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
		if err != nil {
			return nil
		}
		return c
	})

	proxyPort := freePort(t)
	path := writeReloadableConfig(t, typ, "direct", proxyPort)
	eng := buildConfig(t, loadConfig(t, path))

	before, err := eng.Route(metadata.Metadata{Domain: "example.com"})
	if err != nil {
		t.Fatalf("Route before reload: %v", err)
	}
	if before != "primary" {
		t.Fatalf("Route before reload = %q, want primary", before)
	}

	cfg := loadConfig(t, path)
	// Replace the final rule with a suffix rule, so the matching logic itself
	// changes and not merely the target.
	cfg.Rules = []ir.Rule{{
		Type:    "domain-suffix",
		Payload: []string{"example.com"},
		Target:  "fallback",
	}}
	if err := eng.Reload(cfg); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	got, err := eng.Route(metadata.Metadata{Domain: "a.example.com"})
	if err != nil {
		t.Fatalf("Route after reload: %v", err)
	}
	if got != "fallback" {
		t.Fatalf("Route after reload = %q, want fallback", got)
	}

	// The new rule set has no final rule, so an unrelated domain must now be
	// reported as unmatched. This confirms the whole rule list was replaced
	// rather than the old one being extended.
	if _, err := eng.Route(metadata.Metadata{Domain: "other.net"}); !errors.Is(err, router.ErrNoMatch) {
		t.Fatalf("Route for an unmatched domain = %v, want ErrNoMatch", err)
	}
}
