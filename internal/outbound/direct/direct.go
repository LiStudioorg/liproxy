// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package direct

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/metadata"
	"github.com/LiStudioorg/liproxy/internal/outbound"
	"github.com/LiStudioorg/liproxy/internal/outbound/outbounderr"
	"github.com/LiStudioorg/liproxy/internal/registry"
)

// defaultTimeout bounds a dial that the configuration does not bound.
//
// A proxy that hangs on an unreachable destination is indistinguishable from
// one that is down, so there is always a finite timeout even when the user has
// not configured one.
const defaultTimeout = 10 * time.Second

// Outbound connects to the destination through the host's own network stack.
type Outbound struct {
	tag     string
	timeout time.Duration

	// dialer is stored so that a test can substitute one. It is set in New
	// and never mutated afterwards, which is what makes concurrent Dial calls
	// safe without a lock.
	dialer net.Dialer
}

// New builds a direct outbound.
func New(cfg ir.Outbound, _ registry.Deps) (outbound.Outbound, error) {
	timeout := defaultTimeout
	if cfg.Dial.Timeout > 0 {
		timeout = cfg.Dial.Timeout
	}

	return &Outbound{
		tag:     cfg.Tag,
		timeout: timeout,
		dialer:  net.Dialer{Timeout: timeout},
	}, nil
}

// Dial connects to the destination named by meta.
//
// The target comes from meta.Destination(), which prefers a sniffed domain
// over a resolved address. Dialing the name rather than the address is what
// preserves the routing and TLS behaviour the client intended, and it is the
// reason this function does not simply dial DestAddr.
func (o *Outbound) Dial(ctx context.Context, meta metadata.Metadata) (net.Conn, error) {
	target := meta.Destination()
	if target == "" {
		return nil, fmt.Errorf("direct %q: %w", o.tag, outbounderr.ErrNoDestination)
	}

	network := meta.Network
	if network == "" {
		// An inbound that did not set the network means TCP: it is the only
		// transport every protocol has.
		network = "tcp"
	}

	conn, err := o.dialer.DialContext(ctx, network, target)
	if err != nil {
		return nil, fmt.Errorf("direct %q: dial %s: %w", o.tag, target, err)
	}
	return conn, nil
}

// Close releases nothing; each connection is owned by its caller.
func (o *Outbound) Close() error { return nil }

// factory registers the direct outbound.
type factory struct{}

func (factory) Type() string { return "direct" }

func (factory) New(cfg ir.Outbound, deps registry.Deps) (outbound.Outbound, error) {
	return New(cfg, deps)
}

func init() {
	registry.RegisterOutbound(factory{})
}
