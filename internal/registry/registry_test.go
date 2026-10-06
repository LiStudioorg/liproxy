// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package registry

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/inbound"
	"github.com/LiStudioorg/liproxy/internal/metadata"
	"github.com/LiStudioorg/liproxy/internal/outbound"
)

// --- test doubles ---------------------------------------------------------

// stubInbound is a minimal Inbound that records its construction.
type stubInbound struct {
	tag     string
	started bool
	closed  bool
}

func (s *stubInbound) Start(ctx context.Context) error {
	s.started = true
	<-ctx.Done()
	return nil
}

func (s *stubInbound) Close() error {
	s.closed = true
	return nil
}

// stubOutbound is a minimal Outbound that records its construction.
type stubOutbound struct {
	tag    string
	closed bool
	err    error
}

func (s *stubOutbound) Dial(context.Context, metadata.Metadata) (net.Conn, error) {
	return nil, s.err
}

func (s *stubOutbound) Close() error {
	s.closed = true
	return nil
}

// stubInboundFactory builds stubInbounds and remembers the last config it saw.
//
// The recorded fields are mutex-guarded because a factory is called from every
// connection goroutine in production, and the concurrency test exercises that.
// A bare field here would trip the race detector on the test double rather
// than on the code under test, which is a misleading failure.
type stubInboundFactory struct {
	name    string
	fail    error
	nilInst bool

	mu      sync.Mutex
	lastCfg ir.Inbound
	lastDep Deps
}

func (f *stubInboundFactory) Type() string { return f.name }

// seen returns the last configuration and dependencies the factory received.
func (f *stubInboundFactory) seen() (ir.Inbound, Deps) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastCfg, f.lastDep
}

func (f *stubInboundFactory) New(cfg ir.Inbound, deps Deps) (inbound.Inbound, error) {
	f.mu.Lock()
	f.lastCfg = cfg
	f.lastDep = deps
	f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	if f.nilInst {
		return nil, nil
	}
	return &stubInbound{tag: cfg.Tag}, nil
}

// stubOutboundFactory is the outbound counterpart.
type stubOutboundFactory struct {
	name    string
	fail    error
	nilInst bool
	dialErr error

	mu      sync.Mutex
	lastCfg ir.Outbound
}

func (f *stubOutboundFactory) Type() string { return f.name }

// seen returns the last configuration the factory received.
func (f *stubOutboundFactory) seen() ir.Outbound {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastCfg
}

func (f *stubOutboundFactory) New(cfg ir.Outbound, deps Deps) (outbound.Outbound, error) {
	f.mu.Lock()
	f.lastCfg = cfg
	f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	if f.nilInst {
		return nil, nil
	}
	return &stubOutbound{tag: cfg.Tag, err: f.dialErr}, nil
}

// registerUniquely registers a factory under a name unlikely to collide with
// any other test, and removes it afterwards.
//
// The registry is process-global and deliberately has no unregister, because
// production never needs one. Tests therefore have to clean up behind
// themselves, and must do so with the same lock the registry uses.
func registerUniquely[T any](t *testing.T, name string, f T) {
	t.Helper()
	switch v := any(f).(type) {
	case InboundFactory:
		inboundMu.Lock()
		inbounds[name] = v
		inboundMu.Unlock()
		t.Cleanup(func() {
			inboundMu.Lock()
			delete(inbounds, name)
			inboundMu.Unlock()
		})
	case OutboundFactory:
		outboundMu.Lock()
		outbounds[name] = v
		outboundMu.Unlock()
		t.Cleanup(func() {
			outboundMu.Lock()
			delete(outbounds, name)
			outboundMu.Unlock()
		})
	default:
		t.Fatalf("registerUniquely: unsupported factory type %T", f)
	}
}

// --- registration ---------------------------------------------------------

// TestRegisterInboundThenCreate verifies the basic round trip: what goes in
// under a type name comes back out for that type name.
func TestRegisterInboundThenCreate(t *testing.T) {
	f := &stubInboundFactory{name: "test-inbound-ok"}
	registerUniquely(t, f.name, f)

	got, err := NewInbound(ir.Inbound{Tag: "in", Type: "test-inbound-ok"}, Deps{})
	if err != nil {
		t.Fatalf("NewInbound: %v", err)
	}
	if got == nil {
		t.Fatal("NewInbound returned a nil inbound with no error")
	}
	if got, _ := f.seen(); got.Tag != "in" {
		t.Errorf("factory saw tag %q, want in", got.Tag)
	}
}

