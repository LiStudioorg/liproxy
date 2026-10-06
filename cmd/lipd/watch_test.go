// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog"
)

// testLogger returns a logger that discards output.
func testLogger() *zerolog.Logger {
	l := zerolog.Nop()
	return &l
}

// writeFile writes a file, failing the test on error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestWatcherReloadsOnChange verifies that editing a configuration file
// triggers a reload.
func TestWatcherReloadsOnChange(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "liproxy.toml")
	writeFile(t, entry, "mode = \"client\"\n")

	var calls atomic.Int64
	w, err := newWatcher(entry, testLogger(), func() error {
		calls.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("newWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	writeFile(t, entry, "mode = \"server\"\n")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("editing the configuration did not trigger a reload")
}

// TestWatcherDebouncesABurst verifies that one save produces one reload.
//
// Editors write several files in quick succession — a temporary file, then a
// rename — and a single save can produce a handful of events. Reloading per
// event would compile a half-written configuration and log failures for an
// edit that was fine.
func TestWatcherDebouncesABurst(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "liproxy.toml")
	writeFile(t, entry, "mode = \"client\"\n")

	var calls atomic.Int64
	w, err := newWatcher(entry, testLogger(), func() error {
		calls.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("newWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// A burst well inside the debounce window.
	for i := 0; i < 8; i++ {
		writeFile(t, entry, "mode = \"client\"\n")
		time.Sleep(10 * time.Millisecond)
	}

	// Wait past the debounce, then check that the burst collapsed.
	time.Sleep(watchDebounce + 500*time.Millisecond)

	if got := calls.Load(); got != 1 {
		t.Fatalf("a burst of writes produced %d reloads, want 1", got)
	}
}

// TestWatcherIgnoresUnrelatedFiles verifies that files the configuration
// cannot use do not trigger a reload.
//
// The configuration directory sits beside state/ and backups/, which change
// for reasons unrelated to configuration. Reloading on those would compile the
// same configuration repeatedly and, worse, would make a database write appear
// to be a configuration change.
func TestWatcherIgnoresUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "liproxy.toml")
	writeFile(t, entry, "mode = \"client\"\n")

	var calls atomic.Int64
	w, err := newWatcher(entry, testLogger(), func() error {
		calls.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("newWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	unrelated := []string{
		"li.db",
		"li.db-wal",
		"config.toml.bak",
		"README.md",
		".liproxy.toml.swp",
		"liproxy.toml~",
		"notes.txt",
	}
	for _, name := range unrelated {
		writeFile(t, filepath.Join(dir, name), "not configuration\n")
	}

	time.Sleep(watchDebounce + 600*time.Millisecond)

	if got := calls.Load(); got != 0 {
		t.Fatalf("%d reloads were triggered by unrelated files", got)
	}
}

// TestWatcherSeesFragmentDirectories verifies that a change to a fragment
// under toml.d/ triggers a reload.
//
// This is where real configurations live: the main file is small and the
// fragments carry the content, so a watcher that only followed the entry file
// would miss almost every edit.
func TestWatcherSeesFragmentDirectories(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "liproxy.toml")
	writeFile(t, entry, "include = [\"toml.d/*.toml\"]\n")

	frag := filepath.Join(dir, "toml.d")
	if err := os.MkdirAll(frag, 0o755); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int64
	w, err := newWatcher(entry, testLogger(), func() error {
		calls.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("newWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	writeFile(t, filepath.Join(frag, "010-proxy.toml"), "mode = \"client\"\n")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a change in toml.d/ did not trigger a reload")
}

// TestWatcherPicksUpNewFragmentDirectory verifies that a fragment directory
// created after startup is watched.
//
// A configuration that starts with no toml.d/ and gains one when the user
// splits their file would otherwise stop noticing edits for the rest of the
// process's life, which is invisible until someone wonders why their change
// had no effect.
func TestWatcherPicksUpNewFragmentDirectory(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "liproxy.toml")
	writeFile(t, entry, "include = [\"toml.d/*.toml\"]\n")

	var calls atomic.Int64
	w, err := newWatcher(entry, testLogger(), func() error {
		calls.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("newWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	frag := filepath.Join(dir, "toml.d")
	if err := os.MkdirAll(frag, 0o755); err != nil {
		t.Fatal(err)
	}
	// Creating the directory itself is a change worth reloading for, since the
	// include pattern's result changes once files appear in it.
	time.Sleep(watchDebounce + 300*time.Millisecond)

	before := calls.Load()
	writeFile(t, filepath.Join(frag, "010-proxy.toml"), "mode = \"client\"\n")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() > before {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a file in a directory created after startup did not trigger a reload")
}

// TestWatcherReloadFailureDoesNotStopWatching verifies that a failed reload
// leaves the watcher running so a subsequent fix is noticed.
//
// The service keeps serving on the old configuration, so the next edit must
// still be picked up — otherwise a single bad save would leave the operator
// editing a file that nothing reads.
func TestWatcherReloadFailureDoesNotStopWatching(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "liproxy.toml")
	writeFile(t, entry, "mode = \"client\"\n")

	var calls atomic.Int64
	var failFirst sync.Once
	w, err := newWatcher(entry, testLogger(), func() error {
		n := calls.Add(1)
		if n == 1 {
			failFirst.Do(func() {})
			return os.ErrInvalid
		}
		return nil
	})
	if err != nil {
		t.Fatalf("newWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	writeFile(t, entry, "mode = \"server\"\n")
	time.Sleep(watchDebounce + 300*time.Millisecond)

	// A second edit after the failure must still be seen.
	writeFile(t, entry, "mode = \"client\"\n")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the watcher stopped after a failed reload (%d reloads seen)", calls.Load())
}

// TestRelevantFiltersEvents covers the event filter directly, since it decides
// what counts as a configuration change.
func TestRelevantFiltersEvents(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "liproxy.toml")
	writeFile(t, entry, "mode = \"client\"\n")

	w := &watcher{entry: entry, logger: testLogger()}

	// A subdirectory has to be created for the directory branch to be tested.
	sub := filepath.Join(dir, "toml.d")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		op   fsnotify.Op
		want bool
	}{
		{"toml file", filepath.Join(dir, "liproxy.toml"), fsnotify.Write, true},
		{"yaml file", filepath.Join(dir, "a.yaml"), fsnotify.Write, true},
		{"yml file", filepath.Join(dir, "a.yml"), fsnotify.Write, true},
		{"toml fragment", filepath.Join(dir, "toml.d", "010.toml"), fsnotify.Create, true},
		{"directory", sub, fsnotify.Create, true},
		{"database", filepath.Join(dir, "li.db"), fsnotify.Write, false},
		{"text file", filepath.Join(dir, "notes.txt"), fsnotify.Write, false},
		{"editor swap file", filepath.Join(dir, ".liproxy.toml.swp"), fsnotify.Write, false},
		{"editor backup", filepath.Join(dir, "liproxy.toml~"), fsnotify.Write, false},
		{"hidden file", filepath.Join(dir, ".hidden"), fsnotify.Write, false},

		// A chmod says nothing about the content, so it must not trigger a
		// compile of an unchanged file.
		{"permission change", filepath.Join(dir, "liproxy.toml"), fsnotify.Chmod, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := fsnotify.Event{Name: c.path, Op: c.op}
			if got := w.relevant(ev); got != c.want {
				t.Errorf("relevant(%s, %s) = %v, want %v", filepath.Base(c.path), c.op, got, c.want)
			}
		})
	}
}
