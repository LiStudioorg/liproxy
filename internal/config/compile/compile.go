// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package compile turns an ir.FileSet into a validated ir.Config.
//
// It is the pure half of the two-phase design: it reads no files, performs no
// network access, and consults no global state, so the same FileSet always
// produces the same Config. A test can therefore build a FileSet in memory and
// exercise the whole conversion without a filesystem (AGENTS.md 5 and 16,
// pitfall 9).
//
// The pipeline is: decode each file into an untyped tree -> merge the trees
// with provenance -> convert the merged tree into the typed IR -> attach
// provenance to the FileSet. The conversion is written once, so a TOML file
// and a YAML file with the same content compile to deeply equal IRs.
//
// Failures are returned, never swallowed. A single unparseable fragment fails
// the whole Compile, and the caller is expected to keep the previously
// compiled IR running rather than clearing it (AGENTS.md 16, pitfall 20).
package compile

import (
	"github.com/LiStudioorg/liproxy/internal/config/ir"
)

// Options controls compilation behaviour.
type Options struct {
	// Strict rejects keys that do not exist in the IR structure.
	//
	// It defaults to true, because a mistyped field is almost always a
	// mistake and silence is the worst response: the user sees a service
	// running with a setting that was never applied. Set Strict to false only
	// when loading configuration written for a different version.
	//
	// Three field groups are exempt because they are intentional open maps in
	// the IR: ir.Inbound.Options, ir.Outbound.Options and ir.Rule.Params.
	// Their key sets belong to the protocol that owns them and are validated
	// once protocol field sets are defined, not here.
	Strict bool
}

// DefaultOptions returns the options used when a caller does not supply any.
func DefaultOptions() Options {
	return Options{Strict: true}
}

// FromFileSet compiles a loaded FileSet into an IR Config.
//
// Files are consumed in the order the loader produced, which is already
// lowest-priority-first. The returned Config is newly allocated and shares
// nothing with the FileSet, so it is safe to store and swap atomically while
// the FileSet is discarded.
func FromFileSet(set *ir.FileSet) (*ir.Config, error) {
	return FromFileSetWith(set, DefaultOptions())
}

// FromFileSetWith compiles a FileSet using explicit options.
func FromFileSetWith(set *ir.FileSet, opts Options) (*ir.Config, error) {
	if set == nil {
		return nil, errorAt("compile", ir.SourceRef{}, ErrNilFileSet)
	}

	merged, err := mergeFiles(set.Files)
	if err != nil {
		return nil, err
	}

	// Unknown keys are checked before conversion so that a typo is reported as
	// a typo rather than as a missing or defaulted value. The check needs the
	// merged tree, since that is where provenance and the final key set live.
	if opts.Strict {
		if err := checkUnknownKeys(merged.doc); err != nil {
			return nil, err
		}
	}

	cfg, err := convert(merged.doc)
	if err != nil {
		return nil, err
	}

	// Provenance is attached to the FileSet, not to the Config, so that two
	// FileSets differing only in comments or ordering still yield deeply
	// equal IRs — which is what the round-trip test in this package relies
	// on, and what keeps the IR free of diagnostic metadata.
	set.Sources = merged.SourceInfo()
	return cfg, nil
}

// FromFiles is a convenience wrapper for the common case of compiling a list
// of files that are already in merge order.
func FromFiles(files []ir.File) (*ir.Config, error) {
	return FromFileSet(&ir.FileSet{Files: files})
}

// FromFilesWith compiles a list of files using explicit options.
func FromFilesWith(files []ir.File, opts Options) (*ir.Config, error) {
	return FromFileSetWith(&ir.FileSet{Files: files}, opts)
}
