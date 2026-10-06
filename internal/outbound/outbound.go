// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package outbound

import (
	"context"
	"net"

	"github.com/LiStudioorg/liproxy/internal/metadata"
)

// Outbound establishes a connection to a destination on behalf of an inbound.
//
// Dial is called once per connection and must be safe for concurrent use: the
// engine shares one instance across every connection that routes to it.
type Outbound interface {
	// Dial opens a connection to the destination named by meta.
	//
	// The returned connection is owned by the caller, which closes it. An
	// outbound must not retain a reference to a connection it has returned,
	// because the caller may close it at any time and a pooled or tracked
	// reference would then be stale.
	//
	// It returns an error rather than a nil connection on failure; the caller
	// has no way to distinguish a nil connection from a successful one that
	// happens to be nil-typed.
	Dial(ctx context.Context, meta metadata.Metadata) (net.Conn, error)

	// Close releases any resources the outbound holds.
	//
	// It must be safe to call more than once and on an outbound that never
	// dialed, since the engine closes every outbound on shutdown.
	Close() error
}
