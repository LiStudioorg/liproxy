// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package compile

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/config/merge"
)

// ErrUnknownKey reports a key that does not exist in the IR structure.
//
// The typical cause is a typo, so the error carries the closest real field
// when one is near enough to be the likely intent.
var ErrUnknownKey = fmt.Errorf("unknown key")

// openFields lists the paths whose keys are intentionally unconstrained.
//
// These three fields are map[string]any placeholders in the IR: their contents
// belong to whichever protocol or rule type owns them, so this package cannot
// know the valid key set. Rejecting keys there would flag correct
// configuration. The exemption is a path prefix, so everything below these
// fields is exempt too.
//
// When protocol field sets are defined, this list is the single place that has
// to change.
var openFields = []string{
	"inbounds[].options",
	"outbounds[].options",
	"rules[].params",
}

// nonIRKeys are document keys the loader consumes but that remain in the
// merged tree because the merge operates on the file body as written.
//
// They are not IR fields and never will be: include and exclude describe how
// files are assembled, which happens before compilation. Reporting them as
// unknown keys would reject every configuration that uses includes.
var nonIRKeys = map[string]bool{
	"include": true,
	"exclude": true,
}

// checkUnknownKeys walks the merged tree and rejects keys absent from the IR.
//
// The walk is driven by the IR's reflect metadata rather than by a hand-written
// list of valid keys. A hand-written list is the kind of thing that silently
// goes stale the first time a field is added, and a stale allow-list produces
// false rejections on correct configuration — worse than not checking at all.
func checkUnknownKeys(doc *merge.Doc) error {
	return walkKnown(doc, reflect.TypeOf(ir.Config{}), "", doc.Body, nil)
}

// walkKnown descends the tree in parallel with the IR type.
func walkKnown(doc *merge.Doc, t reflect.Type, path string, node map[string]any, intos []int) error {
	known := irFields(t)

	// Keys are visited in sorted order so that a file with several typos
	// reports the same first one on every run.
	for _, key := range sortedKeys(node) {
		if nonIRKeys[key] && path == "" {
			// Only at the document root: a table may legitimately have its own
			// "include" field, and that is the misfiled-directive case the
			// loader already warns about.
			continue
		}
		if _, ok := known[key]; ok {
			fieldPath := pathJoinPath(path, key)
			if isOpenField(fieldPath) {
				continue
			}
			ft := known[key]
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}

			switch child := node[key].(type) {
			case map[string]any:
				if ft.Kind() != reflect.Struct {
					continue
				}
				if err := walkKnown(doc, ft, fieldPath, child, intos); err != nil {
					return err
				}

			case []any:
				// A list of tables is the normal shape for inbounds,
				// outbounds and rules, so the element type is what must be
				// checked. Skipping lists here is why a typo inside an
				// [[inbounds]] block used to go unreported.
				if ft.Kind() != reflect.Slice {
					continue
				}
				elem := ft.Elem()
				if elem.Kind() == reflect.Pointer {
					elem = elem.Elem()
				}
				if elem.Kind() != reflect.Struct {
					continue
				}
				for i, item := range child {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					elemPath := fmt.Sprintf("%s[%d]", fieldPath, i)
					if err := walkKnown(doc, elem, elemPath, m, intos); err != nil {
						return err
					}
				}
			}
			continue
		}

		return unknownKeyError(doc, path, key, known)
	}
	return nil
}

// unknownKeyError builds a positioned error naming the key and its likely fix.
func unknownKeyError(doc *merge.Doc, parent, key string, known map[string]reflect.Type) error {
	path := pathJoinPath(parent, key)

	ref := doc.OriginFor(path).Source
	if ref.Path == "" && parent != "" {
		ref = doc.OriginFor(parent).Source
	}

	names := make([]string, 0, len(known))
	for k := range known {
		names = append(names, k)
	}
	sort.Strings(names)

	// %w must appear in the fmt.Errorf call itself, so the format string is
	// built first and then wrapped.
	detail := path
	if suggestion, ok := closestKey(key, names); ok {
		detail += fmt.Sprintf("; did you mean %q?", suggestion)
	} else if len(names) > 0 {
		detail += fmt.Sprintf("; valid keys here are %s", strings.Join(names, ", "))
	}

	return errorAt("compile", ref, fmt.Errorf("%w: %s", ErrUnknownKey, detail))
}

