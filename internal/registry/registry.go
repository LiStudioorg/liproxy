// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package registry

import (
	"fmt"
	"sort"
	"sync"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/inbound"
	"github.com/LiStudioorg/liproxy/internal/outbound"
)

// InboundFactory builds an inbound from its IR configuration.
type InboundFactory interface {
	// Type is the name this factory registers under. It must match the type
	// string a user writes in configuration.
	Type() string

	// New builds an inbound. It must not start listening: Start does that,
	// once the engine is ready to serve.
	New(cfg ir.Inbound, deps Deps) (inbound.Inbound, error)
}

// OutboundFactory builds an outbound from its IR configuration.
type OutboundFactory interface {
	// Type is the name this factory registers under.
	Type() string

	// New builds an outbound. It may perform validation that does not require
	// network access; anything that touches the network belongs in Dial.
	New(cfg ir.Outbound, deps Deps) (outbound.Outbound, error)
}

// Duplicate registration is a programming error rather than a recoverable
// condition, so Register panics. The alternatives are worse: returning an
// error means every protocol's init must handle one it cannot do anything
// about, and an error return invites callers to ignore it — leaving a
// registration silently absent and the protocol failing later with a
// misleading "not registered" error at startup. A panic during init names the
// guilty protocol and the collision, and it happens before any traffic is
// served. This is the one place AGENTS.md 6 permits a panic: an unrecoverable
// error during initialisation.
//
// The same reasoning applies to a malformed registration, such as an empty
// type name.
var (
	inboundMu sync.RWMutex
	inbounds  = map[string]InboundFactory{}

	outboundMu sync.RWMutex
	outbounds  = map[string]OutboundFactory{}
)

// RegisterInbound adds an inbound factory to the registry.
//
// It is called from a protocol package's init function and is not intended for
// use at any other time.
func RegisterInbound(f InboundFactory) {
	if f == nil {
		panic("registry: RegisterInbound called with a nil factory")
	}
	name := f.Type()
	if name == "" {
		panic("registry: RegisterInbound called with an empty type name")
	}

	inboundMu.Lock()
	defer inboundMu.Unlock()
	if _, dup := inbounds[name]; dup {
		panic(fmt.Sprintf("registry: inbound type %q is already registered", name))
	}
	inbounds[name] = f
}

// RegisterOutbound adds an outbound factory to the registry.
func RegisterOutbound(f OutboundFactory) {
	if f == nil {
		panic("registry: RegisterOutbound called with a nil factory")
	}
	name := f.Type()
	if name == "" {
		panic("registry: RegisterOutbound called with an empty type name")
	}

	outboundMu.Lock()
	defer outboundMu.Unlock()
	if _, dup := outbounds[name]; dup {
		panic(fmt.Sprintf("registry: outbound type %q is already registered", name))
	}
	outbounds[name] = f
}

// NewInbound builds the inbound described by cfg.
//
// A missing registration is reported with the type name and the list of
// registered types. The list is what turns "unknown inbound type vles" from a
// search through the source into an immediately obvious typo.
func NewInbound(cfg ir.Inbound, deps Deps) (inbound.Inbound, error) {
	if cfg.Type == "" {
		return nil, fmt.Errorf("registry: inbound %q has no type", cfg.Tag)
	}

	inboundMu.RLock()
	f, ok := inbounds[cfg.Type]
	inboundMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("%w: inbound type %q (registered: %s)",
			ErrUnknownType, cfg.Type, joinNames(InboundTypes()))
	}

	inst, err := f.New(cfg, deps)
	if err != nil {
		return nil, fmt.Errorf("inbound %q (type %s): %w", cfg.Tag, cfg.Type, err)
	}
	if inst == nil {
		return nil, fmt.Errorf("inbound factory %q returned a nil instance", cfg.Type)
	}
	return inst, nil
}

// NewOutbound builds the outbound described by cfg.
func NewOutbound(cfg ir.Outbound, deps Deps) (outbound.Outbound, error) {
	if cfg.Type == "" {
		return nil, fmt.Errorf("registry: outbound %q has no type", cfg.Tag)
	}

	outboundMu.RLock()
	f, ok := outbounds[cfg.Type]
	outboundMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("%w: outbound type %q (registered: %s)",
			ErrUnknownType, cfg.Type, joinNames(OutboundTypes()))
	}

	inst, err := f.New(cfg, deps)
	if err != nil {
		return nil, fmt.Errorf("outbound %q (type %s): %w", cfg.Tag, cfg.Type, err)
	}
	if inst == nil {
		return nil, fmt.Errorf("outbound factory %q returned a nil instance", cfg.Type)
	}
	return inst, nil
}

// InboundTypes returns the registered inbound type names, sorted.
//
// The result is sorted so that an error message listing them does not vary
// between runs, which would make the message useless in a test expectation.
func InboundTypes() []string {
	inboundMu.RLock()
	defer inboundMu.RUnlock()

	out := make([]string, 0, len(inbounds))
	for name := range inbounds {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// OutboundTypes returns the registered outbound type names, sorted.
func OutboundTypes() []string {
	outboundMu.RLock()
	defer outboundMu.RUnlock()

	out := make([]string, 0, len(outbounds))
	for name := range outbounds {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// joinNames renders a type list for an error message.
func joinNames(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