// TestRegisterOutboundThenCreate verifies the outbound round trip.
func TestRegisterOutboundThenCreate(t *testing.T) {
	f := &stubOutboundFactory{name: "test-outbound-ok"}
	registerUniquely(t, f.name, f)

	got, err := NewOutbound(ir.Outbound{Tag: "out", Type: "test-outbound-ok"}, Deps{})
	if err != nil {
		t.Fatalf("NewOutbound: %v", err)
	}
	if got == nil {
		t.Fatal("NewOutbound returned a nil outbound with no error")
	}
	if got := f.seen(); got.Tag != "out" {
		t.Errorf("factory saw tag %q, want out", got.Tag)
	}
}

// TestDuplicateInboundPanics verifies that registering one type name twice is
// rejected loudly.
//
// A silent second registration would replace the first and leave the losing
// protocol unreachable, with no indication of which implementation is actually
// running — the kind of thing that turns a one-line mistake into an afternoon
// of debugging.
func TestDuplicateInboundPanics(t *testing.T) {
	name := "test-inbound-dup"
	registerUniquely(t, name, &stubInboundFactory{name: name})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a duplicate inbound registration did not panic")
		}
		if msg, _ := r.(string); !strings.Contains(msg, name) {
			t.Errorf("panic message does not name the type: %v", r)
		}
	}()

	RegisterInbound(&stubInboundFactory{name: name})
}

// TestDuplicateOutboundPanics verifies the same for outbounds.
func TestDuplicateOutboundPanics(t *testing.T) {
	name := "test-outbound-dup"
	registerUniquely(t, name, &stubOutboundFactory{name: name})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a duplicate outbound registration did not panic")
		}
		if msg, _ := r.(string); !strings.Contains(msg, name) {
			t.Errorf("panic message does not name the type: %v", r)
		}
	}()

	RegisterOutbound(&stubOutboundFactory{name: name})
}

// TestRegisterNilFactoryPanics verifies that a nil factory is rejected during
// initialisation rather than panicking later on the first connection.
func TestRegisterNilFactoryPanics(t *testing.T) {
	cases := []struct {
		name string
		fn   func()
	}{
		{"nil inbound", func() { RegisterInbound(nil) }},
		{"nil outbound", func() { RegisterOutbound(nil) }},
		{"empty inbound type", func() { RegisterInbound(&stubInboundFactory{}) }},
		{"empty outbound type", func() { RegisterOutbound(&stubOutboundFactory{}) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s did not panic", c.name)
				}
			}()
			c.fn()
		})
	}
}

// --- lookup ---------------------------------------------------------------

// TestUnknownInboundTypeErrors verifies that an unregistered type produces an
// error naming the type and listing what is available.
func TestUnknownInboundTypeErrors(t *testing.T) {
	_, err := NewInbound(ir.Inbound{Tag: "x", Type: "test-definitely-not-registered"}, Deps{})
	if err == nil {
		t.Fatal("an unregistered inbound type was accepted")
	}
	if !errors.Is(err, ErrUnknownType) {
		t.Fatalf("err = %v, want ErrUnknownType", err)
	}
	if !strings.Contains(err.Error(), "test-definitely-not-registered") {
		t.Errorf("error does not name the type: %v", err)
	}
	if !strings.Contains(err.Error(), "registered:") {
		t.Errorf("error does not list registered types: %v", err)
	}
}

// TestUnknownOutboundTypeErrors verifies the same for outbounds.
func TestUnknownOutboundTypeErrors(t *testing.T) {
	_, err := NewOutbound(ir.Outbound{Tag: "x", Type: "test-definitely-not-registered"}, Deps{})
	if err == nil {
		t.Fatal("an unregistered outbound type was accepted")
	}
	if !errors.Is(err, ErrUnknownType) {
		t.Fatalf("err = %v, want ErrUnknownType", err)
	}
}

// TestEmptyTypeErrors verifies that a missing type is reported as such rather
// than as an unknown type, since the fixes differ.
func TestEmptyTypeErrors(t *testing.T) {
	if _, err := NewInbound(ir.Inbound{Tag: "x"}, Deps{}); err == nil {
		t.Error("an inbound with no type was accepted")
	} else if !strings.Contains(err.Error(), "has no type") {
		t.Errorf("error does not distinguish a missing type: %v", err)
	}

	if _, err := NewOutbound(ir.Outbound{Tag: "x"}, Deps{}); err == nil {
		t.Error("an outbound with no type was accepted")
	} else if !strings.Contains(err.Error(), "has no type") {
		t.Errorf("error does not distinguish a missing type: %v", err)
	}
}

