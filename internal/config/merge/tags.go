// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package merge

import (
	"fmt"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
)

// uniqueTagSpec describes a list whose entries carry a tag that must be unique
// across all documents.
type uniqueTagSpec struct {
	// listKey is the top-level key holding the list.
	listKey string

	// tagKey is the key within each entry that names it.
	tagKey string

	// kind names the entry type in error messages ("outbound", "provider").
	kind string

	// mergeable marks a list whose same-named entries combine instead of
	// conflicting. AGENTS.md 21.4 requires exactly this split: two outbounds
	// with one tag are an error, while two rule sets with one name merge.
	mergeable bool
}

// uniqueTagLists enumerates the lists covered by the duplicate-tag rule.
//
// The split between error and merge is not arbitrary. An outbound tag is a
// routing destination: if two files each define "us-node" with different
// credentials, silently keeping one means a rule sends traffic somewhere the
// user did not choose, and the losing definition leaves no trace. A rule set
// name is a container: two files adding rules to "ads" genuinely means "both
// sets of rules apply", so merging is the intended reading rather than a
// conflict.
var uniqueTagLists = []uniqueTagSpec{
	{listKey: "outbounds", tagKey: "tag", kind: "outbound", mergeable: false},
	{listKey: "providers", tagKey: "name", kind: "provider", mergeable: false},
	{listKey: "inbounds", tagKey: "tag", kind: "inbound", mergeable: false},
	{listKey: "proxy_groups", tagKey: "name", kind: "proxy group", mergeable: true},
	{listKey: "rule_sets", tagKey: "name", kind: "rule set", mergeable: true},
}

// mergeNamedEntries folds same-named entries of a mergeable list together.
//
// Two files that each add rules to the rule set "ads" mean "both sets of rules
// apply", so the entries combine rather than one displacing the other. The
// later definition's own keys win on conflict, matching the general rule; only
// the entry lists are concatenated.
//
// This runs after the tree merge because the tree merge cannot see it: by the
// time mergeList has run, the two entries are separate elements of one flat
// array, and "same name" is a property of their contents, not their positions.
func (d *Doc) mergeNamedEntries(spec uniqueTagSpec) error {
	raw, ok := d.Body[spec.listKey]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}

	// index maps a name to the position of its winning entry in the output.
	index := make(map[string]int, len(list))
	out := make([]any, 0, len(list))
	var shadowedNames []ir.SourceRef

	for i, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			out = append(out, entry)
			continue
		}
		name, ok := m[spec.tagKey].(string)
		if !ok || name == "" {
			out = append(out, entry)
			continue
		}

		entryPath := pathIndex(pathJoin("", spec.listKey), i)
		ref := d.Sources[entryPath]

		prevPos, dup := index[name]
		if !dup {
			index[name] = len(out)
			out = append(out, m)
			continue
		}

		prev, _ := out[prevPos].(map[string]any)
		out[prevPos] = d.combineEntries(prev, m, spec, name)
		if ref.Path != "" {
			shadowedNames = append(shadowedNames, ref)
		}
	}

	if len(shadowedNames) > 0 {
		// Recording the displaced definitions under the list path keeps the
		// information available to `lip config sources` without inventing a
		// per-name path that no other code knows how to address.
		d.Shadowed[spec.listKey] = append(d.Shadowed[spec.listKey], shadowedNames...)
	}

	d.Body[spec.listKey] = out
	return nil
}

// combineEntries merges two same-named entries, concatenating their rule and
// proxy lists and letting the later one win on scalar conflict.
func (d *Doc) combineEntries(prev, next map[string]any, spec uniqueTagSpec, name string) map[string]any {
	combined := make(map[string]any, len(prev)+len(next))
	for k, v := range prev {
		combined[k] = v
	}
	for k, v := range next {
		if pv, exists := combined[k]; exists {
			pl, pIsList := pv.([]any)
			nl, nIsList := v.([]any)
			if pIsList && nIsList {
				joined := make([]any, 0, len(pl)+len(nl))
				joined = append(joined, pl...)
				joined = append(joined, nl...)
				combined[k] = joined
				continue
			}
			if isMap(pv) && isMap(v) {
				// Nested tables (a rule set's own metadata) merge key-wise so
				// that the two definitions do not lose each other's settings.
				nested := make(map[string]any, len(pv.(map[string]any)))
				for kk, vv := range pv.(map[string]any) {
					nested[kk] = vv
				}
				for kk, vv := range v.(map[string]any) {
					nested[kk] = vv
				}
				combined[k] = nested
				continue
			}
		}
		combined[k] = v
	}
	return combined
}

