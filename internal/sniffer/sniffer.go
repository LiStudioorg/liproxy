// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package sniffer extracts the destination name from the first bytes of a
// connection.
//
// A connection arrives addressed to an address, and the name the client
// actually asked for is inside the payload: the SNI extension of a TLS
// ClientHello, or the Host header of a plain HTTP request. Without recovering
// it, every rule written against a domain is inert for any client that
// resolves names itself, which is most of them.
//
// The invariant is that sniffing never costs the payload. Whatever is read is
// handed back with the connection, so the relay forwards the original bytes
// and the peer sees an unmodified stream. A sniffer that consumed a ClientHello
// would break every TLS connection in a way that looks like a TLS fault.
//
// Sniffing is best-effort by design. A missed name means domain rules do not
// apply to that connection; a delayed connection is a visible fault. Failing
// open is the only acceptable direction, so every error path returns an empty
// result and the caller proceeds.
package sniffer

import (
	"bufio"
	"context"
	"errors"
	"net"
	"time"
)

// defaultTimeout bounds how long Sniff waits for the client's first bytes.
//
// It is short because the client is already waiting for a dial that has not
// started. A client that sends nothing is not going to be identified, and
// making it wait would delay every connection that is not immediately
// forthcoming.
const defaultTimeout = 300 * time.Millisecond

// maxSniffBytes caps how much of the stream is examined.
//
// A ClientHello carrying a large SNI plus extensions fits well inside this. The
// cap exists so that a peer cannot make the proxy buffer an unbounded amount
// of data before its first byte is forwarded.
const maxSniffBytes = 4096

// Protocol identifies what the sniffed bytes were.
type Protocol string

const (
	// ProtocolTLS means a TLS ClientHello was parsed.
	ProtocolTLS Protocol = "tls"
	// ProtocolHTTP means an HTTP request line and headers were parsed.
	ProtocolHTTP Protocol = "http"
)

// Result is what sniffing produced.
type Result struct {
	// Domain is the name the client asked for. It is empty when nothing could
	// be determined, which callers must treat as "no name available" rather
	// than as an error.
	Domain string

	// Protocol is how the name was found. It is empty when Domain is.
	Protocol Protocol
}

// Options configures a Sniffer.
type Options struct {
	// Timeout bounds how long Sniff waits for the client's first bytes. Zero
	// selects the default.
	Timeout time.Duration

	// Logger receives diagnostics. A nil value disables logging.
	//
	// It is deliberately the only logger this package accepts, rather than a
	// full zerolog.Logger, so the sniffer stays free of the logging
	// dependency that the hot path does not need.
	Logger DebugLogger
}

// DebugLogger receives sniff diagnostics.
//
// The interface is one method because sniffing runs once per connection: the
// cost of a general-purpose logger here is paid on every connection, and a
// narrow interface documents that only debug output is expected.
type DebugLogger interface {
	Debugf(format string, args ...any)
}

// Sniffer extracts destination names from connection preambles.
//
// A Sniffer holds no per-connection state, so one instance serves every
// connection and is safe for concurrent use.
type Sniffer struct {
	timeout time.Duration
	logger  DebugLogger
}

// New builds a Sniffer.
func New(opts Options) *Sniffer {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Sniffer{timeout: timeout, logger: opts.Logger}
}

// Sniff reads the start of a connection, extracts the destination name, and
// returns a connection that still yields every byte.
//
// The returned connection replaces the one passed in and must be used for all
// subsequent reads and writes. It may be a different value even when nothing
// was identified, because the bytes read during the attempt are buffered, and
// reading from the original would lose them.
func (s *Sniffer) Sniff(conn net.Conn) (net.Conn, Result, error) {
	if conn == nil {
		return nil, Result{}, errors.New("sniffer: nil connection")
	}

	// The deadline is set on the connection and cleared afterwards, because
	// the client's own timeout does not exist yet: it is waiting for a
	// response that cannot be produced until the dial this sniff precedes.
	if err := conn.SetReadDeadline(time.Now().Add(s.timeout)); err != nil {
		// A connection that cannot take a deadline is still usable; sniffing
		// without one risks blocking, so it is skipped rather than risked.
		s.debugf("cannot set a sniff deadline, skipping: %v", err)
		return conn, Result{}, nil
	}

	// Peek buffers without consuming: the bytes stay in the reader and are
	// handed to the relay through the returned connection. This is what makes
	// the read non-destructive, and it is why a bufio.Reader is used rather
	// than reading into a slice and prepending it.
	br := bufio.NewReaderSize(conn, maxSniffBytes)

	// Whatever happens below, the deadline is cleared and the buffered reader
	// is returned as the connection. Every exit path runs through here, so no
	// path can leave a deadline that would abort the tunnel.
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
	}()

	head, err := br.Peek(1)
	if err != nil {
		// The client sent nothing in time, or closed. Both are ordinary and
		// both mean "no name", not "failure".
		s.debugf("no preamble to sniff: %v", err)
		return &bufferedConn{Conn: conn, br: br}, Result{}, nil
	}

	var res Result
	switch {
	case isTLSHandshake(head[0]):
		res = s.sniffTLS(br)
	case looksLikeHTTPMethod(head[0]):
		res = s.sniffHTTP(br)
	}

	if res.Domain != "" {
		s.debugf("sniffed %s %q", res.Protocol, res.Domain)
	}
	return &bufferedConn{Conn: conn, br: br}, res, nil
}

// debugf reports diagnostics when a logger is configured.
func (s *Sniffer) debugf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Debugf(format, args...)
	}
}

// bufferedConn carries the bytes read during sniffing.
//
// Reads come from the buffered reader, which already holds the sniffed bytes,
// and writes go straight to the connection. This is the mechanism that makes
// sniffing non-destructive: the relay reads through this type and receives the
// ClientHello from the buffer instead of from a socket it has already been
// drained from.
type bufferedConn struct {
	net.Conn
	br *bufio.Reader
}

// Read returns buffered bytes first, then reads from the connection.
func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.br.Read(p)
}

// SniffInto sniffs a connection and returns the connection to use afterwards.
//
// It is the convenience form for callers that only want the name and will
// always use the returned connection, which is every caller in practice.
func (s *Sniffer) SniffInto(ctx context.Context, conn net.Conn) (net.Conn, Result) {
	if err := ctx.Err(); err != nil {
		// A cancelled request must not spend the sniff timeout before it is
		// abandoned.
		return conn, Result{}
	}
	out, res, err := s.Sniff(conn)
	if err != nil {
		s.debugf("sniff failed: %v", err)
		return conn, Result{}
	}
	if out == nil {
		return conn, Result{}
	}
	return out, res
}

// looksLikeHTTPMethod reports whether a byte could begin an HTTP method.
//
// It tests the first letter only, which is enough to decide whether the HTTP
// parser is worth running and costs one comparison per connection. The
// supported methods are the ones AGENTS.md names; a method outside that set is
// not sniffed, and the connection proceeds unnamed.
func looksLikeHTTPMethod(b byte) bool {
	switch b {
	case 'G', 'P', 'H', 'D', 'O', 'C':
		// GET POST PUT PATCH HEAD DELETE OPTIONS CONNECT
		return true
	default:
		return false
	}
}

// isTLSHandshake reports whether a byte begins a TLS record.
//
// 0x16 is the handshake content type. The version is not checked here because
// the caller has one byte; the parser validates it, and rejecting a record
// early would only save the cost of a parse that is already bounded.
func isTLSHandshake(b byte) bool {
	return b == 0x16
}
