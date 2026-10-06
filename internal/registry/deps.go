// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package registry

import (
	"net"

	"github.com/rs/zerolog"

	"github.com/LiStudioorg/liproxy/internal/metadata"
)

// Router decides which outbound serves a connection.
//
// It is declared here, as a one-method interface, rather than imported from
// internal/router. The router needs the registry to build outbounds, so a
// direct import in the other direction would be a cycle. Naming the capability
// instead of the package breaks it: the registry does not care who implements
// this, and internal/router does so without being referenced from here.
type Router interface {
	// Route returns the tag of the outbound that should serve the connection.
	Route(meta metadata.Metadata) (string, error)
}

// Deps carries the dependencies a protocol factory may use.
//
// It is passed by value, on construction only, and is never stored past the
// factory returning. That keeps a protocol from holding a reference to the
// engine and reaching back into it later, which would make shutdown ordering
// depend on protocol behaviour.
type Deps struct {
	// Logger is the process logger. A nil value is replaced with a disabled
	// logger so that a protocol can log unconditionally without checking.
	Logger *zerolog.Logger

	// Router resolves an outbound tag for a connection.
	//
	// It is for outbounds that chain to another outbound. An inbound does not
	// need it: an inbound hands its connections to Handler instead, which is
	// what keeps this package free of the routing types an inbound would
	// otherwise have to know about. Implementations must tolerate nil.
	Router Router

	// Handler receives a connection an inbound has accepted and parsed.
	//
	// It is an `any` rather than a named type because the interface it must
	// satisfy differs per inbound protocol: an HTTP proxy hands over a stream
	// and expects a tunnel to be served, while a future UDP-based inbound
	// would hand over datagrams. Naming one interface here would force every
	// protocol into the first protocol's shape.
	//
	// A factory asserts it to the interface it actually needs and reports a
	// mismatch as a construction error, so a wiring mistake fails at startup
	// rather than on the first connection.
	Handler any
}

// LoggerOr returns the configured logger, or a disabled one.
//
// Returning a usable logger rather than nil means protocol code can call
// deps.LoggerOr().Debug() without a nil check, and the disabled logger
// discards the event. This matters on the per-connection path, where a
// nil check per log call is real overhead and an omission is a panic.
func (d Deps) LoggerOr() *zerolog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	disabled := zerolog.Nop()
	return &disabled
}

// ListenFunc is the signature a protocol uses to open its listener.
//
// It is declared here so that all protocols open listeners the same way and a
// future change — address reuse, a socket option, a test double — has one
// place to land instead of one per protocol.
type ListenFunc func(network, address string) (net.Listener, error)
