// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/inbound"
	"github.com/LiStudioorg/liproxy/internal/metadata"
	"github.com/LiStudioorg/liproxy/internal/outbound"
	"github.com/LiStudioorg/liproxy/internal/registry"
	"github.com/LiStudioorg/liproxy/internal/relay"
	"github.com/LiStudioorg/liproxy/internal/router"
)

// Options configures an Engine.
type Options struct {
	// Logger receives engine diagnostics. A nil value disables logging.
	Logger *zerolog.Logger

	// ShutdownTimeout bounds how long Start waits for inbounds to stop.
	//
	// It exists because an inbound may be blocked in a read that only
	// cancellation unblocks, and waiting forever on a clean shutdown turns a
	// stop request into a hang.
	ShutdownTimeout time.Duration
}

// defaultShutdownTimeout bounds shutdown when none is configured.
const defaultShutdownTimeout = 5 * time.Second

// logger is the subset of zerolog the engine uses, named so that the state
// types can log without depending on the concrete logger type.
type logger = zerolog.Logger

// Engine is a running proxy service.
type Engine struct {
	logger *zerolog.Logger
	opts   Options

	// slot owns the current configuration generation and the reference count
	// for every generation built so far.
	//
	// Routing and outbound lookup both go through it, which is what lets a
	// reload replace the configuration while connections are in flight.
	slot stateSlot

	// inbounds are built once in New and never replaced, because a listener
	// that is rebound loses its port and every connection queued on it. A
	// configuration that would change them is refused by Reload.
	inbounds []inbound.Inbound

	mu      sync.Mutex
	started bool
	closed  bool
}

// New builds an engine from a compiled configuration.
//
// All construction happens here, before anything listens: the router is built
// and every inbound and outbound is instantiated. A configuration that names a
// missing protocol, an unsupported rule type or a rule pointing at an unknown
// outbound therefore fails here rather than after the service is accepting
// traffic.
func New(cfg *ir.Config, opts Options) (*Engine, error) {
	if cfg == nil {
		return nil, errors.New("engine: nil configuration")
	}

	logger := opts.Logger
	if logger == nil {
		disabled := zerolog.Nop()
		logger = &disabled
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = defaultShutdownTimeout
	}

	e := &Engine{
		logger: logger,
		opts:   opts,
	}

	// Deps carries both capabilities: Handler for inbounds, which hand over
	// accepted connections, and Router for outbounds that chain to another
	// outbound. The engine implements both, so the same value serves either.
	deps := registry.Deps{Logger: logger, Router: e, Handler: e}

	st, err := newState(cfg, deps)
	if err != nil {
		return nil, err
	}
	e.slot.current = st

	for _, inCfg := range cfg.Inbounds {
		in, err := registry.NewInbound(inCfg, deps)
		if err != nil {
			// The state built above owns outbounds that would otherwise leak.
			// It is retired through the normal path so that construction
			// failure and a later reload clean up identically.
			st.retire(*logger)
			return nil, err
		}
		e.inbounds = append(e.inbounds, in)
	}

	logger.Info().
		Int("inbounds", len(cfg.Inbounds)).
		Int("outbounds", len(cfg.Outbounds)).
		Int("rules", st.router.Len()).
		Msg("engine constructed")

	return e, nil
}

// newState builds one configuration generation.
//
// Every fallible step happens here, before anything is swapped: the router is
// built and validated, then every outbound is instantiated. A generation that
// cannot be built is therefore never installed, which is what makes a bad
// reload leave the running configuration untouched rather than half-applied.
func newState(cfg *ir.Config, deps registry.Deps) (*state, error) {
	rt, err := router.New(cfg)
	if err != nil {
		return nil, err
	}

	st := &state{
		cfg:       cfg,
		router:    rt,
		outbounds: make(map[string]outbound.Outbound, len(cfg.Outbounds)),
		// The engine holds one reference for as long as this generation is
		// installed, so a state can never be retired while it is current.
		refs: 1,
	}

	for _, obCfg := range cfg.Outbounds {
		ob, err := registry.NewOutbound(obCfg, deps)
		if err != nil {
			// Outbounds already built are closed here. Leaving them to the
			// caller would mean every failed reload leaked whatever the
			// successful ones had acquired.
			st.retire(*deps.LoggerOr())
			return nil, err
		}
		st.outbounds[obCfg.Tag] = ob
	}

	return st, nil
}

