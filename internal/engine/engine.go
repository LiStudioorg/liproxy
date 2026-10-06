// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
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

// Engine is a running proxy service.
type Engine struct {
	cfg    *ir.Config
	logger *zerolog.Logger
	opts   Options

	router *router.Router

	// inbounds and outbounds are built once in New and never mutated, so they
	// can be read from any goroutine without a lock.
	inbounds  []inbound.Inbound
	outbounds map[string]outbound.Outbound

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

	rt, err := router.New(cfg)
	if err != nil {
		return nil, err
	}

	e := &Engine{
		cfg:       cfg,
		logger:    logger,
		opts:      opts,
		router:    rt,
		outbounds: make(map[string]outbound.Outbound, len(cfg.Outbounds)),
	}

	// Deps carries both capabilities: Handler for inbounds, which hand over
	// accepted connections, and Router for outbounds that chain to another
	// outbound. The engine implements both, so the same value serves either.
	deps := registry.Deps{Logger: logger, Router: e, Handler: e}

	names := make([]string, 0, len(cfg.Outbounds))
	for _, obCfg := range cfg.Outbounds {
		ob, err := registry.NewOutbound(obCfg, deps)
		if err != nil {
			return nil, err
		}
		e.outbounds[obCfg.Tag] = ob
		names = append(names, obCfg.Tag)
	}

	for _, inCfg := range cfg.Inbounds {
		in, err := registry.NewInbound(inCfg, deps)
		if err != nil {
			// Already-built outbounds are closed so that a failed
			// construction does not leak whatever they acquired.
			e.closeOutbounds()
			return nil, err
		}
		e.inbounds = append(e.inbounds, in)
	}

	logger.Info().
		Int("inbounds", len(cfg.Inbounds)).
		Int("outbounds", len(cfg.Outbounds)).
		Int("rules", rt.Len()).
		Msg("engine constructed")

	return e, nil
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
	e.closeOutbounds()
	return firstErr
}

// closeOutbounds closes every outbound, reporting the first failure.
func (e *Engine) closeOutbounds() {
	for tag, ob := range e.outbounds {
		if err := ob.Close(); err != nil {
			e.logger.Warn().Err(err).Str("outbound", tag).Msg("closing outbound failed")
		}
	}
}

// Route returns the outbound tag that should serve a connection.
//
// The engine implements the registry's routing capability so that a protocol
// factory can be given one without importing this package.
func (e *Engine) Route(meta metadata.Metadata) (string, error) {
	return e.router.Route(meta)
}

// Inbounds returns the built inbounds, for diagnostics and tests.
func (e *Engine) Inbounds() []inbound.Inbound { return e.inbounds }

// Outbound returns the outbound registered under a tag.
//
// It exists for diagnostics. The connection path resolves outbounds through
// the same map, so a tag that is absent here is absent there too.
func (e *Engine) Outbound(tag string) (outbound.Outbound, bool) {
	ob, ok := e.outbounds[tag]
	return ob, ok
}

// Reload replaces the configuration.
//
// It is a stub. Hot reload needs the old connections to be drained while the
// new configuration is already accepting, and doing it before the connection
// lifecycle is settled would produce a reload that drops traffic in ways that
// look like protocol bugs. Until then it reports that it is not implemented
// rather than pretending to succeed — a silent no-op would leave the operator
// believing a change took effect.
func (e *Engine) Reload(cfg *ir.Config) error {
	return errors.New("engine: reload is not implemented yet")
}

// dialOutbound resolves a tag and opens a connection through that outbound.
//
// It is the single place where a tag becomes a live connection, so the error
// for an unknown tag and the error for a failed dial are both produced once.
func (e *Engine) dialOutbound(ctx context.Context, tag string, meta metadata.Metadata) (outbound.Outbound, net.Conn, error) {
	ob, ok := e.outbounds[tag]
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
