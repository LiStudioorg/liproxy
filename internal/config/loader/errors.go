// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package loader

import (
	"errors"
	"fmt"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
)

// Sentinel errors. Callers match these with errors.Is; the wrapping carries
// the file and line that must reach the user.
var (
	// ErrFormatUnknown reports that a file's syntax could not be decided from
	// its extension. It is never resolved by guessing: a wrong guess would
	// surface later as a syntax error in the wrong parser, which is far
	// harder to diagnose than a refusal to load (AGENTS.md 21.2).
	ErrFormatUnknown = errors.New("config: cannot determine file format")

	// ErrCycle reports that an include chain revisited a file.
	ErrCycle = errors.New("config: include cycle detected")

	// ErrMaxDepth reports that an include chain exceeded the depth limit.
	ErrMaxDepth = errors.New("config: include depth limit exceeded")

	// ErrMaxFiles reports that a wildcard expansion produced more files than
	// the limit allows.
	ErrMaxFiles = errors.New("config: wildcard expansion limit exceeded")

	// ErrRemoteInclude reports an include pointing at a URL. Remote includes
	// are unimplemented by design, not by omission: configuration must load
	// completely with no network available (AGENTS.md 21.2).
	ErrRemoteInclude = errors.New("config: remote include is not supported")

	// ErrMissingFile reports an explicitly named include that does not exist
	// and was not marked optional.
	ErrMissingFile = errors.New("config: included file not found")

	// ErrParse reports that a file's content could not be parsed as its
	// declared format.
	ErrParse = errors.New("config: parse error")
)

// Error is a loading failure annotated with the position that caused it.
//
// Every user-visible loading error carries a position, because the file and
// line are the only diagnostic the user has: the configuration lives in files
// they wrote, and there is no database to interrogate (AGENTS.md 21.5).
//
// Path and Line describe where the problem was *observed*. For an error in an
// included file, Chain records how the loader got there, since "not found" is
// not actionable without knowing which include pulled it in.
type Error struct {
	// Op is the operation that failed, such as "read" or "parse".
	Op string

	// Ref is the position that caused the failure. Line is zero when the
	// error concerns the file as a whole rather than one line.
	Ref ir.SourceRef

	// Err is the underlying cause, exposed for errors.Is / errors.As.
	Err error

	// Chain is the include chain leading to Ref, outermost first. It is empty
	// when Ref is the entry file. It is what turns a bare "file not found"
	// into a path the user can follow back to their own config.
	Chain []ir.SourceRef
}

// Error renders the failure in the multi-line form required by AGENTS.md
// 21.5, with the include chain indented beneath the position.
func (e *Error) Error() string {
	pos := formatRef(e.Ref)
	if len(e.Chain) == 0 {
		return fmt.Sprintf("%s %s: %v", e.Op, pos, e.Err)
	}
	msg := fmt.Sprintf("%s %s: %v", e.Op, pos, e.Err)
	for _, link := range e.Chain {
		msg += fmt.Sprintf("\n  --> included from %s", formatRef(link))
	}
	return msg
}

// Unwrap exposes the cause so that errors.Is works against the sentinels
// above, and errors.As can reach deeper causes such as fs.ErrNotExist.
func (e *Error) Unwrap() error { return e.Err }

// formatRef renders a source position as "path:line", degrading to the bare
// path when the line is unknown.
func formatRef(ref ir.SourceRef) string {
	if ref.Line > 0 {
		return fmt.Sprintf("%s:%d", ref.Path, ref.Line)
	}
	return ref.Path
}

// errorAt builds a positioned error. It exists so that every construction
// site is forced to supply a position: an error without one would violate
// the requirement that failures name their file.
func errorAt(op string, ref ir.SourceRef, cause error, chain []ir.SourceRef) error {
	return &Error{Op: op, Ref: ref, Err: cause, Chain: chain}
}
