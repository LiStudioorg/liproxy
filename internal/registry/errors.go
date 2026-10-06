// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package registry

import "errors"

// ErrUnknownType reports a configuration naming a protocol type that no
// registered factory provides.
//
// It is a sentinel so that a caller can distinguish "this build does not
// include that protocol" from "the protocol was configured badly". The first
// is a build question answered by importing the right package; the second is a
// configuration question. Reporting them identically sends the user looking in
// the wrong place.
var ErrUnknownType = errors.New("unknown protocol type")
