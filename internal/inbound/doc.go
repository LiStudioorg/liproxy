// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package inbound defines the interface every inbound protocol implements.
//
// It contains the interface and nothing else. Implementations live in
// subpackages such as internal/inbound/http, and each registers itself with
// internal/registry from its own init function. Keeping the interface here,
// apart from the implementations, is what lets the engine name an inbound
// without importing a single protocol.
package inbound
