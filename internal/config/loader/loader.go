// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package loader turns a configuration directory into an ir.FileSet.
//
// It is the IO half of the two-phase design: it scans directories, expands
// includes and globs, reads bytes and records where every value came from.
// It does not merge, validate or compile — those run on the FileSet it
// produces, in a step that performs no IO at all (AGENTS.md 5).
//
// Keeping that boundary sharp is what makes the merge logic testable without
// a filesystem, and it is the reason this package never returns an
// ir.Config.
package loader

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
)

// Defaults for the defensive limits in AGENTS.md 21.3.
const (
	// DefaultMaxDepth bounds include nesting. A chain deeper than this is
	// almost certainly a mistake, and the bound turns an accidental
	// deep recursion into a clear error.
	DefaultMaxDepth = 32

	// DefaultMaxFiles bounds a single wildcard expansion, to stop a pattern
	// like "**/*.toml" from pulling in an entire home directory.
	DefaultMaxFiles = 1000
)

// LoadOptions tunes a single Load call. The zero value applies the defaults
// from AGENTS.md 21.3, which is what callers should normally use.
type LoadOptions struct {
	// MaxDepth is the maximum include nesting, counting the entry file as
	// depth 0. Zero means DefaultMaxDepth.
	MaxDepth int

	// MaxFiles is the maximum number of files one wildcard pattern may
	// expand to. Zero means DefaultMaxFiles.
	MaxFiles int

	// AllowMissing permits the entry file itself to be absent, producing an
	// empty FileSet.
	//
	// This is for first-run convenience: a daemon started before the user has
	// written any configuration should come up with built-in defaults rather
	// than refuse to start. It applies only to the entry file — an include
	// must opt in individually with optional = true, so that a typo in a path
	// is never silently tolerated.
	AllowMissing bool
}

// withDefaults returns a copy with unset limits filled in.
func (o LoadOptions) withDefaults() LoadOptions {
	if o.MaxDepth <= 0 {
		o.MaxDepth = DefaultMaxDepth
	}
	if o.MaxFiles <= 0 {
		o.MaxFiles = DefaultMaxFiles
	}
	return o
}

// Load reads an entry file and everything it includes, producing a FileSet
// in merge order.
//
// The returned FileSet contains the files lowest-priority first, so applying
// them in slice order reproduces the intended precedence. Directories are
// read relative to the entry file's location; the returned Root is the
// directory the entry file lives in.
//
// Every failure is returned, never skipped. A file that cannot be read is
// reported with its path and line, because a silently dropped file is
// indistinguishable from a file that was applied (AGENTS.md 16, pitfall 20).
func Load(entry string, opts LoadOptions) (*ir.FileSet, error) {
	opts = opts.withDefaults()

	absEntry, err := filepath.Abs(entry)
	if err != nil {
		return nil, fmt.Errorf("resolve entry path %q: %w", entry, err)
	}
	absEntry = filepath.Clean(absEntry)

	// Resolve symlinks on the entry path before anything else, so that it
	// enters the visited set under the same name its includes will resolve
	// to. Without this, an entry reached through a symlink and later included
	// by its real path would be loaded twice (AGENTS.md 21.3).
	resolvedEntry, err := resolveForComparison(absEntry)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && opts.AllowMissing {
			return &ir.FileSet{
				Root:     filepath.Dir(absEntry),
				LoadedAt: time.Now().UTC(),
			}, nil
		}
		return nil, &Error{Op: "read", Ref: ir.SourceRef{Path: absEntry}, Err: err}
	}

	l := &loadState{
		opts:     opts,
		visited:  make(map[string]int),
		inFlight: make(map[string]int),
	}
	if err := l.walk(resolvedEntry, ir.FormatUnknown, nil, 0); err != nil {
		return nil, err
	}

	l.set.Root = filepath.Dir(resolvedEntry)
	l.set.LoadedAt = time.Now().UTC()
	return &l.set, nil
}

// loadState holds the mutable state of one Load call.
//
// visited and inFlight are deliberately separate maps, as AGENTS.md 21.3
// requires, because they answer different questions:
//
//   - inFlight is the current recursion stack. Finding a path here means a
//     cycle, and the stack itself is the chain to print.
//   - visited is every path already accepted. Finding a path here means it
//     was reached twice, which is normal (two includes naming one file) and
//     must not load it twice.
//
// Collapsing them into one set would either report diamond includes as
// cycles or load files twice, depending on which check ran first.
type loadState struct {
	opts LoadOptions
	set  ir.FileSet

	// visited maps a resolved absolute path to the index of its File in
	// set.Files, so a repeated include is a lookup rather than a scan.
	visited map[string]int

	// inFlight maps a resolved absolute path to its position in the current
	// recursion stack, enabling both cycle detection and chain reporting.
	inFlight map[string]int

	// stack is the active include chain, outermost first. It is maintained
	// as a slice so that the cycle error can print the whole chain in order.
	stack []ir.SourceRef
}

