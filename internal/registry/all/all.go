// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package all registers every protocol this build includes.
//
// Importing this package for its side effects is what populates the registry:
// each protocol package registers its factories from its own init, and this is
// the single place that decides which of them exist in a given binary.
// cmd/lipd and cmd/lip both import it, so the two binaries always agree on the
// protocol set.
//
// Nothing under internal/ may import this package except a test. The engine,
// router and relay reach protocols only through registry, and that is what
// keeps the dependency graph acyclic and lets a reduced build actually be
// smaller. If a core package imported this one, every protocol would be linked
// into every binary and build-minimal would silently stop being minimal.
//
// Trimming is done by replacing this file's import list, not by editing the
// core. The intended shape is a build-tagged pair: all.go carries the full
// list, and a minimal.go behind a `//go:build !full` constraint carries the
// reduced one. Only file selection changes between the two builds; no caller
// has to know which protocols it got, because a configuration naming an absent
// protocol fails at engine construction with the type name in the error.
package all

import (
	// The HTTP inbound is the standard proxy listener: CONNECT for tunnelled
	// traffic and absolute-URI requests for plain HTTP.
	_ "github.com/LiStudioorg/liproxy/internal/inbound/http"

	// The block outbound exists so a rule can refuse traffic outright, which is
	// how ad-blocking and kill-switch behaviour are expressed.
	_ "github.com/LiStudioorg/liproxy/internal/outbound/block"

	// The direct outbound dials the destination itself, so it is what every
	// configuration needs at least one of.
	_ "github.com/LiStudioorg/liproxy/internal/outbound/direct"
)
