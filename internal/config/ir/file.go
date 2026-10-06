// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package ir

import "time"

// File is a single configuration document that has been read into memory.
//
// It is the unit of provenance: every value in the compiled IR can be traced
// back to exactly one File plus a line number.
type File struct {
	// Ref locates the file on disk. Path is always absolute and
	// symlink-resolved, because that normalized form is the key used for
	// both cycle detection and de-duplication (AGENTS.md 21.3).
	Ref SourceRef `json:"ref"`

	// Content is the raw bytes as read from disk, unmodified.
	//
	// The original bytes are retained, rather than only the decoded values,
	// for three reasons: error messages can quote the offending line, the
	// field-level writer can rewrite a single key without reserializing the
	// whole document (AGENTS.md 21.6 requires preserving user comments and
	// ordering), and content hashes for the reload fingerprint are computed
	// over exactly what the user wrote.
	Content []byte `json:"-"`

	// SHA256 is the hex-encoded hash of Content.
	//
	// The loader computes it once so that the hot-reload fingerprint is a
	// cheap comparison over sorted (path, hash) pairs, without rereading or
	// rehashing the files on every poll (AGENTS.md 21.7 step 1).
	SHA256 string `json:"sha256,omitempty"`

	// Size is len(Content), cached so that diagnostics and size-cap checks
	// do not have to reach back into Content.
	Size int64 `json:"size,omitempty"`

	// ModTime is the file's modification time as reported by the filesystem.
	// It is a hint for change detection only; correctness relies on SHA256,
	// because mtime granularity and clock skew make it unreliable on its own.
	ModTime time.Time `json:"mod_time,omitempty"`

	// Optional is true when the file was pulled in by an include marked
	// optional = true. A missing optional file is skipped silently; a missing
	// required file is an error.
	Optional bool `json:"optional,omitempty"`

	// Depth is the include depth at which this file was reached, starting at
	// 0 for the main config. The loader enforces the 32-level limit against
	// it (AGENTS.md 21.3).
	Depth int `json:"depth,omitempty"`
}

// FileSet is the complete, ordered set of documents that feed one Compile.
//
// It is the boundary between the two halves of configuration handling:
//
//	Load    (has I/O)  : scan dirs, expand includes and globs, read bytes,
//	                     record provenance  ->  FileSet
//	Compile (pure)     : FileSet -> merge -> validate -> Config
//
// Because a FileSet fully describes the inputs, Compile is a pure function
// and can be tested by constructing a FileSet in memory, with no filesystem
// involved (AGENTS.md 5 and 16, pitfall 9).
type FileSet struct {
	// Files are the documents to compile, in merge order: lowest priority
	// first, so applying them in slice order reproduces the result.
	//
	// The loader sorts them by the five-level precedence rule (AGENTS.md
	// 21.1) and, within one directory level, by byte-wise filename order.
	// Byte-wise rather than locale-aware is deliberate, so that ordering is
	// identical on every machine.
	Files []File `json:"files,omitempty"`

	// Sources records value-level provenance for the whole set — which file
	// and line each field came from, and what it shadowed.
	//
	// It is populated during Load, not during Compile. Keeping it on the
	// FileSet rather than on the compiled Config means the IR itself stays
	// free of diagnostic metadata, so two FileSets that differ only in
	// comments or ordering still produce deeply equal IRs.
	Sources SourceInfo `json:"sources,omitempty"`

	// Root is the absolute path of the configuration directory the set was
	// loaded from, typically ~/.li/liproxy. Relative include paths resolve
	// against the directory of the *including* file, not against Root, so
	// this is used for display and for locating state/ and backups/.
	Root string `json:"root,omitempty"`

	// LoadedAt is when the set was assembled. It is informational only and
	// deliberately excluded from the reload fingerprint, which must stay a
	// function of file contents alone (AGENTS.md 21.7).
	LoadedAt time.Time `json:"loaded_at,omitempty"`
}
