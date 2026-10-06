// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package relay copies bytes between two connections.
//
// This version uses io.Copy and nothing else. Buffers, splice(2) and buffer
// pools are explicitly deferred: they change performance, not behaviour, and
// adding them before the surrounding engine is settled would mean tuning
// against a shape that is still moving (AGENTS.md 13 names them as the
// eventual target, not as a precondition).
//
// What this version does establish is the parts that are easy to get wrong and
// hard to notice:
//
//   - Bidirectional copy, so a protocol that only sends after receiving works.
//   - Half-close propagation. When one side finishes sending, the other side
//     must see EOF rather than waiting for a close that will not come. Getting
//     this wrong hangs protocols that signal end-of-request by closing their
//     write side, which includes most HTTP clients.
//   - Cancellation. When the context is done, both connections are closed so
//     that the copy goroutines unblock instead of leaking.
//
// The package imports no protocol implementation and no registry.
package relay
