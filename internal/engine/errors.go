// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import "github.com/LiStudioorg/liproxy/internal/outbound/outbounderr"

// ErrBlocked re-exports the shared refusal sentinel.
//
// The value comes from a leaf package that both the engine and the outbounds
// import, rather than being declared here. The engine is forbidden from
// importing a concrete outbound, so a sentinel declared locally could never
// match what an outbound actually returned — the check would compile and
// silently never fire.
var ErrBlocked = outbounderr.ErrBlocked
