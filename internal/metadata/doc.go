// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package metadata defines the per-connection information passed from an
// inbound to an outbound.
//
// It lives in its own package rather than beside the inbound interface for one
// reason: both the inbound and outbound packages need to name it, and putting
// it in either one would force the other to depend on it. A shared leaf
// package keeps the dependency graph acyclic without introducing an import
// cycle that only shows up when a third protocol is added.
package metadata
