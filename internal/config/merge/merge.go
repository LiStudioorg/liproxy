// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package merge

import (
	"fmt"
	"strings"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
)

// FromFiles merges documents in load order, lowest priority first.
//
// The caller supplies files already ordered by the loader, so this function
// makes no ordering decisions of its own. Applying the documents in slice
// order reproduces the merged result exactly, which is what makes the
// five-level precedence rule in AGENTS.md 21.1 auditable.
//
// It returns the merged tree plus the provenance accumulated along the way.
// Nothing is validated here: unknown keys, missing required fields and
// cross-references are the compile stage's concern.
func FromFiles(files []ir.File) (*Doc, error) {
	docs, err := toDocuments(files)
	if err != nil {
		return nil, err
	}
	return FromDocuments(docs)
}

// FromDocuments merges already-parsed documents.
//
// It exists so that tests, and any future importer, can exercise the merge
// without going through raw bytes. Documents must be in load order.
func FromDocuments(docs []Document) (*Doc, error) {
	d := &Doc{
		Body:         map[string]any{},
		Shadowed:     map[string][]ir.SourceRef{},
		Sources:      map[string]ir.SourceRef{},
		entrySources: map[string]ir.SourceRef{},
		contents:     map[string][]byte{},
	}

	for _, doc := range docs {
		if doc.Body == nil {
			continue
		}
		d.Files = append(d.Files, doc.Ref)
		if doc.Content != nil {
			d.contents[doc.Ref.Path] = doc.Content
		}
		if err := d.mergeTable("", d.Body, doc.Body, doc.Ref, 0); err != nil {
			return nil, err
		}
	}

	if err := d.checkUniqueTags(); err != nil {
		return nil, err
	}
	return d, nil
}

// toDocuments parses each file into a document.
//
// Parsing is done here rather than by the caller so that a syntax error is
// reported with the same positioned-error shape as a merge failure. A file
// that fails to parse is never partially applied: the whole Compile fails, and
// the caller keeps the last good IR (AGENTS.md 16, pitfall 20).
func toDocuments(files []ir.File) ([]Document, error) {
	docs := make([]Document, 0, len(files))
	for _, f := range files {
		body, err := decode(f)
		if err != nil {
			// decode returns a positioned *Error for every failure shape, so
			// the file and line survive to the caller unchanged.
			return nil, err
		}
		docs = append(docs, Document{Ref: f.Ref, Body: body, Content: f.Content})
	}
	return docs, nil
}

// mergeTable merges src into dst at the given field path.
//
// The rules are those of AGENTS.md 21.4, applied uniformly:
//
//   - table + table  -> recurse, so disjoint keys accumulate
//   - array  + array -> append (with per-entry position handling for rules)
//   - scalar + scalar-> src wins, and the displaced source is recorded
//   - anything else  -> ErrTypeMismatch, because precedence answers "which
//     value wins", not "which of two unrelated shapes you meant"
//
// dst is mutated in place; src is only read. That direction matters: the
// accumulating document is the earlier (lower priority) one, and later files
// fold into it.
func (d *Doc) mergeTable(path string, dst, src map[string]any, ref ir.SourceRef, depth int) error {
	if depth > maxDepth {
		return errorAt("merge", ref,
			fmt.Errorf("%w: nesting exceeds %d levels at %s", ErrTypeMismatch, maxDepth, displayPath(path)))
	}

	for _, key := range sortedKeys(src) {
		childPath := pathJoin(path, key)
		srcVal := src[key]

		dstVal, exists := dst[key]
		if !exists {
			// A list is still routed through mergeList even when nothing
			// preceded it, because a position directive has to be honoured
			// and validated regardless of whether the list already existed.
			// Treating "new key" as a plain assignment would silently accept
			// an invalid position in the first file that defines a list.
			if list, isListVal := srcVal.([]any); isListVal {
				merged, shadowed, err := d.mergeList(childPath, nil, list, ref, depth)
				if err != nil {
					return err
				}
				if len(shadowed) > 0 {
					d.Shadowed[childPath] = append(d.Shadowed[childPath], shadowed...)
				}
				dst[key] = merged
				continue
			}
			d.setValue(childPath, srcVal, ref, depth)
			dst[key] = srcVal
			continue
		}

		switch {
		case isMap(dstVal) && isMap(srcVal):
			if err := d.mergeTable(childPath, dstVal.(map[string]any), srcVal.(map[string]any), ref, depth+1); err != nil {
				return err
			}

		case isList(dstVal) && isList(srcVal):
			merged, shadowed, err := d.mergeList(childPath, dstVal.([]any), srcVal.([]any), ref, depth)
			if err != nil {
				return err
			}
			if len(shadowed) > 0 {
				d.Shadowed[childPath] = append(d.Shadowed[childPath], shadowed...)
			}
			dst[key] = merged

		case isMap(dstVal) != isMap(srcVal) || isList(dstVal) != isList(srcVal):
			return errorAt("merge", ref, fmt.Errorf(
				"%w: key %q is a %s here but a %s earlier; "+
					"the two cannot be merged, so one of them must be renamed or moved",
				ErrTypeMismatch, childPath, kindOf(srcVal), kindOf(dstVal)))

		default:
			// Scalar over scalar: the later file wins, and the earlier source
			// is recorded so that "I changed it but nothing happened" can be
			// answered by naming the line that displaced it.
			if prev, ok := d.Sources[childPath]; ok {
				d.Shadowed[childPath] = append(d.Shadowed[childPath], prev)
			}
			d.Sources[childPath] = withLine(ref, ref.Line)
			dst[key] = srcVal
		}
	}
	return nil
}

