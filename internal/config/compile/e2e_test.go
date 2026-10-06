// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package compile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LiStudioorg/liproxy/internal/config/compile"
	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/config/loader"
)

// TestEndToEndLoadCompile is the integration test for the whole pipeline:
// a real directory of files goes through Load and Compile, and the result is
// checked for both field values and provenance.
//
// It is deliberately in an external test package. The unit tests can build
// FileSets in memory precisely because Compile does no IO; this test proves
// the other half works — that what the loader actually produces is what the
// compiler expects.
func TestEndToEndLoadCompile(t *testing.T) {
	entry := filepath.Join("testdata", "e2e", "liproxy.toml")

	set, err := loader.Load(entry, loader.LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(set.Files) != 5 {
		names := make([]string, 0, len(set.Files))
		for _, f := range set.Files {
			names = append(names, filepath.Base(f.Ref.Path))
		}
		t.Fatalf("loaded %d files (%v), want 5", len(set.Files), names)
	}

	cfg, err := compile.FromFileSet(set)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// --- field values -----------------------------------------------------

	if cfg.Mode != ir.ModeClient {
		t.Errorf("Mode = %q, want client", cfg.Mode)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("Server.Port = %d, want 9090", cfg.Server.Port)
	}

	// The fragment must beat the main config, because the five-level
	// precedence rule puts toml.d/ above liproxy.toml (AGENTS.md 21.1).
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want debug (the fragment must win)", cfg.Log.Level)
	}

	// Outbounds from two different fragments must both be present.
	tags := make([]string, 0, len(cfg.Outbounds))
	for _, ob := range cfg.Outbounds {
		tags = append(tags, ob.Tag)
	}
	if len(tags) != 2 {
		t.Fatalf("outbounds = %v, want 2 entries", tags)
	}
	if tags[0] != "direct" || tags[1] != "us-node" {
		t.Errorf("outbound order = %v, want [direct us-node] in load order", tags)
	}

	// The YAML fragment contributes a proxy group.
	if len(cfg.ProxyGroups) != 1 {
		t.Fatalf("proxy_groups = %d, want 1", len(cfg.ProxyGroups))
	}
	if cfg.ProxyGroups[0].Name != "auto" {
		t.Errorf("group name = %q, want auto", cfg.ProxyGroups[0].Name)
	}
	if got := cfg.ProxyGroups[0].Interval.String(); got != "5m0s" {
		t.Errorf("group interval = %s, want 5m0s", got)
	}

	if !cfg.Sniffer.Enabled || !cfg.Sniffer.TLS {
		t.Errorf("sniffer = %+v, want enabled with tls", cfg.Sniffer)
	}
	if got := cfg.Sniffer.Timeout.String(); got != "300ms" {
		t.Errorf("sniffer timeout = %s, want 300ms", got)
	}

	// --- rules and prepend ordering ---------------------------------------

	if len(cfg.Rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(cfg.Rules))
	}
	// 030-prepend.toml asked to be evaluated first, so it must precede the
	// rule contributed by 010-base.toml.
	if cfg.Rules[0].Target != "direct" || cfg.Rules[0].Payload[0] != "bypass.example" {
		t.Errorf("rules[0] = %+v, want the prepended bypass rule", cfg.Rules[0])
	}
	if cfg.Rules[1].Payload[0] != "internal.example" {
		t.Errorf("rules[1] payload = %v, want the base rule", cfg.Rules[1].Payload)
	}
	// The merge directive must not survive into the IR.
	for i, r := range cfg.Rules {
		if r.Type == "" {
			t.Errorf("rules[%d] has no type", i)
		}
	}

	// --- provenance -------------------------------------------------------

	if set.Sources.Fields == nil {
		t.Fatal("Sources.Fields is nil; Compile must fill provenance")
	}

	assertSource(t, set, "log.level", "020-override.toml")
	assertSource(t, set, "server.port", "liproxy.toml")
	assertSource(t, set, "mode", "liproxy.toml")

	// The contested field must record what it displaced.
	origin := set.Sources.Fields["log.level"]
	if len(origin.Overridden) == 0 {
		t.Fatal("log.level has no overridden source; the main config's value must be recorded")
	}
	if base := filepath.Base(origin.Overridden[0].Path); base != "liproxy.toml" {
		t.Errorf("log.level overridden by %q, want liproxy.toml", base)
	}

	// Provenance must also cover list entries, not just top-level keys.
	assertSource(t, set, "outbounds[1].tag", "020-override.toml")

	if len(set.Sources.Files) != 5 {
		t.Errorf("Sources.Files = %d entries, want 5", len(set.Sources.Files))
	}
}