// irFields maps a struct type's TOML/YAML key names to their field types.
//
// The name comes from the json tag, which is the snake_case spelling the IR
// uses throughout and therefore the spelling the conversion reads. Deriving
// it from the field name instead would produce "sessionttl" where the user
// writes "session_ttl", and the checker would reject correct configuration.
//
// A field without a tag is keyed by its lowercased name, which is the fallback
// the conversion uses for fields that have no tag.
func irFields(t reflect.Type) map[string]reflect.Type {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	out := make(map[string]reflect.Type, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := configKeyOf(f)
		if name == "" {
			continue
		}

		// Embedded structs contribute their fields at this level, which is
		// how the IR spells shared blocks such as Dial and Transport.
		if f.Anonymous {
			for k, v := range irFields(f.Type) {
				out[k] = v
			}
			continue
		}
		out[name] = f.Type
	}
	return out
}

// configKeyOf returns the configuration key for a struct field.
//
// It is the json tag name when present, since that is the snake_case key the
// conversion reads and the user writes. Fields explicitly excluded with "-"
// are skipped, and a field with no tag falls back to its lowercased name.
func configKeyOf(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return ""
	}
	if tag != "" {
		if i := strings.IndexByte(tag, ','); i >= 0 {
			tag = tag[:i]
		}
		if tag != "" {
			return tag
		}
	}
	return strings.ToLower(f.Name)
}

// parentField normalizes a field path so that every element of a list shares
// one spelling.
//
// "inbounds[2].options" and "inbounds[0].options" both become
// "inbounds[].options", which is the form the openFields entries are written
// in. Rewriting the index to an empty pair rather than deleting the brackets
// keeps the result a valid path that can still be prefix-matched, and it means
// the literal "inbounds[].options" in openFields normalizes to itself.
func parentField(path string) string {
	var b strings.Builder
	b.Grow(len(path))
	for i := 0; i < len(path); i++ {
		if path[i] != '[' {
			b.WriteByte(path[i])
			continue
		}
		b.WriteString("[]")
		for i < len(path) && path[i] != ']' {
			i++
		}
	}
	return b.String()
}

// isOpenField reports whether a path sits inside a deliberately open map.
//
// List indices are normalized away before matching, so a single entry such as
// "inbounds[].options" covers inbounds[0].options and inbounds[7].options
// alike. Without that normalization the exemption would only ever match the
// literal index written in the table, and every real configuration would be
// rejected.
func isOpenField(path string) bool {
	normalized := parentField(path)
	for _, open := range openFields {
		if normalized == open || strings.HasPrefix(normalized, open+".") {
			return true
		}
	}
	return false
}

// closestKey returns the known key nearest to want, when one is within the
// edit-distance threshold.
//
// A threshold of 2 catches the realistic typos — a transposition, one wrong
// character, one missing character — without proposing an unrelated field for
// a name that is simply not meant to be there.
func closestKey(want string, known []string) (string, bool) {
	const threshold = 2

	best := ""
	bestDist := threshold + 1
	for _, k := range known {
		d := editDistance(want, k, threshold)
		if d < bestDist {
			best, bestDist = k, d
		}
	}
	if bestDist > threshold {
		return "", false
	}
	return best, true
}

// editDistance is the Levenshtein distance between a and b, abandoned as soon
// as it is known to exceed limit.
//
// The early exit matters because this runs once per unknown key over the whole
// field list; a full table would still be correct but would do needless work
// on a list that is mostly irrelevant.
func editDistance(a, b string, limit int) int {
	la, lb := len(a), len(b)
	if la-lb > limit || lb-la > limit {
		return limit + 1
	}

	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr[0] = i
		rowMin := curr[0]
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
			if curr[j] < rowMin {
				rowMin = curr[j]
			}
		}
		if rowMin > limit {
			return limit + 1
		}
		copy(prev, curr)
	}
	return prev[lb]
}

// min3 returns the smallest of three integers.
func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// pathJoinPath joins a parent and child field path, keeping list indices in
// their bracketed form so that isOpenField can recognize them.
func pathJoinPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}
