// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package outbounderr defines sentinel errors an outbound can return, so that
// the engine can classify a failure without importing the outbound that
// produced it.
//
// The alternative — each outbound declaring its own sentinel — cannot work
// while the engine is forbidden from importing concrete protocols (AGENTS.md
// 16, pitfall 2): the engine would have no way to compare against a value it
// cannot name. Putting the shared vocabulary in a leaf package that both sides
// import keeps the dependency graph pointing one way and still lets the engine
// ask "was this deliberate?".
package outbounderr

import "errors"

// ErrBlocked reports that an outbound refused a connection by policy.
//
// An outbound wraps this when the refusal is intended by configuration rather
// than a failure. The distinction matters for logging and metrics: blocking an
// ad domain is the rule working, and reporting it at the same severity as a
// genuine dial failure buries the failures that matter.
var ErrBlocked = errors.New("connection blocked by rule")

// ErrNoDestination reports that a connection carried no usable destination.
//
// It is separate from a dial failure because the cause and the fix differ: a
// dial failure is a network or upstream problem, while this is the inbound and
// the outbound disagreeing about what the connection was for.
var ErrNoDestination = errors.New("no destination in connection metadata")
