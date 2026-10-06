// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog"
)

// watchDebounce is how long to wait after the last observed change before
// reloading.
//
// Editors do not write a file once. They write a temporary file, rename it
// over the target, and update the directory entry, and a single save can
// produce several events across several paths. Reloading on each one would
// compile a half-written configuration and, more confusingly, would log
// failures for a perfectly valid edit. Waiting for the burst to stop turns
// that into one reload of the finished file.
const watchDebounce = 500 * time.Millisecond

// watchedExtensions are the file types that can take part in a configuration.
//
// Filtering here rather than reloading on every event matters because the
// configuration directory sits next to state/ and backups/, which change for
// unrelated reasons. A database write or a backup rotation must not trigger a
// configuration reload.
var watchedExtensions = map[string]bool{
	".toml": true,
	".yaml": true,
	".yml":  true,
}

// watcher reloads the engine when the configuration files change.
type watcher struct {
	entry   string
	reload  func() error
	logger  *zerolog.Logger
	watcher *fsnotify.Watcher

	// dirs are the directories being watched, so an event can be attributed
	// to a path the configuration actually uses.
	dirs []string
}

// newWatcher starts watching the configuration directory tree that entry
// belongs to.
//
// It returns nil when watching is not possible. Losing the ability to notice
// an edit is a degraded mode, not a failure: the configuration already loaded
// is valid and the service works, so refusing to start would trade a working
// proxy for a missing convenience. The reason is logged so it is not silent.
func newWatcher(entry string, logger *zerolog.Logger, reload func() error) (*watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &watcher{
		entry:   entry,
		reload:  reload,
		logger:  logger,
		watcher: fsw,
	}

	// The directories to watch are the one holding the entry file plus the
	// fragment directories beside it. Watching the entry file itself is not
	// enough: an editor that replaces it by rename would leave the watch on a
	// deleted inode, and the change would never be seen again.
	root := filepath.Dir(entry)
	candidates := []string{root, filepath.Join(root, "toml.d"), filepath.Join(root, "yaml.d")}

	for _, dir := range candidates {
		if err := w.addDir(dir); err != nil {
			// Only the root is required; a fragment directory that does not
			// exist yet is normal.
			if dir == root {
				_ = fsw.Close()
				return nil, err
			}
		}
	}

	return w, nil
}

// addDir watches a directory, including any subdirectory already present.
//
// Registration is idempotent. The root tree is walked on startup, which
// already reaches toml.d/ and yaml.d/, and those are also requested by name so
// that a directory created after startup is watched too. Without the check the
// same path would be added twice and listed twice, which makes the startup log
// misrepresent what is being watched.
func (w *watcher) addDir(dir string) error {
	if w.watching(dir) {
		return nil
	}

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return err
	}
	// Fragment directories can nest, so every subdirectory is watched as well.
	// WalkDir reports the root first, which is the caller's own directory, so
	// the walk is what adds it — adding it here too would register the same
	// path twice and list it twice in the startup log.
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if err := w.watcher.Add(path); err != nil {
			if path == dir {
				// The directory the caller asked for could not be watched,
				// which is a real failure; a nested one is only a warning.
				return err
			}
			w.logger.Warn().Err(err).Str("path", path).Msg("cannot watch subdirectory")
			return nil
		}
		// A nested directory may already have been registered by an earlier
		// addDir call for its parent.
		if w.watching(path) {
			return nil
		}
		w.dirs = append(w.dirs, path)
		return nil
	})
}

// watching reports whether a directory is already registered.
func (w *watcher) watching(dir string) bool {
	for _, d := range w.dirs {
		if d == dir {
			return true
		}
	}
	return false
}

// Run watches until ctx is cancelled.
func (w *watcher) Run(ctx context.Context) {
	defer func() {
		if err := w.watcher.Close(); err != nil {
			w.logger.Warn().Err(err).Msg("closing the file watcher failed")
		}
	}()

	w.logger.Info().Strs("directories", w.dirs).Msg("watching configuration files")

	// The timer is created once and reset per burst. Creating one per event
	// would leak a timer for every file system event, which on a busy
	// directory is a steady allocation the runtime cannot reclaim promptly.
	timer := time.NewTimer(watchDebounce)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	pending := false
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if !w.relevant(event) {
				continue
			}

			// A newly created directory must be watched too, or a fragment
			// directory added after startup is invisible.
			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					if err := w.addDir(event.Name); err != nil {
						w.logger.Warn().Err(err).Str("path", event.Name).Msg("cannot watch a new directory")
					}
					continue
				}
			}

			w.logger.Debug().Str("path", event.Name).Str("op", event.Op.String()).Msg("configuration changed")
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(watchDebounce)
			pending = true

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			// A watch error is not fatal: the files are still on disk and the
			// running configuration is still valid. Widening the CLI's
			// dependency on the OS notification API into a proxy outage
			// would be a poor trade.
			w.logger.Warn().Err(err).Msg("file watcher error")

		case <-timer.C:
			if !pending {
				continue
			}
			pending = false
			w.logger.Info().Msg("configuration changed on disk, reloading")
			if err := w.reload(); err != nil {
				// The running configuration is untouched by a failed reload,
				// so this is reported and the service continues. The operator
				// still has a working proxy while they fix the file.
				w.logger.Error().Err(err).Msg("reload failed; the running configuration is unchanged")
			}
		}
	}
}

// relevant reports whether an event concerns a file the configuration can use.
func (w *watcher) relevant(event fsnotify.Event) bool {
	name := event.Name

	// A removed or renamed file still matters: a fragment that disappeared
	// changes the merged result.
	if event.Op&(fsnotify.Remove|fsnotify.Rename|fsnotify.Create|fsnotify.Write) == 0 {
		return false
	}

	base := filepath.Base(name)

	// Editors write these while saving, and they are not part of the
	// configuration. Reloading on them would compile a partial state.
	if strings.HasPrefix(base, ".") || strings.HasSuffix(base, "~") {
		return false
	}
	if strings.HasSuffix(base, ".swp") || strings.HasSuffix(base, ".tmp") {
		return false
	}

	// A directory event is relevant because it may be a new fragment
	// directory that has to be watched.
	if info, err := os.Stat(name); err == nil && info.IsDir() {
		return true
	}

	return watchedExtensions[filepath.Ext(base)]
}
