// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"net"

	"github.com/LiStudioorg/liproxy/internal/metadata"
)

// Handle serves one accepted connection.
//
// It is the method the HTTP inbound calls after it has parsed a request and
// knows the destination. The sequence is the one AGENTS.md 2 describes:
// route, dial, relay.
//
// The client connection is closed here, on every path. The inbound hands over
// ownership when it calls Handle, and an inbound that also closed the
// connection would be closing something it no longer owns — which is how a
// double close turns into a confusing error attributed to the wrong side.
func (e *Engine) Handle(ctx context.Context, client net.Conn, meta metadata.Metadata) error {
	defer func() {
		_ = client.Close()
	}()

	// A connection whose context is already done never had a chance to be
	// served; refusing it early avoids dialing for a client that has gone.
	if err := ctx.Err(); err != nil {
		return err
	}

	// The generation is read once, with a reference taken in the same critical
	// section. Everything below — routing, the outbound lookup, the dial and
	// the relay — runs against this one generation, so a reload landing
	// midway cannot route with one configuration and dial with another.
	//
	// Holding the reference for the whole connection is also what keeps a
	// reload from closing the outbounds underneath it: the retired generation
	// is only closed once the last connection using it has finished.
	st := e.slot.load()
	defer func() {
		if retired := e.slot.release(st); retired != nil {
			retired.retire(*e.logger)
		}
	}()

	tag, err := st.router.Route(meta)
	if err != nil {
		return err
	}

	_, remote, err := e.dialOutbound(ctx, st, tag, meta)
	if err != nil {
		// A refusal by policy is not a failure of this process. It is logged
		// at debug so that blocking a rule does not look like a broken proxy,
		// and the client still sees its connection closed.
		if errors.Is(err, ErrBlocked) {
			e.logger.Debug().
				Str("inbound", meta.InboundTag).
				Str("outbound", tag).
				Str("target", meta.Destination()).
				Msg("connection blocked")
			return err
		}

		e.logger.Warn().
			Err(err).
			Str("inbound", meta.InboundTag).
			Str("outbound", tag).
			Str("target", meta.Destination()).
			Msg("dial failed")
		return err
	}
	defer func() {
		_ = remote.Close()
	}()

	e.logger.Debug().
		Str("inbound", meta.InboundTag).
		Str("outbound", tag).
		Str("target", meta.Destination()).
		Str("network", meta.Network).
		Msg("relaying")

	if err := e.pipe(ctx, client, remote); err != nil {
		// A cancelled context is how every tunnel ends during shutdown, so it
		// is not reported as a failure. Reporting it would make a clean stop
		// look like a storm of errors.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
	return nil
}
