// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package block

import (
	"context"
	"fmt"
	"net"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/metadata"
	"github.com/LiStudioorg/liproxy/internal/outbound"
	"github.com/LiStudioorg/liproxy/internal/outbound/outbounderr"
	"github.com/LiStudioorg/liproxy/internal/registry"
)

// ErrBlocked is re-exported from outbounderr so that callers of this package
// have one name to reach for.
//
// The value is shared rather than declared here: the engine has to recognise a
// deliberate refusal while being forbidden from importing this package, which
// only works if both sides name the same value.
var ErrBlocked = outbounderr.ErrBlocked

// Outbound refuses every connection it is asked to make.
type Outbound struct {
	tag string
}

// New builds a block outbound.
func New(cfg ir.Outbound, _ registry.Deps) (outbound.Outbound, error) {
	return &Outbound{tag: cfg.Tag}, nil
}

// Dial always fails with ErrBlocked.
//
// The destination is included in the message so that the log answers the
// question the user actually has — what was blocked — rather than only that
// something was.
func (o *Outbound) Dial(ctx context.Context, meta metadata.Metadata) (net.Conn, error) {
	return nil, fmt.Errorf("%w: %s", ErrBlocked, meta.Destination())
}

// Close releases nothing, since the block outbound holds no resources.
//
// It returns nil rather than an error so that the engine's shutdown path can
// close every outbound uniformly.
func (o *Outbound) Close() error { return nil }

// factory registers the block outbound.
type factory struct{}

func (factory) Type() string { return "block" }

func (factory) New(cfg ir.Outbound, deps registry.Deps) (outbound.Outbound, error) {
	return New(cfg, deps)
}

func init() {
	registry.RegisterOutbound(factory{})
}
