// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package ir defines the Intermediate Representation that every LiProxy
// configuration source compiles into.
//
// TOML and YAML are only front-end parsers; both must produce the same
// structures defined here. The engine consumes ir.Config and knows nothing
// about either file format.
//
// This package contains type definitions only. It performs no loading, no
// merging, no validation, and no I/O — those live in the parent config
// package. Keeping the dependency direction this way lets Compile stay a pure
// function (see AGENTS.md sections 5 and 16, pitfall 9).
package ir

// SourceRef identifies the exact place a configuration value came from.
//
// It is the basis for two things that users depend on:
//
//   - error messages that point at "which file, which line" (AGENTS.md 21.5),
//     which is the only diagnostic users have, since they never see a database
//   - conflict reports that must name *both* contributing files when two
//     sources define the same outbound or provider tag (AGENTS.md 21.4)
//
// A zero SourceRef means "not traced" — for example a value produced by the
// built-in defaults rather than read from a file. Callers must treat that as a
// legitimate state, not as an error.
type SourceRef struct {
	// Path is the absolute, symlink-resolved path of the file the value was
	// read from. It is empty for values that came from built-in defaults.
	//
	// Paths are normalized with EvalSymlinks by the loader before being
	// recorded here, so that cycle detection and duplicate-source reporting
	// cannot be defeated by a symlink (AGENTS.md 21.3).
	Path string `json:"path,omitempty"`

	// Line is the 1-based line number the value appeared on. Zero means the
	// line is unknown, which happens for values synthesized by the loader
	// rather than read from a specific line (e.g. a glob expansion result).
	Line int `json:"line,omitempty"`

	// Column is the 1-based column the value started at. Zero means unknown.
	//
	// Columns are best-effort: both BurntSushi/toml and yaml.v3 can report
	// them, but not for every node kind. Nothing in the codebase may *require*
	// a column, only use it to enrich a message when present.
	Column int `json:"column,omitempty"`

	// Format records the syntax the value was written in, which is needed to
	// render a correct excerpt around Line and to decide which parser to
	// re-run when producing a field-level diagnostic.
	Format Format `json:"format,omitempty"`

	// Fragment names the configuration fragment this source belongs to, when
	// the file was contributed by an include or a glob rather than being
	// listed directly. It is empty for files named explicitly.
	//
	// It exists so that "I edited toml.d/010-rules.toml but the new value did
	// not win" can be explained by showing the include edge that pulled the
	// file in.
	Fragment string `json:"fragment,omitempty"`
}

// Format identifies the syntax of a configuration source.
//
// The set is deliberately closed: a file whose format cannot be determined is
// an error, never a guess (AGENTS.md 21.2).
type Format string

const (
	// FormatTOML marks a TOML source (.toml).
	FormatTOML Format = "toml"

	// FormatYAML marks a YAML source (.yaml, .yml).
	FormatYAML Format = "yaml"

	// FormatUnknown marks a source whose format has not been determined yet.
	// It is the zero value and must be treated as an error by any consumer
	// that needs to actually parse the content.
	FormatUnknown Format = ""
)

// Origin describes where a resolved value came from and what it displaced.
//
// The "overridden" list is what turns a confusing bug report ("I changed the
// value but nothing happened") into a one-line answer, because AGENTS.md 21.5
// requires that shadowed values be reported alongside the winning one.
type Origin struct {
	// Source is the location that supplied the value that actually won.
	Source SourceRef `json:"source"`

	// Overridden lists the locations that set the same field earlier in the
	// merge order and were therefore discarded, in load order (lowest
	// priority first). It is nil when the winning value was never contested.
	Overridden []SourceRef `json:"overridden,omitempty"`
}

// SourceInfo is the provenance side-table for one compiled IR.
//
// It maps a field path — the same dotted/indexed syntax used in validation
// errors, such as "inbounds[0].listen" — to the Origin of the value currently
// held at that path.
//
// Design notes:
//
//   - It is a plain map rather than a method, because provenance is data
//     produced by the loader and consumed by `lip config sources`, the Web
//     UI, and the error formatter; none of them should have to re-derive it.
//   - Keys are allocated only for fields that were actually set by a file.
//     A path absent from the map means "came from built-in defaults".
//     This keeps the table proportional to what the user wrote, not to the
//     size of the IR.
//   - Building it is the loader's job. Nothing in this package may populate
//     it, because doing so would require the IO that Compile must not perform.
type SourceInfo struct {
	// Fields maps a field path to that field's provenance.
	Fields map[string]Origin `json:"fields,omitempty"`

	// Files lists every file that contributed to the IR, in load order
	// (lowest priority first, and within one include chain, depth-first).
	//
	// Order matters: it is the merge order, so it explains *why* a value won.
	// Duplicates are already collapsed by the loader, since a file reached
	// through two include paths must only contribute once (AGENTS.md 21.3
	// requires a separate visited-set for exactly this).
	Files []SourceRef `json:"files,omitempty"`
}