// Start runs every inbound until ctx is cancelled or one of them fails.
//
// Each inbound gets its own goroutine because Start blocks: a listener that
// stopped accepting would otherwise stop the others, and one protocol's
// failure should not silently take down the rest.
//
// The first failure is returned after every inbound has been closed, so a
// caller always observes a stopped engine rather than a half-stopped one.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return errors.New("engine: Start called on a closed engine")
	}
	if e.started {
		e.mu.Unlock()
		return errors.New("engine: Start called twice")
	}
	e.started = true
	e.mu.Unlock()

	if len(e.inbounds) == 0 {
		// Not an error: a configuration may define only outbounds, for
		// instance when this process is not the one accepting traffic. It is
		// worth a warning because it usually means a missing inbound.
		e.logger.Warn().Msg("engine started with no inbounds; no traffic will be served")
	}

	errCh := make(chan error, len(e.inbounds))
	var wg sync.WaitGroup

	for _, in := range e.inbounds {
		wg.Add(1)
		go func(in inbound.Inbound) {
			defer wg.Done()
			if err := in.Start(ctx); err != nil {
				errCh <- err
			}
		}(in)
	}

	// Wait for either a failure or cancellation.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	var firstErr error
	select {
	case firstErr = <-errCh:
		e.logger.Error().Err(firstErr).Msg("inbound failed")
	case <-ctx.Done():
	case <-done:
	}

	// Every inbound is closed and waited on, whichever path led here, so that
	// Start does not return while a listener is still running.
	e.mu.Lock()
	already := e.closed
	e.mu.Unlock()
	if !already {
		if err := e.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	select {
	case <-done:
	case <-time.After(e.opts.ShutdownTimeout):
		e.logger.Warn().Dur("timeout", e.opts.ShutdownTimeout).
			Msg("inbounds did not stop within the shutdown timeout")
	}

	if firstErr != nil && !errors.Is(firstErr, context.Canceled) {
		return firstErr
	}
	return nil
}

// Handler is embedded so the engine can serve an inbound's connections.
//
// Handle is the method the HTTP inbound calls; see handle.go.
var _ = (*Engine)(nil)

// Close stops every inbound and releases every outbound.
//
// It is safe to call more than once and safe to call on an engine that never
// started, because it runs on a shutdown path that cannot know how far startup
// got.
func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	inbounds := e.inbounds
	e.mu.Unlock()

	var firstErr error
	for _, in := range inbounds {
		if err := in.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// Releasing the engine's own reference retires the installed generation if
	// no connection is still using it. A generation that is still in use is
	// closed by whichever connection releases it last, so shutdown neither
	// waits indefinitely for in-flight traffic nor closes outbounds that are
	// still carrying it.
	if st := e.slot.release(e.slot.snapshot()); st != nil {
		st.retire(*e.logger)
	}
	return firstErr
}

// Route returns the outbound tag that should serve a connection.
//
// The engine implements the registry's routing capability so that a protocol
// factory can be given one without importing this package.
func (e *Engine) Route(meta metadata.Metadata) (string, error) {
	return e.slot.snapshot().router.Route(meta)
}

// Inbounds returns the built inbounds, for diagnostics and tests.
func (e *Engine) Inbounds() []inbound.Inbound { return e.inbounds }

// Outbound returns the outbound registered under a tag.
//
// It exists for diagnostics. The connection path resolves outbounds through
// the same map, so a tag that is absent here is absent there too.
func (e *Engine) Outbound(tag string) (outbound.Outbound, bool) {
	ob, ok := e.slot.snapshot().outbounds[tag]
	return ob, ok
}

// Config returns the configuration currently in effect.
//
// It is the installed generation, not the one last passed to Reload, so a
// caller reading it after a failed reload sees what is actually running.
func (e *Engine) Config() *ir.Config {
	return e.slot.snapshot().cfg
}

