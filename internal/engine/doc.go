// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package engine assembles a compiled configuration into a running service.
//
// Its job is to turn an ir.Config into live listeners and to connect an
// accepted connection to an outbound: build the router, build every inbound
// and outbound through the registry, and serve each connection by routing it
// and relaying its bytes.
//
// The package imports no protocol implementation. Inbound and outbound types
// are created through internal/registry, and the protocol packages are
// imported once, by cmd/lipd, purely for their registration side effects. That
// separation is what makes a reduced build possible: dropping a protocol
// package from cmd/lipd removes it from the binary, with no change here
// (AGENTS.md 16, pitfall 2).
//
// It also implements the handler interface the HTTP inbound expects. The
// inbound owns its listener and its protocol parsing; anything involving
// routing or another outbound belongs here, so that a second inbound protocol
// does not have to reimplement it.
package engine
