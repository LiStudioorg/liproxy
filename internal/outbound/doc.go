// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package outbound defines the interface every outbound protocol implements.
//
// It contains the interface and nothing else. Implementations live in
// subpackages such as internal/outbound/direct and internal/outbound/block,
// and each registers itself with internal/registry from its own init
// function.
package outbound
