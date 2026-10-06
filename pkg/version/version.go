// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package version carries the build identity of a LiProxy binary.
//
// The three variables are deliberately package-level strings with usable
// defaults: a binary produced by a plain `go build` (no ldflags, which is what
// `go test` and `go run` do) must still report something coherent. The
// Makefile overrides them via -X at link time, so this file is the single
// source of truth for both the defaults and the -X symbol names.
package version

import (
	"fmt"
	"runtime"
)

// Build metadata, injected at link time with:
//
//	-X github.com/LiStudioorg/liproxy/pkg/version.Version=...
//	-X github.com/LiStudioorg/liproxy/pkg/version.Commit=...
//	-X github.com/LiStudioorg/liproxy/pkg/version.BuildTime=...
//
// The defaults describe an un-injected development build rather than being
// empty, so that `lip version` never prints a blank line.
var (
	// Version is the semantic version or git describe output.
	Version = "dev"
	// Commit is the short git commit hash the binary was built from.
	Commit = "none"
	// BuildTime is the UTC build timestamp in RFC 3339 format.
	BuildTime = "unknown"
)

// String renders the full build identity in one line, including the Go
// version this binary was compiled with. Used by both lip and lipd so the two
// binaries can never disagree about how a version is formatted.
func String() string {
	return fmt.Sprintf("LiProxy %s (commit %s, built %s, %s)",
		Version, Commit, BuildTime, runtime.Version())
}
