// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Command lipd is the LiProxy daemon: it loads the configuration, compiles it
// to IR and runs the proxy engine.
//
// Scope note: the API server, database and embedded Web UI are not wired up
// yet. What runs today is the proxy path end to end: configuration files in,
// live listeners out. --check validates what it can, which now includes
// compilation rather than only file presence.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/rs/zerolog"

	"github.com/LiStudioorg/liproxy/internal/config/compile"
	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/config/loader"
	"github.com/LiStudioorg/liproxy/internal/engine"
	"github.com/LiStudioorg/liproxy/pkg/log"
	"github.com/LiStudioorg/liproxy/pkg/version"

	// Protocol registrations. These imports have side effects only, and they
	// live here rather than in the engine so that building a reduced binary is
	// a matter of removing a line from this file. The engine, router and relay
	// must never import a protocol package (AGENTS.md 16, pitfall 2).
	_ "github.com/LiStudioorg/liproxy/internal/registry/all"
)

// defaultConfigPath is where lipd looks for liproxy.toml when --config is
// omitted.
const defaultConfigPath = "~/.li/liproxy/liproxy.toml"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without the process-level concerns, so flag handling and exit
// codes can be exercised by tests.
func run(args []string, stdout, stderr *os.File) int {
	return runContext(context.Background(), args, stdout, stderr)
}

// runContext is run with an injectable parent context, so a test can cancel it
// instead of signalling the whole test process.
func runContext(parent context.Context, args []string, stdout, stderr *os.File) int {
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

	// The loader's non-fatal diagnostics go to the process logger, so a
	// wildcard that matched nothing or an include filed under a table is
	// visible without failing the load.
	loadOpts := loader.LoadOptions{Logger: logger}

	set, err := loader.Load(resolved, loadOpts)
	if err != nil {
		logger.Error().Err(err).Str("path", resolved).Msg("failed to load configuration")
		return 1
	}

	cfg, err := compile.FromFileSet(set)
	if err != nil {
		logger.Error().Err(err).Str("path", resolved).Msg("failed to compile configuration")
		return 1
	}

	if *check {
		logger.Info().
			Str("path", resolved).
			Int("files", len(set.Files)).
			Int("inbounds", len(cfg.Inbounds)).
			Int("outbounds", len(cfg.Outbounds)).
			Int("rules", len(cfg.Rules)).
			Msg("config check passed")
		return 0
	}

	return serve(parent, cfg, logger, stderr)
}

// serve runs the engine until a signal arrives.
//
// The signal context is created here rather than in run so that a test can
// drive run to completion without signalling the whole test process.
func serve(parent context.Context, cfg *ir.Config, logger *zerolog.Logger, stderr *os.File) int {
	eng, err := engine.New(cfg, engine.Options{Logger: logger})
	if err != nil {
		logger.Error().Err(err).Msg("failed to build the engine")
		return 1
	}

	ctx, stop := signalContext(parent)
	defer stop()

	logger.Info().Msg("starting")
	if err := eng.Start(ctx); err != nil {
		logger.Error().Err(err).Msg("engine stopped with an error")
		return 1
	}
	logger.Info().Msg("stopped")
	return 0
}

// checkConfig is retained for callers that only need the file probe.
func checkConfig(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("config not found: %w", err)
		}
		return fmt.Errorf("cannot access config: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("config path is a directory, not a file: %s", path)
	}
	if info.Size() == 0 {
		return fmt.Errorf("config is empty: %s", path)
	}
	return nil
}

// expandPath resolves a leading ~ against the user's home directory.
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

// signalContext returns a context cancelled by SIGINT or SIGTERM.
func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
