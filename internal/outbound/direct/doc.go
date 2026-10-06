// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package direct implements the outbound that connects without a proxy.
//
// It is the default destination for traffic that no rule claims, and it is the
// only outbound that needs no upstream credentials, so it is what the loopback
// tests in this repository use.
package direct
