// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package sniffer

import (
	"bufio"
	"bytes"
	"strings"
)

// httpMethods are the request methods this parser recognizes.
//
// The set is bounded deliberately: the first line of the connection is
// attacker controlled, and matching against a fixed list is what keeps an
// arbitrary byte stream from being parsed as HTTP.
var httpMethods = [][]byte{
	[]byte("GET "),
	[]byte("POST "),
	[]byte("PUT "),
	[]byte("PATCH "),
	[]byte("HEAD "),
	[]byte("DELETE "),
	[]byte("OPTIONS "),
	[]byte("CONNECT "),
}

// maxHTTPHeadBytes bounds how much of an HTTP preamble is read.
//
// Request lines and headers are small, and the cap prevents a peer that sends
// an endless header block from making the proxy buffer without limit.
const maxHTTPHeadBytes = 2048

// sniffHTTP extracts the Host header from an HTTP request.
//
// Only the head is examined. A request whose headers have not fully arrived is
// not waited for: the name is a routing hint, and delaying the connection for
// one would cost more than the hint is worth.
func (s *Sniffer) sniffHTTP(br *bufio.Reader) Result {
	head, err := peekHead(br)
	if err != nil || len(head) == 0 {
		return Result{}
	}

	// The method check is what keeps an arbitrary byte stream from being
	// parsed as HTTP. It is redundant with the parsing below in the sense that
	// a stream without a Host line yields nothing anyway, but the redundancy
	// is deliberate: it bounds the set of inputs the header walk will accept,
	// so a future change to header handling cannot accidentally make a
	// non-HTTP stream routable.
	//
	// It is also not free to remove: the walk below scans every line, and
	// rejecting up front avoids that work for the overwhelming majority of
	// connections that are TLS.
	if !hasHTTPMethod(head) {
		return Result{}
	}

	for _, line := range bytes.Split(head, []byte("\r\n")) {
		if len(line) == 0 {
			// The blank line ends the head, so no Host header follows.
			break
		}
		if domain := hostFromLine(line); domain != "" {
			return Result{Domain: domain, Protocol: ProtocolHTTP}
		}
	}
	return Result{}
}

// httpRequestHead is the separator that ends a request head.
var httpRequestHead = []byte("\r\n\r\n")

// peekHead reads up to the end of the request head without consuming it.
//
// It stops at the terminating blank line, at the cap, or when there is nothing
// more buffered. The last case is not an error: the caller receives what has
// arrived and decides whether it is enough.
//
// The buffered bytes are examined before any read is attempted. A read is only
// worth making when the head is not already complete, because a read on a
// connection whose peer is waiting for a response blocks until the sniff
// deadline — which would delay every HTTP request by the full timeout even
// though the header needed was sitting in the buffer the whole time.
func peekHead(br *bufio.Reader) ([]byte, error) {
	if peeked, err := br.Peek(br.Buffered()); err == nil || len(peeked) > 0 {
		if idx := bytes.Index(peeked, httpRequestHead); idx >= 0 {
			return peeked[:idx], nil
		}
	}

	// The head is incomplete in the buffer. Ask for more, stopping as soon as
	// the terminator arrives or the cap is reached.
	for buffered := br.Buffered(); buffered < maxHTTPHeadBytes; {
		peeked, err := br.Peek(buffered + 1)
		if err != nil {
			return peeked, err
		}
		buffered = len(peeked)
		if idx := bytes.Index(peeked, httpRequestHead); idx >= 0 {
			return peeked[:idx], nil
		}
	}

	peeked, _ := br.Peek(br.Buffered())
	return peeked, nil
}

// hasHTTPMethod reports whether data starts with a recognized request method.
func hasHTTPMethod(data []byte) bool {
	for _, m := range httpMethods {
		if bytes.HasPrefix(data, m) {
			return true
		}
	}
	return false
}

// hostFromLine returns the value of a Host header, or empty.
//
// The name is compared case-insensitively because header names are, and the
// value is taken up to the first colon: a Host header may carry a port, and the
// routing decision is about the name.
func hostFromLine(line []byte) string {
	const prefix = "host:"
	if len(line) < len(prefix) {
		return ""
	}
	if !strings.EqualFold(string(line[:len(prefix)]), prefix) {
		return ""
	}

	value := strings.TrimSpace(string(line[len(prefix):]))
	if value == "" {
		return ""
	}
	if i := strings.LastIndexByte(value, ':'); i >= 0 {
		// An IPv6 literal keeps its brackets, and its port follows the closing
		// bracket, so the colon inside the address is not a separator.
		if !strings.Contains(value, "]") || strings.LastIndexByte(value, ']') < i {
			value = value[:i]
		}
	}
	return sanitizeDomain(value)
}

// sanitizeDomain cleans a name taken from the wire.
//
// The value is attacker controlled and becomes a routing input, so anything
// that is not a plausible host name is rejected outright. Rejecting is safe:
// an unnamed connection is routed by its address, while a name containing
// control characters or separators could match a rule the client never asked
// for.
func sanitizeDomain(s string) string {
	if s == "" || len(s) > 253 {
		return ""
	}

	// A trailing dot is the fully qualified form of the same name.
	s = strings.TrimSuffix(s, ".")

	// An empty label, a leading or trailing dot, or a doubled dot means the
	// value is malformed rather than merely unusual.
	if s == "" || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") {
		return ""
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '.' || c == '_':
		case c == ':' || c == '[' || c == ']':
			// An address literal. It carries no name for domain rules to
			// match, so it is rejected here rather than downstream.
			return ""
		default:
			return ""
		}
	}

	// A bare address is not a name. Passing it on would let domain rules match
	// against address text, which the router explicitly guards against.
	if isNumericOnly(s) {
		return ""
	}

	// Lowercased so that a rule written in either case matches. The rules are
	// matched case-insensitively anyway, but a normalized value keeps the
	// logged and compared forms identical.
	return strings.ToLower(s)
}

// isNumericOnly reports whether a string is a dotted-decimal address.
func isNumericOnly(s string) bool {
	digits := 0
	dots := 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] >= '0' && s[i] <= '9':
			digits++
		case s[i] == '.':
			dots++
		default:
			return false
		}
	}
	return digits > 0 && dots > 0
}