// TestFactoryErrorIsWrapped verifies that a factory's own error survives, with
// the tag and type added, so the user learns both what failed and where.
func TestFactoryErrorIsWrapped(t *testing.T) {
	sentinel := errors.New("bad certificate")
	name := "test-inbound-failing"
	registerUniquely(t, name, &stubInboundFactory{name: name, fail: sentinel})

	_, err := NewInbound(ir.Inbound{Tag: "in", Type: name}, Deps{})
	if err == nil {
		t.Fatal("a failing factory produced no error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("the factory error was not preserved: %v", err)
	}
	if !strings.Contains(err.Error(), `"in"`) || !strings.Contains(err.Error(), name) {
		t.Errorf("error does not identify the inbound: %v", err)
	}
}

// TestNilInstanceIsRejected verifies that a factory returning (nil, nil) is
// caught here.
//
// Without the check the nil reaches the engine, which fails much later with a
// nil dereference far from the buggy factory.
func TestNilInstanceIsRejected(t *testing.T) {
	name := "test-inbound-nil"
	registerUniquely(t, name, &stubInboundFactory{name: name, nilInst: true})

	if _, err := NewInbound(ir.Inbound{Tag: "in", Type: name}, Deps{}); err == nil {
		t.Error("a factory returning a nil instance produced no error")
	}

	nameO := "test-outbound-nil"
	registerUniquely(t, nameO, &stubOutboundFactory{name: nameO, nilInst: true})
	if _, err := NewOutbound(ir.Outbound{Tag: "out", Type: nameO}, Deps{}); err == nil {
		t.Error("a factory returning a nil instance produced no error")
	}
}

// TestTypesAreSorted verifies that the type lists have a stable order, since
// they appear in error messages that tests and users compare.
func TestTypesAreSorted(t *testing.T) {
	registerUniquely(t, "test-zzz-in", &stubInboundFactory{name: "test-zzz-in"})
	registerUniquely(t, "test-aaa-in", &stubInboundFactory{name: "test-aaa-in"})

	got := InboundTypes()
	az := indexOf(got, "test-aaa-in")
	zz := indexOf(got, "test-zzz-in")
	if az < 0 || zz < 0 {
		t.Fatalf("registered types missing from %v", got)
	}
	if az > zz {
		t.Fatalf("types are not sorted: aaa at %d, zzz at %d", az, zz)
	}
}

// --- dependencies ---------------------------------------------------------

// TestDepsLoggerOrIsNeverNil verifies that a zero Deps still yields a usable
// logger, so protocol code needs no nil check on the hot path.
func TestDepsLoggerOrIsNeverNil(t *testing.T) {
	var d Deps
	if d.LoggerOr() == nil {
		t.Fatal("Deps{}.LoggerOr() returned nil")
	}
	// Must not panic: the disabled logger discards the event.
	d.LoggerOr().Info().Str("k", "v").Msg("discarded")

	logger := zerolog.Nop()
	d = Deps{Logger: &logger}
	if d.LoggerOr() != &logger {
		t.Error("LoggerOr did not return the configured logger")
	}
}

// TestDepsRouterIsPassedThrough verifies that the router given to a factory is
// the one the factory receives, since a dropped router would make routing fail
// with a confusing nil error.
func TestDepsRouterIsPassedThrough(t *testing.T) {
	name := "test-inbound-deps"
	f := &stubInboundFactory{name: name}
	registerUniquely(t, name, f)

	r := routerFunc(func(metadata.Metadata) (string, error) { return "direct", nil })
	if _, err := NewInbound(ir.Inbound{Tag: "in", Type: name}, Deps{Router: r}); err != nil {
		t.Fatalf("NewInbound: %v", err)
	}
	_, deps := f.seen()
	if deps.Router == nil {
		t.Fatal("the router was not passed to the factory")
	}
	got, err := deps.Router.Route(metadata.Metadata{})
	if err != nil || got != "direct" {
		t.Fatalf("Router.Route = (%q, %v), want (direct, nil)", got, err)
	}
}

// routerFunc adapts a function to the Router interface.
type routerFunc func(metadata.Metadata) (string, error)

func (f routerFunc) Route(m metadata.Metadata) (string, error) { return f(m) }

// TestRegistryConcurrentLookup verifies that lookups are safe under the race
// detector, since the engine resolves outbounds from many connection
// goroutines at once.
func TestRegistryConcurrentLookup(t *testing.T) {
	name := "test-outbound-concurrent"
	registerUniquely(t, name, &stubOutboundFactory{name: name})

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				if _, err := NewOutbound(ir.Outbound{Tag: "o", Type: name}, Deps{}); err != nil {
					t.Errorf("NewOutbound: %v", err)
					return
				}
				_ = OutboundTypes()
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

// indexOf returns the position of want in names, or -1.
func indexOf(names []string, want string) int {
	for i, n := range names {
		if n == want {
			return i
		}
	}
	return -1
}
