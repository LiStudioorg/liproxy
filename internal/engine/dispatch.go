// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"net"
	"time"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/metadata"
	"github.com/LiStudioorg/liproxy/internal/sniffer"
	"github.com/rs/zerolog"
)

// Handler is the interface an inbound calls to hand over an accepted
// connection.
//
// It is satisfied by *Engine. Naming it here documents the single entry point
// that also performs sniffing, so an inbound does not have to know that
// sniffing exists.
type Handler interface {
	Handle(ctx context.Context, client net.Conn, meta metadata.Metadata) error
}

// dispatch handles one connection, sniffing it first when sniffing applies.
//
// This is the entry point an inbound should call rather than Handle directly.
// Sniffing has to happen here, between the inbound and the route decision,
// because the name it recovers is what the route decision needs: an inbound
// that reported only an address would leave every domain rule inert for
// clients that resolve names themselves, which is most of them.
//
// The connection handed to Handle is the sniffer's wrapper, not the original.
// That is not cosmetic — it is what carries the bytes read during sniffing —
// and passing the original would truncate the client's first flight.
func (e *Engine) dispatch(ctx context.Context, client net.Conn, meta metadata.Metadata) error {
	sn := e.sniffer()
	if sn == nil || meta.Domain != "" {
		// No sniffer, or the inbound already knows the name. Sniffing in the
		// second case would consume bytes the inbound has already parsed past
		// and could only produce a name that is already known.
		return e.Handle(ctx, client, meta)
	}

	conn, res := sn.SniffInto(ctx, client)
	if res.Domain != "" {
		meta.Domain = res.Domain
		meta.Protocol = string(res.Protocol)

		// Overriding the destination is what makes the recovered name reach
		// the outbound. Without it the name only affects routing, and the dial
		// still goes to the address the client resolved — which for a proxy
		// means the request is tunnelled to an address the client chose, not
		// the name it asked for.
		if e.snifferOverridesDestination() {
			meta.DestAddr = nil
		}
	}
	return e.Handle(ctx, conn, meta)
}

// HandleAndSniff is the exported form of dispatch, for inbounds in other
// packages.
//
// The engine's own handler is dispatch, but Go cannot have both an exported
// and an unexported method of the same name, and the name Handle is already
// the inbound-facing entry point. Rather than rename the hot path, the
// sniffing entry point is exported under a name that says what it adds.
func (e *Engine) HandleAndSniff(ctx context.Context, client net.Conn, meta metadata.Metadata) error {
	return e.dispatch(ctx, client, meta)
}

// Sniffer returns the sniffer in effect, or nil when sniffing is off.
//
// The value is derived from the installed configuration rather than stored, so
// a reload that turns sniffing on or off takes effect without a separate step
// that could be forgotten.
func (e *Engine) Sniffer() *sniffer.Sniffer {
	return e.sniffer()
}

// sniffer builds a sniffer from the installed configuration.
//
// It returns nil when sniffing is disabled, which is the default. Sniffing
// delays a connection by up to its timeout, and a configuration that uses no
// domain rules gains nothing from paying that cost.
func (e *Engine) sniffer() *sniffer.Sniffer {
	cfg := e.slot.snapshot().cfg
	if cfg == nil || !cfg.Sniffer.Enabled {
		return nil
	}

	opts := sniffer.Options{
		Timeout: time.Duration(cfg.Sniffer.Timeout),
		Logger:  snifferLogger{logger: e.logger},
	}
	return sniffer.New(opts)
}

// snifferOverridesDestination reports whether a sniffed name should replace the
// destination address.
func (e *Engine) snifferOverridesDestination() bool {
	cfg := e.slot.snapshot().cfg
	return cfg != nil && cfg.Sniffer.OverrideDestination
}

// snifferEnabled reports whether sniffing is on for a connection.
//
// An inbound may override the global setting in either direction, so the
// per-inbound value wins when it is set. The override exists because sniffing
// is a tradeoff: a listener carrying traffic that is already domain-addressed
// gains nothing and pays the timeout.
func snifferEnabled(cfg *ir.Config, inboundTag string) bool {
	enabled := cfg.Sniffer.Enabled
	for _, in := range cfg.Inbounds {
		if in.Tag == inboundTag && in.Sniff != nil {
			return *in.Sniff
		}
	}
	return enabled
}

// snifferLogger adapts the engine's logger to the sniffer's narrow interface.
//
// The sniffer takes a one-method interface rather than a zerolog.Logger so its
// hot path carries no logging dependency to pay for. This adapter is the only
// place that knows about both.
type snifferLogger struct {
	logger *zerolog.Logger
}

// Debugf logs one diagnostic message.
func (l snifferLogger) Debugf(format string, args ...any) {
	l.logger.Debug().Msgf(format, args...)
}
