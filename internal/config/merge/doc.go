// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package merge folds an ordered list of configuration documents into a
// single intermediate representation.
//
// It sits between the two halves of the loading pipeline and belongs to
// neither: it consumes ir.File values, which carry both decoded content and
// provenance, and produces a doc, which is the same tree annotated with the
// source of every value. It performs no IO, does not validate, and never
// produces an ir.Config — that conversion is the compile package's job.
//
// # Why an intermediate representation
//
// TOML and YAML differ in ways that matter before merging but not after. The
// one that drives this design is duplicate-key handling: TOML rejects a
// duplicated table at parse time, while YAML permits a repeated key and keeps
// the last. Resolving that difference inside the merge would mean writing the
// merge twice, once per syntax, which AGENTS.md 5 forbids. Instead both
// parsers decode into an untyped tree — map[string]any, []any, scalars — and
// the shared merge runs on that.
//
// Ordering is the second reason. Merge order is load order, and load order
// determines which of two competing values wins. Merging into the typed IR
// would make list positions meaningful before the position is known.
//
// # Duplicate keys are an error
//
// The tree merge rejects a key defined twice within one document. Both
// parsers are given the chance to accept it first — neither is asked to
// enforce this — so the rule is identical for TOML and YAML, and a user who
// writes the same key twice is told rather than silently getting one of the
// two values. Silently preferring either one is the failure mode AGENTS.md 16
// pitfall 20 is about.
package merge

import (
	"fmt"
	"sort"
	"strings"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
)

// Document is one parsed configuration file, before merging.
type Document struct {
	// Ref locates the file, with Line carrying the position of the document
	// root (line 1) and Format recording the syntax it was written in.
	Ref ir.SourceRef

	// Body is the decoded document: a map with string keys, whose values are
	// maps, slices or scalars. A document that decodes to anything other than
	// a map is rejected by FromFiles, because a configuration file with a
	// scalar at its root has no keys to merge.
	Body map[string]any

	// Content is the document's original bytes. It is retained so that a line
	// number can be recovered for a value whose parser does not report one,
	// which is the case for every TOML key other than a syntax error.
	Content []byte
}

// Doc is a merged configuration tree whose every value knows where it came
// from.
//
// Nodes are shared with the input documents' provenance, not copied: Doc
// records the merge result, and Shadowed records what each winning value
// displaced. Together they answer "why is this value what it is", which is
// what `lip config sources` and the Web UI need (AGENTS.md 21.5).
type Doc struct {
	// Body is the merged tree. Its shape mirrors the input documents, so it
	// contains map[string]any, []any and scalar values.
	Body map[string]any

	// Shadowed records, for each field path, the sources that set that field
	// earlier in the merge order and were therefore discarded. It is keyed by
	// the same dotted path syntax used in validation errors.
	//
	// A path is absent when its value was never contested, which keeps the
	// table proportional to the number of actual conflicts rather than to the
	// size of the document.
	Shadowed map[string][]ir.SourceRef

	// Sources maps a field path to the source of the value currently held
	// there. It is populated for every key contributed by any file; a path
	// absent from the map came from built-in defaults.
	Sources map[string]ir.SourceRef

	// Files lists the documents that were merged, in load order.
	Files []ir.SourceRef

	// entrySources maps a list entry to the source that contributed it,
	// keyed by a content-derived identity.
	//
	// It exists because provenance paths for list entries are index-based and
	// prepending shifts those indices. Rebuilding the paths needs to know where
	// each entry came from without consulting the paths being rewritten.
	//
	// The key is derived from the entry's content plus its position in the
	// result, not from the map value: a Go map is a reference type, but taking
	// its address yields a distinct pointer to a copied header on every range,
	// so pointer identity does not work here.
	entrySources map[string]ir.SourceRef

	// contents holds each document's raw bytes, keyed by path, for the
	// line-recovery search described on lineOf.
	contents map[string][]byte
}

// maxDepth bounds recursion while flattening and comparing documents.
//
// Configuration is human-written and shallow; this exists only so that a
// pathologically nested document reports an error instead of exhausting the
// stack. It is far below the depth at which the Go runtime would actually
// fail, because a 200-level config is a bug regardless of what the runtime
// tolerates.
const maxDepth = 100

// pathJoin appends a key to a field path.
//
// The syntax is dotted for map keys and indexed for list entries, matching
// the paths used in validation errors so that one string addresses a field
// everywhere in the system (AGENTS.md 21.5).
func pathJoin(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}

// pathIndex appends a list index to a field path.
func pathIndex(base string, i int) string {
	return fmt.Sprintf("%s[%d]", base, i)
}

// sortedKeys returns a map's keys in byte-wise order.
//
// Iterating a Go map is deliberately randomized, so any code that walks a
// document must sort first or produce output that differs between runs. That
// would make both error messages and provenance non-deterministic, and a
// non-deterministic field path in an error is worse than no field path.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isMap reports whether a decoded value is a nested table.
func isMap(v any) bool {
	_, ok := v.(map[string]any)
	return ok
}

// isList reports whether a decoded value is an array.
func isList(v any) bool {
	_, ok := v.([]any)
	return ok
}

// kindOf names a value's shape for error messages.
//
// The name is user-facing: "table" and "array" are the words the configuration
// documentation uses, so an error that says "table" is actionable where one
// that says "map[string]interface {}" is not.
func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "table"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case int64, int, uint64, float64:
		return "number"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// displayPath renders a field path for a human, mapping the empty path (the
// document root) to something nameable.
func displayPath(path string) string {
	if path == "" {
		return "(document root)"
	}
	return path
}

// summarizePath shortens a long path for a message, keeping the tail.
//
// Deep paths in generated or imported documents can be arbitrarily long, and
// the tail is where the distinguishing key is. Truncating the head keeps
// messages readable without hiding the part that identifies the field.
func summarizePath(path string) string {
	const maxLen = 120
	if len(path) <= maxLen {
		return path
	}
	return "..." + path[len(path)-maxLen:]
}

// joinBreadcrumb renders the chain of keys leading to a node, for errors that
// need to say where inside a document a problem is.
func joinBreadcrumb(parts []string) string {
	if len(parts) == 0 {
		return "(document root)"
	}
	return strings.Join(parts, ".")
}
