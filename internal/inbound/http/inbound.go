// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package http

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/rs/zerolog"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/inbound"
	"github.com/LiStudioorg/liproxy/internal/metadata"
	"github.com/LiStudioorg/liproxy/internal/registry"
)

// Handler serves one proxied connection's request.
//
// The inbound is a listener; deciding where a connection should go is the
// engine's job, and a Router-shaped dependency would make this package depend
// on routing. The callback keeps the dependency pointing one way: the engine
// knows about this inbound, and the inbound knows only that someone can serve
// a request.
type Handler interface {
	// Handle proxies one CONNECT tunnel from client to the destination chosen
	// by the engine.
	//
	// It returns when the tunnel closes. An error means the tunnel never
	// started or ended abnormally; a normal close returns nil.
	Handle(ctx context.Context, client net.Conn, meta metadata.Metadata) error
}

// sniffingHandler is implemented by a handler that recovers a destination name
// from the connection before routing.
//
// It is a separate optional interface rather than part of Handler because
// sniffing is an engine capability, not something an inbound should have to
// implement or even know about. An inbound reports what it parsed; whether the
// stream is examined further is the engine's decision, and it depends on
// configuration the inbound cannot see.
//
// The handler swaps the connection it returns, and that swap is the point:
// sniffing reads from the stream, and the bytes it read are buffered in the
// connection it hands back.
type sniffingHandler interface {
	HandleAndSniff(ctx context.Context, client net.Conn, meta metadata.Metadata) error
}

// serve hands a connection to the handler, sniffing first when it can.
func (in *Inbound) serve(ctx context.Context, client net.Conn, meta metadata.Metadata) error {
	if sh, ok := in.handler.(sniffingHandler); ok {
		return sh.HandleAndSniff(ctx, client, meta)
	}
	return in.handler.Handle(ctx, client, meta)
}

// Inbound is an HTTP proxy listener.
type Inbound struct {
	tag     string
	listen  string
	port    int
	handler Handler

	// logger is resolved once, in New, so the request path never consults
	// Deps or checks for a nil logger.
	logger *zerolog.Logger

	// server is built in New and only started in Start, so that construction
	// performs no IO and a bad configuration fails before anything is bound.
	server *http.Server

	mu       sync.Mutex
	listener net.Listener
	closed   bool
}

// New builds an HTTP inbound from its IR configuration.
//
// The handler comes from Deps: the engine supplies one that routes and relays.
// A missing handler is reported here rather than at the first request, so a
// wiring mistake fails at startup instead of appearing as a proxy that accepts
// connections and then does nothing with them.
func New(cfg ir.Inbound, deps registry.Deps) (inbound.Inbound, error) {
	h, ok := deps.Handler.(Handler)
	if !ok || h == nil {
		return nil, errors.New("http inbound requires a Handler in Deps.Handler")
	}
	// Port 0 is allowed: it asks the kernel for any free port, which is what
	// tests use and what a configuration that does not care about the port
	// number can write. Only negative and out-of-range values are rejected.
	if cfg.Port < 0 || cfg.Port > 65535 {
		return nil, fmt.Errorf("http inbound %q: port %d is out of range", cfg.Tag, cfg.Port)
	}

	listen := cfg.Listen
	if listen == "" {
		listen = "127.0.0.1"
	}

	in := &Inbound{
		tag:     cfg.Tag,
		listen:  listen,
		port:    cfg.Port,
		handler: h,
		logger:  deps.LoggerOr(),
	}

	in.server = &http.Server{
		// The handler is used directly rather than through a ServeMux. Go
		// 1.22 changed ServeMux to treat a CONNECT request's authority as a
		// path, so "CONNECT example.com:443" is matched against the pattern
		// list and answered with 404 before any handler runs. A proxy has no
		// path routes to dispatch on — every request goes to one function —
		// so the mux adds nothing and would silently break CONNECT.
		Handler: http.HandlerFunc(in.serveProxy),
		// ReadHeaderTimeout bounds a client that opens a connection and sends
		// nothing. Without it, idle half-open connections accumulate and each
		// one holds a goroutine and a socket.
		ReadHeaderTimeout: readHeaderTimeout(cfg),
		// No WriteTimeout: a CONNECT tunnel is long-lived by design, and a
		// write deadline would tear down healthy tunnels mid-transfer.
	}

	return in, nil
}

// Start binds the listener and serves until ctx is cancelled.
func (in *Inbound) Start(ctx context.Context) error {
	addr := net.JoinHostPort(in.listen, fmt.Sprint(in.port))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("http inbound %q: listen %s: %w", in.tag, addr, err)
	}

	in.mu.Lock()
	if in.closed {
		in.mu.Unlock()
		_ = ln.Close()
		return nil
	}
	in.listener = ln
	in.mu.Unlock()

	// Shutdown on cancellation, in its own goroutine, because Serve blocks and
	// has to be the thing that waits.
	go func() {
		<-ctx.Done()
		_ = in.Close()
	}()

	err = in.server.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		// Both mean the listener was closed by Close, which is the normal
		// shutdown path rather than a failure.
		return nil
	}
	return err
}

// Close stops accepting connections and waits for in-flight requests.
func (in *Inbound) Close() error {
	in.mu.Lock()
	if in.closed {
		in.mu.Unlock()
		return nil
	}
	in.closed = true
	ln := in.listener
	in.mu.Unlock()

	// Shutdown waits for active requests, but a hijacked CONNECT tunnel is not
	// tracked by the server once hijacked, so this returns promptly even with
	// tunnels open. Those are closed by the engine through the connection
	// registry rather than here.
	err := in.server.Close()
	if ln != nil {
		// Close is idempotent, and doing it here as well covers the case where
		// Serve was never reached.
		_ = ln.Close()
	}
	return err
}

// Addr returns the bound address, or nil before Start.
func (in *Inbound) Addr() net.Addr {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.listener == nil {
		return nil
	}
	return in.listener.Addr()
}

// Tag returns the inbound's configuration tag.
func (in *Inbound) Tag() string { return in.tag }

// log returns the inbound's logger.
//
// It is a method so that the request path reads naturally while still holding
// a non-nil logger resolved once at construction.
func (in *Inbound) log() *zerolog.Logger { return in.logger }

// factory registers the HTTP inbound.
type factory struct{}

func (factory) Type() string { return "http" }

func (factory) New(cfg ir.Inbound, deps registry.Deps) (inbound.Inbound, error) {
	return New(cfg, deps)
}

func init() {
	registry.RegisterInbound(factory{})
}
