// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package merge

import (
	"errors"
	"fmt"
	"strings"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
)

// Sentinel errors returned by this package. Callers match them with
// errors.Is rather than by inspecting message text (AGENTS.md 6).
var (
	// ErrConflict reports two sources defining the same unique tag, where the
	// second is an error rather than an override. AGENTS.md 21.4 makes this a
	// hard failure for outbound and provider tags specifically, because
	// silently discarding one of two same-named nodes would leave a rule
	// pointing at a node the user did not intend.
	ErrConflict = errors.New("config: conflicting definition")

	// ErrTypeMismatch reports two documents giving the same key incompatible
	// shapes, such as a table in one file and an array in another. Merging
	// those has no sensible meaning, so it is reported rather than resolved
	// by precedence: precedence answers "which value wins", not "which of
	// these two unrelated things did you mean".
	ErrTypeMismatch = errors.New("config: type mismatch")

	// ErrBadPosition reports an unusable position directive on a list entry.
	ErrBadPosition = errors.New("config: invalid position")
)

// Error is a positioned error from the merge stage.
//
// It carries the offending file and line so that the message a user sees can
// point at the line to edit, which is the contract for every configuration
// error in this project (AGENTS.md 21.5).
type Error struct {
	// Op names the stage that failed: "parse", "merge" or "convert".
	Op string

	// Ref locates the value the error is about.
	Ref ir.SourceRef

	// Err is the underlying cause, exposed to errors.Is/As via Unwrap.
	Err error

	// Related lists other sources that participated in the failure, such as
	// the first definition of a duplicated tag. It is what turns "duplicate
	// tag" into an actionable message, because the user needs both lines.
	Related []ir.SourceRef
}

// Error renders the error with its position and any related sources.
func (e *Error) Error() string {
	var b strings.Builder
	if e.Op != "" {
		b.WriteString(e.Op)
		b.WriteString(": ")
	}
	b.WriteString(formatRef(e.Ref))
	b.WriteString(": ")
	b.WriteString(e.Err.Error())

	for _, rel := range e.Related {
		b.WriteString("\n  --> also defined at ")
		b.WriteString(formatRef(rel))
	}
	return b.String()
}

// Unwrap exposes the cause so that errors.Is works against the sentinels in
// this package and against any wrapped standard-library error.
func (e *Error) Unwrap() error { return e.Err }

// formatRef renders a source location as "path:line", omitting the line when
// it is unknown so that "file:0" never appears in a message.
func formatRef(ref ir.SourceRef) string {
	switch {
	case ref.Path == "":
		return "(unknown source)"
	case ref.Line <= 0:
		return ref.Path
	default:
		return fmt.Sprintf("%s:%d", ref.Path, ref.Line)
	}
}

// withLine returns a copy of ref with the line overridden when set.
//
// The loader records a file's SourceRef with Line 1 for the document root;
// individual keys get their own line from the parser. This keeps the override
// explicit rather than mutating a shared value.
func withLine(ref ir.SourceRef, line int) ir.SourceRef {
	if line > 0 {
		ref.Line = line
	}
	return ref
}

// errorAt wraps a cause in a positioned Error.
func errorAt(op string, ref ir.SourceRef, err error) error {
	return &Error{Op: op, Ref: ref, Err: err}
}

// conflictAt builds a positioned conflict error naming both sources.
//
// related holds the earlier definitions in load order. The message lists them
// explicitly because the fix depends on which line the user meant to keep.
func conflictAt(op string, ref ir.SourceRef, related []ir.SourceRef, format string, args ...any) error {
	return &Error{
		Op:      op,
		Ref:     ref,
		Err:     fmt.Errorf("%w: %s", ErrConflict, fmt.Sprintf(format, args...)),
		Related: related,
	}
}