// setValue records a newly introduced key and its provenance.
func (d *Doc) setValue(path string, val any, ref ir.SourceRef, depth int) {
	d.Sources[path] = withLine(ref, ref.Line)

	switch v := val.(type) {
	case map[string]any:
		for _, key := range sortedKeys(v) {
			d.setValue(pathJoin(path, key), v[key], ref, depth+1)
		}
	case []any:
		for i, item := range v {
			d.setValue(pathIndex(path, i), item, ref, depth+1)
		}
	}
}

// mergeList appends src entries to dst, honouring a per-entry position.
//
// Appending is the default because most fragments contribute independent
// entries: a second file adding rules means "these rules apply too", not
// "replace the first file's rules". Prepending exists for the case where the
// user is adding an exception, which must be evaluated before what is already
// there (AGENTS.md 21.4).
//
// Rules are the only list that carries position, but the implementation does
// not special-case the name: it looks for a "position" key on any table entry.
// That keeps the rule uniform and lets a future list opt in without touching
// this function. A non-table entry can never carry a position, so scalars and
// nested arrays always append.
func (d *Doc) mergeList(path string, dst, src []any, ref ir.SourceRef, depth int) ([]any, []ir.SourceRef, error) {
	if depth > maxDepth {
		return nil, nil, errorAt("merge", ref,
			fmt.Errorf("%w: nesting exceeds %d levels at %s", ErrTypeMismatch, maxDepth, displayPath(path)))
	}

	// Prepends are collected separately and spliced in ahead of the
	// accumulated list, so that several of them keep their written order.
	var prepend []any
	var shadowed []ir.SourceRef

	result := make([]any, 0, len(dst)+len(src))
	result = append(result, dst...)

	for i, item := range src {
		entryPath := pathIndex(path, i)

		m, ok := item.(map[string]any)
		if !ok {
			// A scalar entry has no keys to reindex, so its path can be
			// recorded directly.
			d.Sources[entryPath] = withLine(ref, ref.Line)
			result = append(result, item)
			continue
		}

		pos, err := positionOf(m, ref, entryPath)
		if err != nil {
			return nil, nil, err
		}

		clean := m
		if pos != ir.PositionAppend {
			clean = withoutPosition(m)
		}

		// The source is keyed by the entry's content, so reindexing can
		// recover it after the indices shift. The line is refined from the
		// file's own text, because the decoded tree carries the document line
		// rather than the per-key one for TOML.
		entryRef := withLine(ref, d.lineOf(ref, m))
		d.rememberEntry(clean, entryRef)

		if pos == ir.PositionPrepend {
			prepend = append(prepend, clean)
			continue
		}
		result = append(result, clean)
	}

	// A prepended entry must land ahead of everything already merged. Since
	// prepend holds the entries in the order they were written, inserting them
	// as a block preserves that order: the first written rule ends up first,
	// which is what "prepend, in written order" means to the user.
	if len(prepend) > 0 {
		result = append(prepend, result...)
	}

	// Indices in the provenance paths shift when entries are prepended, so
	// the recorded paths are rebuilt against the final ordering. Reporting
	// "rules[0]" for an entry that ended up at index 3 would send the user to
	// the wrong line.
	d.reindexList(path, result)

	return result, shadowed, nil
}

// positionOf reads and validates the position key of a list entry.
func positionOf(m map[string]any, ref ir.SourceRef, entryPath string) (ir.Position, error) {
	raw, ok := m["position"]
	if !ok {
		return ir.PositionAppend, nil
	}

	s, isString := raw.(string)
	if !isString {
		return "", errorAt("merge", ref, fmt.Errorf(
			"%w: position at %s must be a string, got %s",
			ErrBadPosition, entryPath, kindOf(raw)))
	}

	switch ir.Position(s) {
	case ir.PositionAppend:
		return ir.PositionAppend, nil
	case ir.PositionPrepend:
		return ir.PositionPrepend, nil
	default:
		return "", errorAt("merge", ref, fmt.Errorf(
			"%w: position %q at %s is not recognized; use %q or %q",
			ErrBadPosition, s, entryPath, string(ir.PositionPrepend), "append"))
	}
}

