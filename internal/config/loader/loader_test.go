// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package loader

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
)

// entry builds the path of a fixture entry file under testdata.
func entry(t *testing.T, parts ...string) string {
	t.Helper()
	return filepath.Join(append([]string{"testdata"}, parts...)...)
}

// loadedPaths returns the base names of the loaded files, in load order.
func loadedPaths(set *ir.FileSet) []string {
	out := make([]string, 0, len(set.Files))
	for _, f := range set.Files {
		out = append(out, filepath.Base(f.Ref.Path))
	}
	return out
}

// contains reports whether want appears in got.
func contains(got []string, want string) bool {
	for _, g := range got {
		if g == want {
			return true
		}
	}
	return false
}

func TestLoadSingleTOML(t *testing.T) {
	set, err := Load(entry(t, "single_toml", "main.toml"), LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(set.Files) != 1 {
		t.Fatalf("want 1 file, got %d", len(set.Files))
	}
	f := set.Files[0]
	if f.Ref.Format != ir.FormatTOML {
		t.Errorf("format = %q, want toml", f.Ref.Format)
	}
	if !filepath.IsAbs(f.Ref.Path) {
		t.Errorf("path %q is not absolute", f.Ref.Path)
	}
	if len(f.Content) == 0 {
		t.Error("content is empty")
	}
	if f.SHA256 == "" {
		t.Error("sha256 is empty")
	}
	if f.Size != int64(len(f.Content)) {
		t.Errorf("size = %d, content length = %d", f.Size, len(f.Content))
	}
	if set.Root == "" {
		t.Error("root is empty")
	}
}

func TestLoadSingleYAML(t *testing.T) {
	for _, name := range []string{"main.yaml", "alt.yml"} {
		t.Run(name, func(t *testing.T) {
			set, err := Load(entry(t, "single_yaml", name), LoadOptions{})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(set.Files) != 1 {
				t.Fatalf("want 1 file, got %d", len(set.Files))
			}
			if got := set.Files[0].Ref.Format; got != ir.FormatYAML {
				t.Errorf("format = %q, want yaml", got)
			}
		})
	}
}

func TestLoadUnknownFormatFails(t *testing.T) {
	_, err := Load(entry(t, "badformat", "main.txt"), LoadOptions{})
	if err == nil {
		t.Fatal("want an error for an unrecognized extension, got nil")
	}
	if !errors.Is(err, ErrFormatUnknown) {
		t.Errorf("error = %v, want ErrFormatUnknown", err)
	}
	// The message must name the file, since that is the user's only clue.
	if !strings.Contains(err.Error(), "main.txt") {
		t.Errorf("error %q does not name the offending file", err)
	}
}

func TestLoadIncludeSingleFile(t *testing.T) {
	set, err := Load(entry(t, "include_single", "main.toml"), LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(set.Files) != 2 {
		t.Fatalf("want 2 files, got %d (%v)", len(set.Files), loadedPaths(set))
	}
	// The including file comes first and the fragment after it, so that
	// applying the files in order gives the fragment the final say.
	//
	// This direction is the five-level precedence rule of AGENTS.md 21.1: the
	// main config is level 2 and a toml.d/ fragment is level 3, so the
	// fragment must win. The opposite order made every fragment lose to the
	// file that included it.
	if got := loadedPaths(set); got[0] != "main.toml" || got[1] != "extra.toml" {
		t.Errorf("load order = %v, want [main.toml extra.toml]", got)
	}
}

func TestLoadIncludeStarWildcard(t *testing.T) {
	set, err := Load(entry(t, "include_star", "main.toml"), LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := loadedPaths(set)
	// Both matching .toml files, and no .txt file.
	if len(got) != 3 {
		t.Fatalf("want 3 files (main + 2 fragments), got %d: %v", len(got), got)
	}
	if !contains(got, "one.toml") || !contains(got, "two.toml") {
		t.Errorf("missing wildcard matches: %v", got)
	}
	if contains(got, "skip.txt") {
		t.Errorf("a non-matching file was loaded: %v", got)
	}
}

func TestLoadIncludeDoublestarRecursive(t *testing.T) {
	set, err := Load(entry(t, "include_doublestar", "main.toml"), LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := loadedPaths(set)
	if len(got) != 4 {
		t.Fatalf("want 4 files (main + 3 at every depth), got %d: %v", len(got), got)
	}
	for _, want := range []string{"top.toml", "mid.toml", "bottom.toml"} {
		if !contains(got, want) {
			t.Errorf("** did not reach %s: %v", want, got)
		}
	}
}

func TestLoadIncludeBraceExpansion(t *testing.T) {
	set, err := Load(entry(t, "include_brace", "main.toml"), LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := loadedPaths(set)
	if len(got) != 3 {
		t.Fatalf("want 3 files (main + alpha + beta), got %d: %v", len(got), got)
	}
	if !contains(got, "alpha.toml") || !contains(got, "beta.toml") {
		t.Errorf("brace alternatives missing: %v", got)
	}
	if contains(got, "gamma.toml") {
		t.Errorf("a file outside the brace set was loaded: %v", got)
	}
}

func TestLoadExcludeRemovesMatches(t *testing.T) {
	set, err := Load(entry(t, "include_exclude", "main.toml"), LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := loadedPaths(set)
	if len(got) != 3 {
		t.Fatalf("want 3 files (main + 2 kept), got %d: %v", len(got), got)
	}
	if !contains(got, "a.toml") || !contains(got, "b.toml") {
		t.Errorf("kept fragments missing: %v", got)
	}
	if contains(got, "c.toml") {
		t.Errorf("excluded file was still loaded: %v", got)
	}
}

func TestLoadCrossFormatExplicit(t *testing.T) {
	// The fragment has a .txt extension, so only the explicit format makes it
	// loadable at all. Without it, the loader must refuse rather than guess.
	if _, err := Load(entry(t, "include_crossfmt", "main.toml"), LoadOptions{}); err == nil {
		t.Fatal("want failure when a .txt fragment is included without an explicit format")
	}

	set, err := Load(entry(t, "include_crossfmt", "main_structured.toml"), LoadOptions{})
	if err != nil {
		t.Fatalf("Load with explicit format: %v", err)
	}
	if len(set.Files) != 2 {
		t.Fatalf("want 2 files, got %d: %v", len(set.Files), loadedPaths(set))
	}
	found := false
	for _, f := range set.Files {
		if strings.HasSuffix(f.Ref.Path, "nodes.txt") {
			found = true
			if f.Ref.Format != ir.FormatYAML {
				t.Errorf("explicit format not applied: got %q, want yaml", f.Ref.Format)
			}
		}
	}
	if !found {
		t.Error("the cross-format fragment was not loaded")
	}
}

func TestLoadOptionalMissingSucceeds(t *testing.T) {
	set, err := Load(entry(t, "include_optional_missing", "main.toml"), LoadOptions{})
	if err != nil {
		t.Fatalf("optional include of a missing file must succeed: %v", err)
	}
	if len(set.Files) != 1 {
		t.Fatalf("want only the entry file, got %d: %v", len(set.Files), loadedPaths(set))
	}
}

func TestLoadRequiredMissingFails(t *testing.T) {
	_, err := Load(entry(t, "include_optional_missing", "required.toml"), LoadOptions{})
	if err == nil {
		t.Fatal("want failure for a required include naming a missing file")
	}
	if !errors.Is(err, ErrMissingFile) {
		t.Errorf("error = %v, want ErrMissingFile", err)
	}
}

func TestLoadCircularIncludeDetected(t *testing.T) {
	_, err := Load(entry(t, "circular", "a.toml"), LoadOptions{})
	if err == nil {
		t.Fatal("want a cycle error")
	}
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("error = %v, want ErrCycle", err)
	}
	// The chain must be printed so the user can see which files form the loop.
	msg := err.Error()
	if !strings.Contains(msg, "a.toml") || !strings.Contains(msg, "b.toml") {
		t.Errorf("cycle message does not name both files: %q", msg)
	}
	if !strings.Contains(msg, "->") {
		t.Errorf("cycle message has no chain arrow: %q", msg)
	}
}

func TestLoadCircularThreeNodeChain(t *testing.T) {
	_, err := Load(entry(t, "circular3", "a.toml"), LoadOptions{})
	if err == nil {
		t.Fatal("want a cycle error")
	}
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("error = %v, want ErrCycle", err)
	}
	msg := err.Error()
	for _, name := range []string{"a.toml", "b.toml", "c.toml"} {
		if !strings.Contains(msg, name) {
			t.Errorf("chain omits %s: %q", name, msg)
		}
	}
}

func TestLoadSelfReferenceDetected(t *testing.T) {
	_, err := Load(entry(t, "self_ref", "a.toml"), LoadOptions{})
	if err == nil {
		t.Fatal("want a cycle error for a self-include")
	}
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("error = %v, want ErrCycle", err)
	}
}

func TestLoadSymlinkCycleDetected(t *testing.T) {
	// symlink/a.toml includes shared/b.toml, which includes ../a.toml. The
	// files are reached by different spellings, so only a comparison over
	// EvalSymlinks-resolved paths sees the loop.
	_, err := Load(entry(t, "symlink", "a.toml"), LoadOptions{})
	if err == nil {
		t.Fatal("want a cycle error through the symlink")
	}
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("error = %v, want ErrCycle", err)
	}
}

func TestLoadSymlinkAliasIsSameFile(t *testing.T) {
	// symlink/c.toml includes ../link/b.toml, where link is a symlink to
	// shared. Resolved, that is the same file as shared/b.toml, which itself
	// includes ../a.toml. A path-string comparison would treat the two
	// spellings as distinct files and miss the cycle entirely, recursing
	// until the stack overflows.
	_, err := Load(entry(t, "symlink", "c.toml"), LoadOptions{})
	if err == nil {
		t.Fatal("want a cycle error: the symlinked spelling reaches an already-open file")
	}
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("error = %v, want ErrCycle (resolution should unify the two spellings)", err)
	}
}

func TestLoadDepthLimitExceeded(t *testing.T) {
	// The fixture is a 36-file chain, deeper than the default limit of 32.
	_, err := Load(entry(t, "depth", "d00.toml"), LoadOptions{})
	if err == nil {
		t.Fatal("want a depth error")
	}
	if !errors.Is(err, ErrMaxDepth) {
		t.Fatalf("error = %v, want ErrMaxDepth", err)
	}
	if !strings.Contains(err.Error(), "32") {
		t.Errorf("error does not state the limit: %q", err)
	}
}

func TestLoadDepthLimitConfigurable(t *testing.T) {
	// A tighter limit must fail sooner, proving the option is honoured,
	// and a looser one must let the same fixture through.
	if _, err := Load(entry(t, "depth", "d00.toml"), LoadOptions{MaxDepth: 3}); !errors.Is(err, ErrMaxDepth) {
		t.Fatalf("MaxDepth=3: error = %v, want ErrMaxDepth", err)
	}
	set, err := Load(entry(t, "depth", "d00.toml"), LoadOptions{MaxDepth: 64})
	if err != nil {
		t.Fatalf("MaxDepth=64 should allow the 36-file chain: %v", err)
	}
	if len(set.Files) != 36 {
		t.Errorf("want 36 files, got %d", len(set.Files))
	}
}

func TestLoadWildcardFileLimitExceeded(t *testing.T) {
	// The fixture holds 60 fragments; a cap of 10 must reject the expansion.
	_, err := Load(entry(t, "wide", "main.toml"), LoadOptions{MaxFiles: 10})
	if err == nil {
		t.Fatal("want a file-limit error")
	}
	if !errors.Is(err, ErrMaxFiles) {
		t.Fatalf("error = %v, want ErrMaxFiles", err)
	}
	if !strings.Contains(err.Error(), "10") {
		t.Errorf("error does not state the limit: %q", err)
	}

	// The same fixture loads when the cap is raised, proving the failure was
	// the limit and not a malformed pattern.
	set, err := Load(entry(t, "wide", "main.toml"), LoadOptions{MaxFiles: 100})
	if err != nil {
		t.Fatalf("MaxFiles=100: %v", err)
	}
	if len(set.Files) != 61 {
		t.Errorf("want 61 files (main + 60), got %d", len(set.Files))
	}
}

func TestExpandPatternLimitIsUnitTestable(t *testing.T) {
	// The expansion helper enforces the cap itself, so the limit can be
	// exercised without creating thousands of files on disk.
	dir := filepath.Join("testdata", "wide")
	_, err := expandPattern(dir, "files/*.toml", 5)
	if !errors.Is(err, ErrMaxFiles) {
		t.Fatalf("error = %v, want ErrMaxFiles", err)
	}

	got, err := expandPattern(dir, "files/*.toml", 1000)
	if err != nil {
		t.Fatalf("expandPattern: %v", err)
	}
	if len(got) != 60 {
		t.Errorf("want 60 matches, got %d", len(got))
	}
}

func TestLoadRemoteIncludeRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.toml")
	if err := os.WriteFile(path, []byte("include = [\"https://example.com/x.toml\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, LoadOptions{})
	if !errors.Is(err, ErrRemoteInclude) {
		t.Fatalf("error = %v, want ErrRemoteInclude", err)
	}
}

func TestLoadMissingEntryFile(t *testing.T) {
	missing := entry(t, "single_toml", "nope.toml")
	if _, err := Load(missing, LoadOptions{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want a not-exist cause", err)
	}

	// AllowMissing turns the same call into an empty, usable FileSet, which
	// is what lets a daemon start before any configuration exists.
	set, err := Load(missing, LoadOptions{AllowMissing: true})
	if err != nil {
		t.Fatalf("AllowMissing: %v", err)
	}
	if len(set.Files) != 0 {
		t.Errorf("want 0 files, got %d", len(set.Files))
	}
	if set.Root == "" {
		t.Error("root should still be set for a missing entry file")
	}
}

func TestLoadParseErrorCarriesPosition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.toml")
	if err := os.WriteFile(path, []byte("[log\nlevel = \"info\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, LoadOptions{})
	if err == nil {
		t.Fatal("want a parse error")
	}
	if !errors.Is(err, ErrParse) {
		t.Fatalf("error = %v, want ErrParse", err)
	}
	var le *Error
	if !errors.As(err, &le) {
		t.Fatalf("error is not a *loader.Error: %T", err)
	}
	if le.Ref.Line == 0 {
		t.Errorf("parse error carries no line number: %+v", le.Ref)
	}
}

func TestLoadDiamondIncludeLoadsOnce(t *testing.T) {
	// main includes both left and right, and each of those includes shared.
	// The shared file must appear exactly once: loading it twice would
	// duplicate every value it defines.
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("shared.toml", "[shared]\nv = 1\n")
	write("left.toml", "include = [\"shared.toml\"]\n")
	write("right.toml", "include = [\"shared.toml\"]\n")
	write("main.toml", "include = [\"left.toml\", \"right.toml\"]\n")

	set, err := Load(filepath.Join(dir, "main.toml"), LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	count := 0
	for _, f := range set.Files {
		if filepath.Base(f.Ref.Path) == "shared.toml" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("shared.toml loaded %d times, want 1", count)
	}
}

// TestLoadWildcardMissLogsWarning verifies the non-fatal diagnostic for an
// include pattern that matched nothing.
//
// The load must still succeed: an empty directory is a legitimate state. The
// point of the warning is that "matched nothing" stops being
// indistinguishable from "configuration applied cleanly".
func TestLoadWildcardMissLogsWarning(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main.toml")
	if err := os.WriteFile(main, []byte(`
include = ["fragments/*.toml"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	opts := LoadOptions{Logger: &logger}

	set, err := Load(main, opts)
	if err != nil {
		t.Fatalf("a wildcard matching nothing must not fail the load: %v", err)
	}
	if len(set.Files) != 1 {
		t.Fatalf("files = %d, want 1 (only the main config)", len(set.Files))
	}

	out := buf.String()
	if !strings.Contains(out, "matched no files") {
		t.Fatalf("no warning was logged for the empty wildcard; got %q", out)
	}
	if !strings.Contains(out, "fragments/*.toml") {
		t.Errorf("warning does not quote the pattern; got %q", out)
	}
	if !strings.Contains(out, "main.toml") {
		t.Errorf("warning does not name the declaring file; got %q", out)
	}
}

// TestLoadExcludedToEmptyLogsWarning verifies that a pattern whose every match
// was excluded is reported distinctly, since that points at an over-broad
// exclude rather than a mistyped path.
func TestLoadExcludedToEmptyLogsWarning(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "fragments"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fragments", "a.toml"), []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.toml")
	if err := os.WriteFile(main, []byte(`
include = ["fragments/*.toml"]
exclude = ["fragments/*"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger := zerolog.New(&buf)

	set, err := Load(main, LoadOptions{Logger: &logger})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(set.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(set.Files))
	}

	if out := buf.String(); !strings.Contains(out, "excluded") {
		t.Fatalf("no exclusion warning was logged; got %q", out)
	}
}

// TestLoadWithoutLoggerDoesNotPanic verifies that the default nil logger is
// safe, so a caller that does not care about warnings needs no setup.
func TestLoadWithoutLoggerDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main.toml")
	if err := os.WriteFile(main, []byte(`include = ["nothing/*.toml"]`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(main, LoadOptions{}); err != nil {
		t.Fatalf("Load with a nil logger: %v", err)
	}
}

// TestLoadMisfiledIncludeWarns verifies the diagnostic for an include key that
// TOML filed inside a table.
//
// The load must still succeed — the document is valid TOML — but the user has
// to be told, because the alternative is a silently unapplied fragment, which
// is the failure mode this warning exists to eliminate.
func TestLoadMisfiledIncludeWarns(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "toml.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "toml.d", "extra.toml"), []byte("mode = \"client\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The include sits below [log], so TOML reads it as log.include.
	main := filepath.Join(dir, "liproxy.toml")
	if err := os.WriteFile(main, []byte(`
[log]
level = "info"
include = ["toml.d/*.toml"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger := zerolog.New(&buf)

	set, err := Load(main, LoadOptions{Logger: &logger})
	if err != nil {
		t.Fatalf("a misfiled include must not fail the load: %v", err)
	}
	if len(set.Files) != 1 {
		t.Fatalf("files = %d, want 1 (the fragment is not included)", len(set.Files))
	}

	out := buf.String()
	if !strings.Contains(out, "log.include") {
		t.Fatalf("warning does not name the misfiled key; got %q", out)
	}
	if !strings.Contains(out, "liproxy.toml") {
		t.Errorf("warning does not name the file; got %q", out)
	}
	// The include key is on line 4 of the fixture above, counting the leading
	// blank line.
	if !strings.Contains(out, `"line":4`) {
		t.Errorf("warning does not carry the line number; got %q", out)
	}
}

// TestLoadMisfiledExcludeWarns verifies the same for a misfiled exclude.
func TestLoadMisfiledExcludeWarns(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "liproxy.toml")
	if err := os.WriteFile(main, []byte(`
[dns]
enabled = true
exclude = ["x/*"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	if _, err := Load(main, LoadOptions{Logger: &logger}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "dns.exclude") {
		t.Fatalf("no warning for a misfiled exclude; got %q", out)
	}
}

// TestLoadCorrectIncludeDoesNotWarn verifies that the correctly placed include
// produces no diagnostic, so the warning cannot become noise that users learn
// to ignore.
func TestLoadCorrectIncludeDoesNotWarn(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "toml.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "toml.d", "extra.toml"), []byte("mode = \"client\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "liproxy.toml")
	if err := os.WriteFile(main, []byte(`
include = ["toml.d/*.toml"]

[log]
level = "info"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger := zerolog.New(&buf)

	set, err := Load(main, LoadOptions{Logger: &logger})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(set.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(set.Files))
	}
	if out := buf.String(); strings.Contains(out, "belongs to a table") {
		t.Fatalf("a correctly placed include produced a warning: %q", out)
	}
}
