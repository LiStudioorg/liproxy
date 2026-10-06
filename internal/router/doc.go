// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package router selects an outbound for a connection by matching rules.
//
// This is the first version and it deliberately does very little: three
// matchers, linear scan, no compiled data structure. What it establishes is
// the contract and the failure behaviour.
//
// The contract is that Route is total — it always returns a tag or an error,
// never "no opinion" — because a connection that reaches the router has to go
// somewhere, and silently dropping it is indistinguishable from a hang.
//
// The failure behaviour is that unsupported rule types are rejected when the
// router is built, not when a connection arrives. A rule type this version
// cannot evaluate is a configuration problem, and a configuration problem must
// be visible at startup; discovering it on the first matching connection means
// the service has already been accepting traffic while being unable to route
// part of it.
//
// Matching is a linear scan over the rule list. A trie or Aho-Corasick index
// is the right eventual structure and is explicitly out of scope here: it
// changes none of the contracts above, and building it before the rule set is
// settled would mean rewriting it.
//
// The package imports no protocol implementation, only the registry, which is
// what keeps a reduced build possible (AGENTS.md 16, pitfall 2).
package router