// Reload replaces the configuration without dropping traffic.
//
// The sequence is: build a complete new generation, refuse if the inbounds
// would have to change, then swap it in. Every failure happens before the
// swap, so a rejected reload leaves the running configuration exactly as it
// was — a bad edit never takes the service down, which is the whole reason the
// configuration file can be treated as the source of truth.
//
// Existing connections keep using the generation they started on and are not
// interrupted: a reload is a configuration change, not a reason to cut off
// someone's download. Their outbounds stay open until the last of them
// finishes, at which point the retired generation is closed.
func (e *Engine) Reload(cfg *ir.Config) error {
	if cfg == nil {
		return errors.New("engine: nil configuration")
	}

	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return errors.New("engine: Reload called on a closed engine")
	}

	// Inbounds cannot be replaced in place. A listener that is rebound loses
	// its port, and anything queued on the old one is dropped, so a reload
	// that changed the listening set would fail for reasons the operator
	// cannot see. Refusing is honest and the fix is a restart.
	if err := e.checkInboundsUnchanged(cfg); err != nil {
		return err
	}

	deps := registry.Deps{Logger: e.logger, Router: e, Handler: e}

	// Built before the swap, so a configuration naming a missing protocol or
	// an unsupported rule type fails here with the old one still serving.
	next, err := newState(cfg, deps)
	if err != nil {
		return err
	}

	retired := e.slot.swap(next)
	if retired != nil {
		// Nothing was using the previous generation, which is the common case
		// for a reload between connections.
		retired.retire(*e.logger)
	}

	e.logger.Info().
		Int("outbounds", len(cfg.Outbounds)).
		Int("rules", next.router.Len()).
		Bool("draining", retired == nil).
		Msg("configuration reloaded")

	return nil
}

// checkInboundsUnchanged refuses a reload whose inbound list differs from the
// running one.
func (e *Engine) checkInboundsUnchanged(cfg *ir.Config) error {
	current := e.slot.snapshot().cfg
	if inboundSetsEqual(current.Inbounds, cfg.Inbounds) {
		return nil
	}
	return fmt.Errorf("%w: the running engine listens on %s but the new configuration asks for %s",
		ErrInboundChangeRequiresRestart, describeInbounds(current.Inbounds), describeInbounds(cfg.Inbounds))
}

// inboundSetsEqual reports whether two inbound lists describe the same
// listeners.
//
// The comparison is over the fields that determine what is listened on, not
// over the whole struct. Option maps are deliberately excluded: a changed
// header timeout does not require rebinding, and treating it as one would
// force a restart for something the running listener could adopt. Fields are
// compared positionally, because the order of the list is what the loader and
// merger preserve and a reordering is worth reporting rather than silently
// matching.
func inboundSetsEqual(a, b []ir.Inbound) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Tag != b[i].Tag ||
			a[i].Type != b[i].Type ||
			a[i].Listen != b[i].Listen ||
			a[i].Port != b[i].Port {
			return false
		}
	}
	return true
}

// describeInbounds renders a listener list for an error message.
func describeInbounds(list []ir.Inbound) string {
	if len(list) == 0 {
		return "nothing"
	}
	out := make([]string, 0, len(list))
	for _, in := range list {
		listen := in.Listen
		if listen == "" {
			listen = "0.0.0.0"
		}
		out = append(out, fmt.Sprintf("%s (%s on %s:%d)", in.Tag, in.Type, listen, in.Port))
	}
	return strings.Join(out, ", ")
}

// dialOutbound resolves a tag and opens a connection through that outbound.
//
// It is the single place where a tag becomes a live connection, so the error
// for an unknown tag and the error for a failed dial are both produced once.
func (e *Engine) dialOutbound(ctx context.Context, st *state, tag string, meta metadata.Metadata) (outbound.Outbound, net.Conn, error) {
	ob, ok := st.outbounds[tag]
	if !ok {
		return nil, nil, fmt.Errorf("engine: rule selected outbound %q, which is not defined", tag)
	}

	conn, err := ob.Dial(ctx, meta)
	if err != nil {
		return ob, nil, err
	}
	return ob, conn, nil
}

// pipe relays between two connections using the standard relay.
func (e *Engine) pipe(ctx context.Context, a, b net.Conn) error {
	return relay.Pipe(ctx, a, b)
}
