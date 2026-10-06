// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package ir defines LiProxy's Intermediate Representation and the
// provenance types that explain where each compiled value came from.
//
// Three invariants hold for everything in this package:
//
//  1. No I/O. Reading files, expanding globs and fetching providers all
//     happen during Load, before a FileSet exists. Compile receives a
//     FileSet and touches nothing else, so identical input always yields an
//     identical Config (AGENTS.md 5 and 16, pitfall 9).
//  2. No behaviour. These are data definitions only — no methods beyond what
//     the standard library needs, no merging, no validation. Both live in
//     the parent config package so that the TOML and YAML paths cannot
//     diverge (AGENTS.md 5, "dual-format isomorphism").
//  3. Format-neutral. Nothing here mentions TOML or YAML except Format,
//     which records syntax for diagnostics. The engine reads Config and is
//     never told which syntax produced it.
package ir