// checkUniqueTags reports same-named entries that must not coexist.
//
// It runs after the whole merge, not per file, because a duplicate can only be
// detected once every document has contributed. That ordering is also what
// lets the message name both sources: by the time the check runs, the
// provenance table holds the line of the winning definition and Shadowed holds
// the lines it displaced.
func (d *Doc) checkUniqueTags() error {
	for _, spec := range uniqueTagLists {
		if spec.mergeable {
			if err := d.mergeNamedEntries(spec); err != nil {
				return err
			}
			continue
		}
		if err := d.checkUniqueTagList(spec); err != nil {
			return err
		}
	}
	return nil
}

// checkUniqueTagList validates one list.
func (d *Doc) checkUniqueTagList(spec uniqueTagSpec) error {
	raw, ok := d.Body[spec.listKey]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		// A non-array here is a shape error that the conversion stage reports
		// with better context than this check could.
		return nil
	}

	// seen maps a tag to the source of its winning definition.
	seen := make(map[string]ir.SourceRef, len(list))

	for i, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		tag, ok := m[spec.tagKey].(string)
		if !ok || tag == "" {
			// A missing or non-string tag is a validation concern: the error
			// "required field tag is empty" is more useful than a duplicate
			// report, and reporting here would preempt it.
			continue
		}

		entryPath := pathIndex(pathJoin("", spec.listKey), i)
		ref := d.Sources[entryPath]
		if ref.Path == "" {
			// Provenance was not recorded for this entry, which happens only
			// for a list built outside the merge. Skipping keeps the check
			// from inventing a location.
			continue
		}

		if prev, dup := seen[tag]; dup {
			// Both locations are named: the one being rejected and the one
			// that already claimed the tag. The user needs both to decide
			// which to rename, and AGENTS.md 21.4 requires the pair.
			return &Error{
				Op:  "merge",
				Ref: ref,
				Err: fmt.Errorf(
					"%w: %s %q is defined more than once; "+
						"tags are unique across all config files, so rename one of them",
					ErrConflict, spec.kind, tag),
				Related: dedupeRefs([]ir.SourceRef{prev}, d.Shadowed[entryPath]),
			}
		}
		seen[tag] = ref
	}
	return nil
}

// dedupeRefs returns the non-empty, distinct sources among its arguments,
// preserving order.
//
// The merge records a displaced definition in more than one place — as the
// entry's overridden origin and as a shadowed source — so a naive
// concatenation prints the same line twice and makes the message look like
// there are three definitions where there are two.
func dedupeRefs(refs ...[]ir.SourceRef) []ir.SourceRef {
	seen := map[ir.SourceRef]bool{}
	var out []ir.SourceRef
	for _, group := range refs {
		for _, r := range group {
			if r.Path == "" || seen[r] {
				continue
			}
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// OriginFor returns the provenance of a field path as an ir.Origin, including
// the list of sources it displaced.
//
// It is the conversion from the merge's internal bookkeeping to the shape the
// IR exposes, and is what compile copies into FileSet.Sources so that
// `lip config sources` can answer "who set this, and what did it override".
func (d *Doc) OriginFor(path string) ir.Origin {
	origin := ir.Origin{}
	if src, ok := d.Sources[path]; ok {
		origin.Source = src
	}
	if over, ok := d.Shadowed[path]; ok {
		origin.Overridden = append(origin.Overridden, over...)
	}
	return origin
}

// Files returns the merged documents' sources in load order.
func (d *Doc) FileSources() []ir.SourceRef {
	out := make([]ir.SourceRef, len(d.Files))
	copy(out, d.Files)
	return out
}
