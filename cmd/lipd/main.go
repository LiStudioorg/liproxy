// LiProxy - a web-driven universal proxy platform.
// Copyright (C) 2026 LiProxy Contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

// Command lipd is the LiProxy daemon: it loads the bootstrap configuration,
// starts the REST/WebSocket API and the proxy engine, and serves the embedded
// Web UI.
//
// Scope note: at this stage lipd only resolves its flags, reports its build
// identity and (with --check) verifies that the bootstrap file exists. The API
// server, database and engine are not wired up yet, so --check is a
// file-presence probe and not yet the full validation described in
// AGENTS.md section 16. It is deliberately not documented in the README until
// it validates what the README claims.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/LiStudioorg/liproxy/pkg/log"
	"github.com/LiStudioorg/liproxy/pkg/version"
)

// defaultConfigPath is where lipd looks for liproxy.toml when --config is
// omitted. It mirrors database.path in the README field table.
const defaultConfigPath = "~/.li/liproxy/liproxy.toml"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without the process-level concerns, so the flag handling and
// the --check exit codes can be exercised by tests.
func run(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("lipd", flag.ContinueOnError)
	fs.SetOutput(stderr)

	configPath := fs.String("config", defaultConfigPath, "path to liproxy.toml")
	check := fs.Bool("check", false, "validate the configuration and exit")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, error")

	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: lipd [flags]\n\n")
		fmt.Fprintf(stderr, "LiProxy daemon.\n\n")
		fmt.Fprintf(stderr, "Flags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		// flag already printed the error and usage.
		return 2
	}

	logger, err := log.Setup(stdout, *logLevel)
	if err != nil {
		fmt.Fprintf(stderr, "lipd: %v\n", err)
		return 2
	}

	logger.Info().Msg(version.String())

	resolved, err := expandPath(*configPath)
	if err != nil {
		logger.Error().Err(err).Msg("failed to resolve config path")
		return 1
	}

	if *check {
		if err := checkConfig(resolved); err != nil {
			logger.Error().Err(err).Str("path", resolved).Msg("config check failed")
			return 1
		}
		logger.Info().Str("path", resolved).Msg("config check passed")
		return 0
	}

	// The daemon proper (bootstrap, database, API, engine) lands in later
	// units. Refusing to start is the honest behaviour: silently exiting 0
	// would look like a successful run to any supervisor.
	logger.Error().
		Str("path", resolved).
		Msg("lipd cannot start yet: bootstrap, database, API and engine are not implemented")
	return 1
}

// checkConfig verifies that the bootstrap file is present and readable. It
// does not parse the TOML yet; that arrives with internal/bootstrap.
func checkConfig(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("bootstrap config not found: %w", err)
		}
		return fmt.Errorf("cannot access bootstrap config: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("bootstrap config path is a directory, not a file: %s", path)
	}
	if info.Mode().Perm()&0o400 == 0 {
		return fmt.Errorf("bootstrap config is not readable by the current user: %s", path)
	}
	// A zero-byte file passes the existence probe but can never carry the ten
	// bootstrap fields, so reject it here rather than at parse time.
	if info.Size() == 0 {
		return fmt.Errorf("bootstrap config is empty: %s", path)
	}
	return nil
}

// expandPath resolves a leading ~ against the user's home directory. Written
// here rather than pulled from a dependency because the same expansion will be
// needed by the bootstrap parser in the next unit and it is a few lines.
func expandPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("config path is empty")
	}
	if path != "~" && !hasTildePrefix(path) {
		return filepath.Clean(path), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// hasTildePrefix reports whether path starts with "~/" or `~\`.
func hasTildePrefix(path string) bool {
	if len(path) < 2 || path[0] != '~' {
		return false
	}
	return path[1] == '/' || path[1] == filepath.Separator
}