// walk loads one file and recurses into its includes.
//
// depth counts include nesting from the entry file at zero. declared carries
// a format explicitly stated by the include directive; it is Unknown for the
// entry file and for shorthand includes, which rely on the extension.
func (l *loadState) walk(resolvedPath string, declared ir.Format, chain []ir.SourceRef, depth int) error {
	// A path already in flight means the chain has looped back on itself.
	if start, ok := l.inFlight[resolvedPath]; ok {
		return errorAt("include", ir.SourceRef{Path: resolvedPath},
			fmt.Errorf("%w: %s", ErrCycle, formatCycle(l.stack[start:], resolvedPath)), chain)
	}

	// A path already loaded contributes once. Returning here is what makes a
	// diamond include (A includes B and C, both include D) load D a single
	// time instead of duplicating its contents.
	if _, ok := l.visited[resolvedPath]; ok {
		return nil
	}

	if depth > l.opts.MaxDepth {
		return errorAt("include", ir.SourceRef{Path: resolvedPath},
			fmt.Errorf("%w: depth %d exceeds the limit of %d",
				ErrMaxDepth, depth, l.opts.MaxDepth), chain)
	}

	file, err := l.readFile(resolvedPath, declared)
	if err != nil {
		return err
	}

	dir := filepath.Dir(resolvedPath)
	d, err := parseDirectives(file)
	if err != nil {
		return prependChain(err, chain)
	}

	// Mark in-flight before descending so that a self-include is caught.
	l.inFlight[resolvedPath] = len(l.stack)
	l.stack = append(l.stack, file.Ref)
	defer func() {
		delete(l.inFlight, resolvedPath)
		l.stack = l.stack[:len(l.stack)-1]
	}()

	// Order matters: a file's own content is applied before the files it
	// includes, so that an included fragment is more specific and wins.
	// Dependencies load depth-first, and each is appended before its parent.
	for _, inc := range d.Includes {
		if err := l.loadInclude(inc, d, dir, file.Ref, depth); err != nil {
			return err
		}
	}

	// The file is recorded after its includes so that Files is in a valid
	// merge order: every file appears after the ones it depends on.
	l.visited[resolvedPath] = len(l.set.Files)
	l.set.Files = append(l.set.Files, *file)
	return nil
}

// loadInclude expands one include directive and loads each resulting path.
func (l *loadState) loadInclude(inc include, d directives, dir string, ref ir.SourceRef, depth int) error {
	// Attribute the directive to its own line, so a failure names the line
	// that declared the include rather than the top of the file.
	ref.Line = inc.Line

	paths, err := expandPattern(dir, inc.Pattern, l.opts.MaxFiles)
	if err != nil {
		return errorAt("include", ref, err, l.stack)
	}

	paths, err = applyExcludes(paths, d.Excludes, dir, ref, l.stack)
	if err != nil {
		return err
	}

	for _, p := range paths {
		if err := l.loadIncluded(p, inc, dir, ref, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// loadIncluded loads one path produced by an include, applying the optional
// and explicit-format rules that belong to that directive.
func (l *loadState) loadIncluded(path string, inc include, dir string, ref ir.SourceRef, depth int) error {
	resolved, err := resolveForComparison(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// An exact pattern naming a missing file is a likely typo and is
			// reported unless the directive opted out.
			if inc.Optional && !hasMeta(inc.Pattern) {
				return nil
			}
			if hasMeta(inc.Pattern) {
				// A wildcard that matched nothing is not an error: an empty
				// directory is a legitimate state, and expandPattern only
				// yields existing paths.
				return nil
			}
			return errorAt("read", ref,
				fmt.Errorf("%w: %q", ErrMissingFile, inc.Pattern), l.stack)
		}
		return errorAt("read", ref, err, l.stack)
	}

	return l.walk(resolved, inc.Format, l.stack, depth)
}

// readFile reads one file into an ir.File, computing its hash and format.
//
// declared is the format stated on the include directive, or Unknown to fall
// back to the extension. An explicit value wins because it is the only way to
// include a file whose extension does not identify its syntax.
func (l *loadState) readFile(path string, declared ir.Format) (*ir.File, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, &Error{Op: "read", Ref: ir.SourceRef{Path: path}, Err: err}
	}

	format, err := detectFormat(path, declared, ir.SourceRef{Path: path})
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, &Error{Op: "stat", Ref: ir.SourceRef{Path: path}, Err: err}
	}

	sum := sha256.Sum256(content)
	return &ir.File{
		Ref:     ir.SourceRef{Path: path, Format: format},
		Content: content,
		SHA256:  hex.EncodeToString(sum[:]),
		Size:    int64(len(content)),
		ModTime: info.ModTime(),
	}, nil
}

// resolveForComparison turns a path into the canonical form used as the key
// for both cycle detection and de-duplication.
//
// Symlinks are resolved before comparison, which is what makes
// "a.toml → link/b.toml → a.toml" detectable. Comparing only cleaned paths
// would miss it and recurse until the stack overflows (AGENTS.md 21.3).
func resolveForComparison(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// formatCycle renders a detected cycle as "a → b → c → a".
//
// The repeated tail is included deliberately: seeing the chain return to its
// start is what makes the loop obvious at a glance, whereas a bare list of
// participants leaves the reader to work out the ordering themselves.
func formatCycle(stack []ir.SourceRef, repeated string) string {
	parts := make([]string, 0, len(stack)+1)
	for _, ref := range stack {
		parts = append(parts, ref.Path)
	}
	parts = append(parts, repeated)

	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " -> "
		}
		out += p
	}
	return out
}

// prependChain attaches an outer include chain to an error that lacks one.
//
// Errors raised while parsing a file are constructed before the caller knows
// how the file was reached, so the chain is attached on the way out. Without
// it, "file not found" inside a deeply included fragment would name a path
// the user never wrote and cannot search for.
func prependChain(err error, chain []ir.SourceRef) error {
	if len(chain) == 0 {
		return err
	}
	var e *Error
	if errors.As(err, &e) && len(e.Chain) == 0 {
		e.Chain = chain
	}
	return err
}
