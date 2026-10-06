// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package loader

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
)

// expandPattern resolves one include pattern to a sorted list of absolute
// paths.
//
// Single-segment (*) and recursive (**) wildcards, single-character (?) and
// brace alternation ({a,b}) are all handled by doublestar. The standard
// library's filepath.Glob supports none of the last three, and AGENTS.md 21.2
// records that reimplementing them by hand is explicitly the wrong trade:
// brace expansion in particular is easy to get subtly wrong at the edges.
//
// Ordering is byte-wise over the full path rather than locale-aware, so that
// load order is identical on every machine (AGENTS.md 21.1).
func expandPattern(baseDir, pattern string, maxFiles int) ([]string, error) {
	if err := rejectRemote(pattern); err != nil {
		return nil, err
	}

	// Patterns are resolved relative to the directory of the file that
	// declared them, not the process working directory or the configured
	// root. An absolute pattern is used as written.
	target := pattern
	if !filepath.IsAbs(pattern) {
		target = filepath.Join(baseDir, pattern)
	}

	// A pattern with no metacharacters is an exact path. It is handled
	// without globbing so that a missing file yields "not found" rather than
	// an empty expansion, which would otherwise be indistinguishable from a
	// wildcard that matched nothing.
	if !hasMeta(pattern) {
		return []string{filepath.Clean(target)}, nil
	}

	// Expand enforces the file-count cap internally, so an oversized
	// expansion fails here rather than after materialising.
	matches, err := doublestar.FilepathGlob(target)
	if err != nil {
		return nil, fmt.Errorf("expand pattern %q: %w", pattern, err)
	}
	if len(matches) > maxFiles {
		return nil, fmt.Errorf("%w: pattern %q matched %d files, limit is %d; narrow the pattern",
			ErrMaxFiles, pattern, len(matches), maxFiles)
	}

	cleaned := make([]string, 0, len(matches))
	for _, m := range matches {
		cleaned = append(cleaned, filepath.Clean(m))
	}
	sort.Strings(cleaned)
	return cleaned, nil
}

// hasMeta reports whether a pattern contains any wildcard syntax.
//
// It is a plain substring test rather than a brace-aware scan, because the
// only decision it drives is "exact path or glob". A path containing a
// literal `{` would fall to the glob branch and still resolve correctly;
// doublestar treats unmatched braces literally.
func hasMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[{")
}

// applyExcludes removes paths matching any exclude pattern.
//
// Excludes are applied to the expansion result, as AGENTS.md 21.2 specifies,
// so an exclude can prune files contributed by several include patterns
// without editing each one.
//
// An unreadable or malformed exclude pattern is reported rather than ignored:
// silently ignoring it would leave files loaded that the user believes are
// excluded, which is the failure mode hardest to notice.
func applyExcludes(paths []string, excludes []string, baseDir string, ref ir.SourceRef, chain []ir.SourceRef) ([]string, error) {
	if len(excludes) == 0 {
		return paths, nil
	}

	kept := make([]string, 0, len(paths))
	for _, p := range paths {
		excluded := false
		for _, ex := range excludes {
			if err := rejectRemote(ex); err != nil {
				return nil, errorAt("exclude", ref, err, chain)
			}
			target := ex
			if !filepath.IsAbs(ex) {
				target = filepath.Join(baseDir, ex)
			}
			match, err := doublestar.Match(target, p)
			if err != nil {
				return nil, errorAt("exclude", ref,
					fmt.Errorf("%w: invalid exclude pattern %q: %v", ErrParse, ex, err), chain)
			}
			if match {
				excluded = true
				break
			}
		}
		if !excluded {
			kept = append(kept, p)
		}
	}
	return kept, nil
}

// rejectRemote reports an include or exclude that points at a URL.
//
// Remote includes are out of scope rather than unimplemented: configuration
// must load with no network, so a URL is rejected loudly instead of being
// fetched or quietly skipped (AGENTS.md 21.2).
func rejectRemote(pattern string) error {
	lower := strings.ToLower(strings.TrimSpace(pattern))
	for _, scheme := range []string{"http://", "https://"} {
		if strings.HasPrefix(lower, scheme) {
			return fmt.Errorf("%w: %q", ErrRemoteInclude, pattern)
		}
	}
	return nil
}

// detectFormat decides a file's syntax from its extension.
//
// Only the two known extensions are accepted. A file that cannot be
// classified is an error, never a guess, because a wrong guess surfaces as a
// confusing syntax error from the wrong parser (AGENTS.md 21.2).
func detectFormat(path string, explicit ir.Format, ref ir.SourceRef) (ir.Format, error) {
	if explicit != ir.FormatUnknown {
		return explicit, nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		return ir.FormatTOML, nil
	case ".yaml", ".yml":
		return ir.FormatYAML, nil
	default:
		return ir.FormatUnknown, errorAt("detect format", ref,
			fmt.Errorf("%w: %q has no recognized extension; use .toml/.yaml, or set an explicit format",
				ErrFormatUnknown, path), nil)
	}
}
