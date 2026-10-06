// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package router

import "errors"

// ErrNoMatch reports that no rule matched and there is no final rule.
//
// The connection is not silently sent to a default. A configuration that does
// not say where unmatched traffic goes has not decided, and guessing on the
// user's behalf can route traffic through an outbound they did not intend —
// which, for a proxy, can mean leaking traffic they expected to be tunnelled.
var ErrNoMatch = errors.New("no rule matched and no final rule is configured")

// ErrUnsupportedMatcher reports a rule type this version cannot evaluate.
//
// It is reported when the router is built rather than per connection, because
// a rule the router cannot evaluate is a configuration error that does not
// depend on traffic.
var ErrUnsupportedMatcher = errors.New("unsupported rule matcher")