// withoutPosition returns a copy of a list entry with the position key
// removed.
//
// position is a merge directive, not configuration: it tells the merger where
// to put the entry and has no meaning to the engine. Dropping it here keeps it
// from reaching validation, which would otherwise have to know about a key
// that only ever existed to influence ordering.
func withoutPosition(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k == "position" {
			continue
		}
		out[k] = v
	}
	return out
}

// reindexList rewrites provenance paths for list entries after reordering.
//
// Prepending shifts every existing entry's index, so a recorded path such as
// "rules[0]" can end up describing a different entry than the one it was
// recorded for. A stale path sends the user to the wrong line, which is worse
// than no path at all, so the paths are rebuilt against the final ordering.
//
// The source of each entry is recovered from d.entries, a side map keyed by
// element identity. The recorded paths cannot be used for the lookup because
// they are precisely what is being rewritten.
func (d *Doc) reindexList(path string, list []any) {
	for old := range d.Sources {
		if isListChildPath(path, old) {
			delete(d.Sources, old)
		}
	}
	for i, item := range list {
		d.recordEntry(pathIndex(path, i), item)
	}
}

// lineOf refines a source line for one list entry.
//
// The decoded tree carries no per-key position for TOML — BurntSushi/toml
// exposes only parsed values and a key list — so a duplicate-tag report would
// otherwise name the file without a line, which is exactly the case where the
// user needs to be sent to the right place.
//
// The line is recovered by locating the entry's identifying value in the
// file's own text. YAML entries already carry their node line and take the
// early return.
func (d *Doc) lineOf(ref ir.SourceRef, entry map[string]any) int {
	if ref.Line > 1 || ref.Path == "" || ref.Format != ir.FormatTOML {
		// Already positioned (YAML, or a caller that knows the line).
		return ref.Line
	}

	content, ok := d.contents[ref.Path]
	if !ok {
		return ref.Line
	}

	needle := entryAnchor(entry)
	if needle == "" {
		return ref.Line
	}

	for i, line := range strings.Split(string(content), "\n") {
		if strings.Contains(line, needle) {
			return i + 1
		}
	}
	return ref.Line
}

// entryAnchor picks the string that identifies a list entry in source text.
//
// A tag or name is chosen first because it is unique by construction and is
// the value the error message already quotes, so the reported line and the
// reported text agree.
func entryAnchor(entry map[string]any) string {
	for _, key := range []string{"tag", "name", "type"} {
		if v, ok := entry[key].(string); ok && v != "" {
			return v
		}
	}
	for _, key := range sortedKeys(entry) {
		if v, ok := entry[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// rememberEntry associates an entry's content with the source that set it.
func (d *Doc) rememberEntry(entry map[string]any, ref ir.SourceRef) {
	d.entrySources[entryKey(entry)] = ref
}

// entryKey derives a stable key from a list entry's own scalar fields.
//
// The identity key is built from the entry's top-level scalar values rather
// than from the map value itself: Go maps cannot be map keys, and a pointer to
// a ranged map variable is a fresh pointer each iteration, so neither supports
// a lookup. Two structurally identical entries in different lists would
// collide, but they would also carry the same source, so the collision is
// harmless.
func entryKey(entry map[string]any) string {
	var b strings.Builder
	for _, k := range sortedKeys(entry) {
		switch v := entry[k].(type) {
		case string:
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(v)
			b.WriteByte('\x00')
		case bool:
			fmt.Fprintf(&b, "%s=%t\x00", k, v)
		case int64:
			fmt.Fprintf(&b, "%s=%d\x00", k, v)
		case int:
			fmt.Fprintf(&b, "%s=%d\x00", k, v)
		case float64:
			fmt.Fprintf(&b, "%s=%v\x00", k, v)
		}
	}
	return b.String()
}

// isListChildPath reports whether a field path addresses an element of the
// list at base, or something inside such an element.
func isListChildPath(base, fieldPath string) bool {
	if len(fieldPath) <= len(base) || !strings.HasPrefix(fieldPath, base) {
		return false
	}
	return fieldPath[len(base)] == '['
}

// recordEntry records provenance for one list entry and everything inside it.
func (d *Doc) recordEntry(entryPath string, item any) {
	switch v := item.(type) {
	case map[string]any:
		// An entry carries its own source when it passed through mergeList,
		// which also registered it by content. Otherwise the list already
		// existed and the entry arrived through mergeTable, in which case the
		// path still holds the source recorded before the rewrite.
		src, known := d.entrySources[entryKey(v)]
		if !known {
			src, known = d.Sources[entryPath]
		}
		if known {
			d.Sources[entryPath] = src
		}
		for _, key := range sortedKeys(v) {
			if known {
				d.Sources[pathJoin(entryPath, key)] = src
			}
			d.recordEntry(pathJoin(entryPath, key), v[key])
		}
	case []any:
		for i, sub := range v {
			d.recordEntry(pathIndex(entryPath, i), sub)
		}
	}
}
