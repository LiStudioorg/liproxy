// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package registry maps protocol type names to the factories that build them.
//
// It exists so that the engine, router and relay can create protocol instances
// without importing any protocol package (AGENTS.md 16, pitfall 2). A protocol
// registers itself from its own init function, and cmd/lipd aggregates those
// registrations with blank imports. The practical consequence is that
// building a smaller binary is a matter of importing fewer protocol packages,
// with no change to the engine.
//
// Registration happens during package initialisation and is not synchronized
// for writes: init functions run before main, so there is no race to guard
// against. Lookups read a map that is never written after startup, which is
// why they need no lock on the per-connection path.
package registry