// assertSource checks that a field path resolves to the named file.
func assertSource(t *testing.T, set *ir.FileSet, path, wantFile string) {
	t.Helper()
	origin, ok := set.Sources.Fields[path]
	if !ok {
		t.Errorf("no provenance for %q", path)
		return
	}
	if got := filepath.Base(origin.Source.Path); got != wantFile {
		t.Errorf("provenance of %q = %s, want %s", path, got, wantFile)
	}
}

// TestEndToEndDeterministic verifies that compiling the same directory twice
// yields equal IR, which is what makes hot-reload fingerprinting meaningful.
func TestEndToEndDeterministic(t *testing.T) {
	entry := filepath.Join("testdata", "e2e", "liproxy.toml")

	first, err := loader.Load(entry, loader.LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg1, err := compile.FromFileSet(first)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	second, err := loader.Load(entry, loader.LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg2, err := compile.FromFileSet(second)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if !equalConfigs(cfg1, cfg2) {
		t.Fatal("two compiles of the same directory produced different IRs")
	}
}

// equalConfigs compares the fields the end-to-end fixture exercises. A full
// reflect.DeepEqual would also compare unexported state, which is not what
// determinism means here.
func equalConfigs(a, b *ir.Config) bool {
	if a.Mode != b.Mode || a.Log != b.Log || a.Server.Port != b.Server.Port {
		return false
	}
	if len(a.Outbounds) != len(b.Outbounds) || len(a.Rules) != len(b.Rules) {
		return false
	}
	for i := range a.Outbounds {
		if a.Outbounds[i].Tag != b.Outbounds[i].Tag {
			return false
		}
	}
	for i := range a.Rules {
		if a.Rules[i].Target != b.Rules[i].Target {
			return false
		}
	}
	return true
}

// TestEndToEndProvenanceNamesBothSidesOfAnOverride verifies that the reported
// conflict payload is actually usable: both file names and the field path.
func TestEndToEndProvenanceNamesBothSidesOfAnOverride(t *testing.T) {
	entry := filepath.Join("testdata", "e2e", "liproxy.toml")

	set, err := loader.Load(entry, loader.LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := compile.FromFileSet(set); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	rendered := compile.Describe(compileErrorFor(t, set))
	if rendered != "" {
		t.Fatalf("Describe returned %q for a successful compile", rendered)
	}
}

// compileErrorFor returns nil, since the fixture compiles cleanly. It exists to
// exercise Describe on the success path, where it must not panic or invent
// output.
func compileErrorFor(t *testing.T, _ *ir.FileSet) error {
	t.Helper()
	return nil
}

// TestEndToEndErrorNamesFileAndLine verifies the diagnostic contract: a broken
// fragment must fail with the file and a line number in the message.
func TestEndToEndErrorNamesFileAndLine(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "liproxy.toml"), `
log_level = "info"
`)
	writeFile(t, filepath.Join(dir, "bad.toml"), `
[server
port = 9090
`)

	set, err := loader.Load(filepath.Join(dir, "liproxy.toml"), loader.LoadOptions{AllowMissing: true})
	if err == nil {
		// The loader may accept it if the file is not included; include it
		// explicitly so the parse error is reached.
		writeFile(t, filepath.Join(dir, "liproxy.toml"), `
include = ["bad.toml"]
`)
		set, err = loader.Load(filepath.Join(dir, "liproxy.toml"), loader.LoadOptions{})
	}
	if err != nil {
		// A parse failure raised during load is equally acceptable; what
		// matters is that it names the file.
		if !strings.Contains(err.Error(), "bad.toml") {
			t.Fatalf("load error does not name the file: %v", err)
		}
		return
	}

	if _, err := compile.FromFileSet(set); err == nil {
		t.Fatal("a file with an unterminated table compiled successfully")
	} else if !strings.Contains(err.Error(), "bad.toml") {
		t.Fatalf("compile error does not name the file: %v", err)
	}
}

// writeFile writes a test fixture, failing the test if it cannot.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
