// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package http implements an HTTP proxy inbound.
//
// It handles both forms an HTTP proxy must support:
//
//   - CONNECT, used by clients for HTTPS and for anything that is not HTTP.
//     The connection is hijacked and handed to the relay as a raw stream.
//   - Ordinary requests with an absolute URI, the older proxy form still used
//     for plain HTTP. The request is forwarded and the response returned.
//
// The package is deliberately built on net/http rather than on a hand-written
// request parser. A proxy request parser has to handle chunked bodies,
// Content-Length, request smuggling defenses, HTTP/1.1 framing and header
// limits, and every one of those is a place to introduce a vulnerability. The
// standard library already does it correctly.
package http
