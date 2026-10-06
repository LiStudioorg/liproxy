// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"sync"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/outbound"
	"github.com/LiStudioorg/liproxy/internal/router"
)

// state is one immutable configuration generation.
//
// A reload never edits a state; it builds a new one and swaps it in. That is
// what makes the switch atomic from a connection's point of view: a connection
// reads the current state once and works entirely within it, so it cannot
// observe a router from one configuration paired with an outbound map from
// another.
//
// A state is immutable after construction, but the engine mutates refs. refs
// is therefore guarded by the engine's mutex, not by the state.
type state struct {
	cfg    *ir.Config
	router *router.Router
	// outbounds is read-only after construction. Connections resolve a tag
	// against it, and each resolution is followed by a Dial on the value it
	// found, so the map itself is never written after New.
	outbounds map[string]outbound.Outbound

	// refs counts the connections currently serving through this state.
	//
	// It starts at 1, held by the engine itself. That initial reference is
	// what makes the accounting work without a race: a retained state can
	// never reach zero while the engine is still able to hand it out, so the
	// transition from "held by the engine" to "held by connections" needs no
	// separate flag and no ordering assumption between the swap and the
	// release.
	refs int
}

// release decrements the reference count and reports whether this was the last
// reference.
//
// It must be called with the engine's mutex held. The check and the decrement
// happen together because two goroutines releasing the final two references
// must not both be told they were last.
func (s *state) release() bool {
	s.refs--
	return s.refs <= 0
}

// acquire records that one more connection is using this state.
//
// It must be called with the engine's mutex held, and it must be called in the
// same critical section that reads the state pointer. Acquiring afterwards
// would be a use-after-free in the accounting sense: the reload could have
// swapped and released between the read and the acquire, dropping the count to
// zero and closing these outbounds under a connection that is about to use
// them.
func (s *state) acquire() {
	s.refs++
}

// Retire closes the outbounds of a state whose last connection has finished.
//
// It is separate from release so the caller can drop the lock before closing.
// Closing may block — an outbound with a drain may wait — and holding the
// engine mutex across it would stall every new connection behind a shutdown.
func (s *state) retire(logger logger) {
	for tag, ob := range s.outbounds {
		if err := ob.Close(); err != nil {
			logger.Warn().Err(err).Str("outbound", tag).Msg("closing retired outbound failed")
		}
	}
}

// stateSlot owns the current generation and the reference accounting for every
// generation the engine has ever built.
//
// Grouping it in one type keeps the invariant that refs is only ever touched
// under mu, which is the part of this that is easy to get wrong and hard to
// notice: a miscounted reference either closes outbounds under live
// connections or never closes them at all.
type stateSlot struct {
	mu      sync.Mutex
	current *state
}

// load returns the current state with a reference held for the caller.
//
// The returned state is guaranteed not to be retired until the caller passes
// it to release. Every path that obtains a state must release it exactly once.
func (s *stateSlot) load() *state {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.current
	st.acquire()
	return st
}

// release drops a reference and, if it was the last, hands back the state so
// the caller can close it outside the lock.
func (s *stateSlot) release(st *state) (retired *state) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.release() {
		return st
	}
	return nil
}

// swap installs a new state and retires the previous one if nothing is using
// it.
//
// The new state is installed before the old one is released, so there is never
// a moment in which the engine has no configuration. A connection arriving
// during a reload gets either the old generation or the new one, both of which
// are complete.
func (s *stateSlot) swap(next *state) (retired *state) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.current
	s.current = next
	if prev.release() {
		return prev
	}
	return nil
}

// snapshot returns the current state without taking a reference.
//
// It is for diagnostics, where observing a generation that is being retired is
// acceptable because nothing will be dialed through it.
func (s *stateSlot) snapshot() *state {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}
