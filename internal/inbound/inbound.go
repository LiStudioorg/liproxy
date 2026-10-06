// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package inbound

import "context"

// Inbound is a listener that accepts connections and hands them to the engine.
//
// The engine never calls Accept itself. An inbound owns its own accept loop,
// because the shape of that loop differs by protocol: a plain TCP listener
// accepts streams, while a UDP-based protocol assembles datagrams into flows
// before it has anything resembling a connection. Pushing Accept into the
// interface would force every protocol into the TCP shape.
type Inbound interface {
	// Start begins accepting connections and blocks until ctx is cancelled or
	// the listener fails.
	//
	// It is called once, from a goroutine the engine owns. A nil return means
	// the listener stopped because ctx was cancelled; any other return is a
	// failure worth reporting, since a listener that stops on its own leaves
	// traffic unserved while the process still looks healthy.
	//
	// Implementations must not close themselves when ctx is cancelled;
	// Close is called separately so that the engine controls shutdown order.
	Start(ctx context.Context) error

	// Close stops the listener and releases its resources.
	//
	// It must be safe to call more than once and safe to call on an inbound
	// that never started, because the engine calls it from a shutdown path
	// that runs whether or not startup completed.
	Close() error
}
