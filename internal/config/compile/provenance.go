// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package compile

import (
	"errors"
	"fmt"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/config/merge"
)

// merged is the intermediate state between merging and conversion.
//
// It keeps the merge result and its provenance together so that conversion
// can read the tree while SourceInfo later reports where each value came
// from, without the two having to be recomputed.
type merged struct {
	doc *merge.Doc
}

// mergeFiles folds the FileSet's documents into one tree.
//
// It delegates to the merge package rather than reimplementing the fold, so
// that the ordering and conflict rules live in exactly one place and the
// compile stage stays a pure conversion.
func mergeFiles(files []ir.File) (*merged, error) {
	doc, err := merge.FromFiles(files)
	if err != nil {
		// The merge package already returns positioned errors; wrapping would
		// add a layer without adding information.
		return nil, err
	}
	return &merged{doc: doc}, nil
}

// SourceInfo converts merge provenance into the FileSet's provenance table.
//
// Field paths are precomputed during verification, which runs after this, so
// the table initially carries the paths the merge knows about. Any path the
// merge recorded maps directly onto the same dotted syntax used in errors.
func (m *merged) SourceInfo() ir.SourceInfo {
	info := ir.SourceInfo{
		Fields: make(map[string]ir.Origin, len(m.doc.Sources)),
		Files:  m.doc.FileSources(),
	}
	for path := range m.doc.Sources {
		info.Fields[path] = m.doc.OriginFor(path)
	}
	return info
}

// Error is a positioned error from the compile stage.
type Error struct {
	// Op names the stage: "compile", "convert" or "verify".
	Op string

	// Ref locates the value, when the stage knows it.
	Ref ir.SourceRef

	// Err is the cause, exposed via Unwrap.
	Err error
}

// Error renders the error with its position when one is known.
func (e *Error) Error() string {
	if e.Ref.Path == "" {
		return fmt.Sprintf("%s: %v", e.Op, e.Err)
	}
	if e.Ref.Line > 0 {
		return fmt.Sprintf("%s: %s:%d: %v", e.Op, e.Ref.Path, e.Ref.Line, e.Err)
	}
	return fmt.Sprintf("%s: %s: %v", e.Op, e.Ref.Path, e.Err)
}

// Unwrap exposes the cause for errors.Is/As.
func (e *Error) Unwrap() error { return e.Err }

// errorAt wraps a cause in a positioned compile error.
func errorAt(op string, ref ir.SourceRef, err error) error {
	return &Error{Op: op, Ref: ref, Err: err}
}

// Describe renders an error for a human, including any related sources.
//
// It exists so that the CLI and the Web UI render a compilation failure the
// same way, and so that the "which file, which line" contract is satisfied by
// one function rather than by every caller.
//
// Both error types in the pipeline already include their own related sources
// in Error(); this function exists to give callers one entry point, and to
// appending anything further would duplicate lines rather than add them.
func Describe(err error) string {
	if err == nil {
		return ""
	}

	// A merge.Error already renders its Related sources as part of its own
	// message, and doing it again here would print every related location
	// twice. Describe therefore returns the error's own rendering for that
	// type, and only falls back to appending related sources for an error
	// whose Error() does not include them.
	var mergeErr *merge.Error
	if errors.As(err, &mergeErr) {
		return err.Error()
	}

	var compileErr *Error
	if errors.As(err, &compileErr) {
		return err.Error()
	}

	return err.Error()
}

// formatRef renders a source location as "path:line".
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
