// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package block implements the outbound that refuses connections.
//
// It exists so that a rule can terminate traffic deliberately. Without it, the
// only way to express "do not let this through" is to point the rule at an
// outbound that will fail for an unrelated reason, which produces a real
// connection attempt to a real destination — traffic the user asked to stop.
package block
