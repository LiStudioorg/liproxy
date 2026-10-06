// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package sniffer

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// --- building a real ClientHello ------------------------------------------

// clientHello builds a TLS ClientHello record carrying an SNI.
//
// The bytes are assembled field by field rather than copied from a capture, so
// the fixture stays readable and a field that shifts shows up as a failing
// test instead of a mysterious one. The layout is the one RFC 8446 and
// RFC 6066 define.
func clientHello(serverName string) []byte {
	// Extensions: the SNI one and a padding extension, so the parser has to
	// walk past something to reach it in the general case.
	sni := serverNameExtension(serverName)
	padding := extension(0x0015, make([]byte, 8))

	var exts bytes.Buffer
	exts.Write(sni)
	exts.Write(padding)

	var body bytes.Buffer
	body.Write([]byte{0x03, 0x03})             // legacy_version
	body.Write(make([]byte, 32))               // random
	body.WriteByte(0x00)                       // session_id length
	body.Write([]byte{0x00, 0x02, 0x13, 0x01}) // cipher_suites: TLS_AES_128_GCM_SHA256
	body.Write([]byte{0x01, 0x00})             // compression_methods: null
	body.Write(u16(len(exts.Bytes())))         // extensions length
	body.Write(exts.Bytes())

	handshake := make([]byte, 0, 4+body.Len())
	handshake = append(handshake, 0x01) // ClientHello
	handshake = appendU24(handshake, body.Len())
	handshake = append(handshake, body.Bytes()...)

	record := make([]byte, 0, 5+len(handshake))
	record = append(record, 0x16, 0x03, 0x01)
	record = append(record, u16(len(handshake))...)
	record = append(record, handshake...)
	return record
}

// serverNameExtension builds the SNI extension for a host name.
func serverNameExtension(name string) []byte {
	entry := make([]byte, 0, 3+len(name))
	entry = append(entry, 0x00) // name_type: host_name
	entry = append(entry, u16(len(name))...)
	entry = append(entry, name...)

	list := append(u16(len(entry)), entry...)
	return extension(0x0000, list)
}

// extension wraps a payload in an extension header.
func extension(typ uint16, payload []byte) []byte {
	out := make([]byte, 0, 4+len(payload))
	out = append(out, u16(int(typ))...)
	out = append(out, u16(len(payload))...)
	out = append(out, payload...)
	return out
}

// u16 encodes a value as two bytes.
func u16(v int) []byte {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], uint16(v))
	return b[:]
}

// appendU24 appends a value as three bytes.
func appendU24(dst []byte, v int) []byte {
	return append(dst, byte(v>>16), byte(v>>8), byte(v))
}

// record wraps a handshake message in a TLS record header.
func record(body []byte) []byte {
	out := make([]byte, 0, 5+len(body))
	out = append(out, 0x16, 0x03, 0x01)
	out = append(out, u16(len(body))...)
	return append(out, body...)
}

// --- helpers --------------------------------------------------------------

// sniffBytes runs the sniffer over an in-memory connection carrying data.
func sniffBytes(t *testing.T, data []byte) (net.Conn, Result) {
	t.Helper()

	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	go func() {
		_, _ = client.Write(data)
	}()

	// A short timeout keeps the malformed-input cases fast: those return no
	// name, and the sniffer still waits for bytes that the truncated input
	// never supplies.
	s := New(Options{Timeout: 150 * time.Millisecond})
	conn, res, err := s.Sniff(server)
	if err != nil {
		t.Fatalf("Sniff: %v", err)
	}
	return conn, res
}

// --- TLS ------------------------------------------------------------------

// TestSniffTLSClientHelloSNI verifies the core case: a ClientHello's SNI is
// extracted.
func TestSniffTLSClientHelloSNI(t *testing.T) {
	cases := []string{
		"example.com",
		"a.b.c.example.com",
		"xn--fiqs8s.example",
		"very-long-label-that-is-still-legal.example.com",
	}

	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			_, res := sniffBytes(t, clientHello(name))
			if res.Domain != name {
				t.Fatalf("Domain = %q, want %q", res.Domain, name)
			}
			if res.Protocol != ProtocolTLS {
				t.Fatalf("Protocol = %q, want %q", res.Protocol, ProtocolTLS)
			}
		})
	}
}

// TestSniffTLSIsCaseInsensitive verifies that the name is normalized.
func TestSniffTLSIsCaseInsensitive(t *testing.T) {
	_, res := sniffBytes(t, clientHello("Example.COM"))
	if res.Domain != "example.com" {
		t.Fatalf("Domain = %q, want example.com", res.Domain)
	}
}

// TestSniffTLSWithoutSNI verifies that a ClientHello with no SNI yields no
// name rather than an error.
//
// A client connecting to an address sends one of these, and it is a valid
// handshake that must reach the destination.
func TestSniffTLSWithoutSNI(t *testing.T) {
	var body bytes.Buffer
	body.Write([]byte{0x03, 0x03})
	body.Write(make([]byte, 32))
	body.WriteByte(0x00)
	body.Write([]byte{0x00, 0x02, 0x13, 0x01})
	body.Write([]byte{0x01, 0x00})
	body.Write(u16(0)) // no extensions

	handshake := append([]byte{0x01}, appendU24(nil, body.Len())...)
	handshake = append(handshake, body.Bytes()...)

	_, res := sniffBytes(t, record(handshake))
	if res.Domain != "" {
		t.Fatalf("Domain = %q, want empty", res.Domain)
	}
}

// TestSniffTLSSkipsToALaterExtension verifies that the parser walks the
// extension list rather than assuming SNI comes first.
//
// Extension order is not fixed, so a parser that read only the first entry
// would work against one client and silently fail against another.
func TestSniffTLSSkipsToALaterExtension(t *testing.T) {
	// SNI deliberately placed after two unrelated extensions.
	var exts bytes.Buffer
	exts.Write(extension(0x002b, []byte{0x03, 0x04}))             // supported_versions
	exts.Write(extension(0x000a, []byte{0x00, 0x02, 0x00, 0x1d})) // groups
	exts.Write(serverNameExtension("late.example"))

	var body bytes.Buffer
	body.Write([]byte{0x03, 0x03})
	body.Write(make([]byte, 32))
	body.WriteByte(0x00)
	body.Write([]byte{0x00, 0x02, 0x13, 0x01})
	body.Write([]byte{0x01, 0x00})
	body.Write(u16(exts.Len()))
	body.Write(exts.Bytes())

	handshake := append([]byte{0x01}, appendU24(nil, body.Len())...)
	handshake = append(handshake, body.Bytes()...)

	_, res := sniffBytes(t, record(handshake))
	if res.Domain != "late.example" {
		t.Fatalf("Domain = %q, want late.example", res.Domain)
	}
}

// TestSniffTLSNegativeCaseIsNotReadAsSNI verifies that an extension situated
// after the SNI is not mistaken for part of it.
//
// The SNI parse must be bounded by the extension's declared length. A parser
// that passed the remainder of the list would let a later extension's bytes be
// read as the name, so a client could choose which domain the rules see by
// placing a crafted extension after its SNI.
func TestSniffTLSNegativeCaseIsNotReadAsSNI(t *testing.T) {
	var exts bytes.Buffer
	exts.Write(serverNameExtension("real.example"))
	// An extension whose payload looks exactly like another ServerNameList.
	// If the declared length were ignored, this would be reachable as the SNI
	// value on a second pass or bleed into the first parse.
	decoy := serverNameExtension("decoy.example")
	exts.Write(extension(0x0010, decoy))

	var body bytes.Buffer
	body.Write([]byte{0x03, 0x03})
	body.Write(make([]byte, 32))
	body.WriteByte(0x00)
	body.Write([]byte{0x00, 0x02, 0x13, 0x01})
	body.Write([]byte{0x01, 0x00})
	body.Write(u16(exts.Len()))
	body.Write(exts.Bytes())

	handshake := append([]byte{0x01}, appendU24(nil, body.Len())...)
	handshake = append(handshake, body.Bytes()...)

	_, res := sniffBytes(t, record(handshake))
	if res.Domain != "real.example" {
		t.Fatalf("Domain = %q, want real.example; a trailing extension leaked into the SNI parse", res.Domain)
	}
}

// TestSniffTLSRejectsMalformedLengths verifies that a truncated or overlong
// length field ends the parse without reading past the buffer.
//
// The lengths are attacker controlled, so a parser that trusted them would
// panic or read adjacent memory in a real implementation.
func TestSniffTLSRejectsMalformedLengths(t *testing.T) {
	valid := clientHello("example.com")

	cases := []struct {
		name string
		data []byte
	}{
		{"empty record", []byte{}},
		{"header only", []byte{0x16, 0x03, 0x01, 0x00, 0x10}},
		{"record length beyond data", func() []byte {
			b := append([]byte(nil), valid...)
			binary.BigEndian.PutUint16(b[3:5], 4000)
			return b
		}()},
		{"handshake length beyond record", func() []byte {
			b := append([]byte(nil), valid...)
			b[6], b[7], b[8] = 0xff, 0xff, 0xff
			return b
		}()},
		{"not a handshake record", []byte{0x17, 0x03, 0x01, 0x00, 0x05, 0x01, 0x02, 0x03, 0x04, 0x05}},
		{"handshake is not ClientHello", func() []byte {
			b := append([]byte(nil), valid...)
			b[5] = 0x02 // ServerHello
			return b
		}()},
		{"zero record length", []byte{0x16, 0x03, 0x01, 0x00, 0x00}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, res := sniffBytes(t, c.data)
			// The only requirement is that nothing panicked and no name was
			// invented. An empty result means the connection proceeds
			// unnamed, which is the correct fallback.
			if res.Domain != "" {
				t.Fatalf("Domain = %q, want empty for malformed input", res.Domain)
			}
		})
	}
}

// --- HTTP -----------------------------------------------------------------

// TestSniffHTTPHost verifies that a plain HTTP request's Host header is read.
func TestSniffHTTPHost(t *testing.T) {
	cases := []struct {
		name string
		req  string
		want string
	}{
		{"GET", "GET /index.html HTTP/1.1\r\nHost: example.com\r\n\r\n", "example.com"},
		{"POST", "POST /submit HTTP/1.1\r\nHost: api.example.com\r\n\r\n", "api.example.com"},
		{"HEAD", "HEAD / HTTP/1.1\r\nHost: head.example\r\n\r\n", "head.example"},
		{"PUT", "PUT /x HTTP/1.1\r\nHost: put.example\r\n\r\n", "put.example"},
		{"DELETE", "DELETE /x HTTP/1.1\r\nHost: del.example\r\n\r\n", "del.example"},
		{"OPTIONS", "OPTIONS * HTTP/1.1\r\nHost: opt.example\r\n\r\n", "opt.example"},
		{"with port", "GET / HTTP/1.1\r\nHost: example.com:8080\r\n\r\n", "example.com"},
		{"lowercase header", "GET / HTTP/1.1\r\nhost: lower.example\r\n\r\n", "lower.example"},
		{"mixed case header", "GET / HTTP/1.1\r\nHoSt: mixed.example\r\n\r\n", "mixed.example"},
		{"after other headers", "GET / HTTP/1.1\r\nUser-Agent: x\r\nAccept: */*\r\nHost: late.example\r\n\r\n", "late.example"},
		{"uppercase host", "GET / HTTP/1.1\r\nHost: UPPER.EXAMPLE\r\n\r\n", "upper.example"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, res := sniffBytes(t, []byte(c.req))
			if res.Domain != c.want {
				t.Fatalf("Domain = %q, want %q", res.Domain, c.want)
			}
			if res.Protocol != ProtocolHTTP {
				t.Fatalf("Protocol = %q, want %q", res.Protocol, ProtocolHTTP)
			}
		})
	}
}

// TestSniffHTTPWithoutHost verifies that a request with no Host header, or one
// that is not HTTP at all, yields no name.
func TestSniffHTTPWithoutHost(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"no host header", "GET / HTTP/1.1\r\nUser-Agent: x\r\n\r\n"},
		{"empty host", "GET / HTTP/1.1\r\nHost: \r\n\r\n"},
		{"headers not complete", "GET / HTTP/1.1\r\nUser-Agent: x\r\n"},
		{"unknown method", "FROBNICATE / HTTP/1.1\r\nHost: example.com\r\n\r\n"},
		{"not http at all", "\x00\x01\x02\x03binary garbage\xff\xfe"},

		// A stream that is not HTTP but happens to contain a Host-like line.
		// Without the method check, the HTTP parser would accept it and hand
		// the router a name the client never asked for — which is how a
		// crafted preamble could steer traffic to a rule of the sender's
		// choosing.
		{"non-http carrying a host line", "\x16\x03\x01not-a-method\r\nHost: smuggled.example\r\n\r\n"},
		{"text before a host line", "hello there\r\nHost: smuggled.example\r\n\r\n"},
		{"binary with a host line", "\x00\x01\x02\x03\r\nHost: smuggled.example\r\n\r\n"},
		{"address as host", "GET / HTTP/1.1\r\nHost: 93.184.216.34\r\n\r\n"},
		{"address with port", "GET / HTTP/1.1\r\nHost: 10.0.0.1:8080\r\n\r\n"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, res := sniffBytes(t, []byte(c.data))
			if res.Domain != "" {
				t.Fatalf("Domain = %q, want empty", res.Domain)
			}
		})
	}
}

// TestSanitizeDomainRejectsUnsafeValues covers the sanitizer directly, since
// it is the boundary between attacker-controlled bytes and a routing input.
//
// It is the load-bearing check for a crafted preamble: hasHTTPMethod narrows
// which streams reach the header walk, but the sanitizer is what guarantees
// that whatever the walk finds is a plausible name. A mutation that removed the
// method check alone would still be caught here for any value that is not a
// host name.
func TestSanitizeDomainRejectsUnsafeValues(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"example.com", "example.com"},
		{"EXAMPLE.com", "example.com"},
		{"example.com.", "example.com"},
		{"a-b.example", "a-b.example"},
		{"under_score.example", "under_score.example"},

		// Rejections: each of these would otherwise become a routing input.
		{"", ""},
		{".", ""},
		{".example.com", ""},
		{"example..com", ""},
		{"example.com..", ""},
		{"93.184.216.34", ""},
		{"10.0.0.1", ""},
		{"[::1]", ""},
		{"::1", ""},
		{"example.com\x00evil", ""},
		{"example.com/../etc", ""},
		{"example.com\r\nHost: evil", ""},
		{"exam ple.com", ""},
		{strings.Repeat("a", 300) + ".com", ""},
	}

	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := sanitizeDomain(c.in); got != c.want {
				t.Errorf("sanitizeDomain(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// --- byte preservation ----------------------------------------------------

// TestSniffedBytesAreForwardedIntact is the test that matters most.
//
// Sniffing reads from the connection. If those bytes are lost, the destination
// receives a truncated ClientHello and every TLS connection fails in a way
// that looks like a TLS fault rather than a proxy bug. The requirement is that
// what the peer reads is byte-for-byte what the client sent.
func TestSniffedBytesAreForwardedIntact(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"tls client hello", clientHello("preserve.example")},
		{"tls without sni", record(append([]byte{0x01}, appendU24(nil, 40)...))},
		{"http request", []byte("GET / HTTP/1.1\r\nHost: preserve.example\r\n\r\nbody-after-head")},
		{"unrecognized bytes", []byte("not any known protocol at all")},
		{"single byte", []byte("x")},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn, _ := sniffBytes(t, c.data)

			got := make([]byte, len(c.data))
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, err := io.ReadFull(conn, got); err != nil {
				t.Fatalf("reading back the sniffed bytes: %v", err)
			}
			if !bytes.Equal(got, c.data) {
				t.Fatalf("the sniffed bytes were altered:\n got %q\nwant %q", got, c.data)
			}
		})
	}
}

// TestSniffedBytesSurviveASplitRead verifies that the buffer is preserved
// across several small reads, as a relay would perform.
//
// A relay copies with its own buffer size, so the sniffed bytes are typically
// drained in pieces. A reader that returned the buffer only once would corrupt
// everything after the first read.
func TestSniffedBytesSurviveASplitRead(t *testing.T) {
	data := clientHello("split.example")
	conn, res := sniffBytes(t, data)
	if res.Domain != "split.example" {
		t.Fatalf("Domain = %q, want split.example", res.Domain)
	}

	var got []byte
	buf := make([]byte, 7) // deliberately small and not a divisor of the length
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for len(got) < len(data) {
		n, err := conn.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			break
		}
	}

	if !bytes.Equal(got, data) {
		t.Fatalf("a chunked read lost or reordered bytes:\n got %d bytes\nwant %d bytes", len(got), len(data))
	}
}

// --- fail-open behaviour --------------------------------------------------

// TestSniffTimesOutWithoutBlocking verifies that a silent client does not hold
// the connection open.
//
// The client is waiting for a dial that has not started, so a long wait here
// is a visible delay on every connection that does not speak first.
func TestSniffTimesOutWithoutBlocking(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	s := New(Options{Timeout: 50 * time.Millisecond})

	start := time.Now()
	conn, res, err := s.Sniff(server)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Sniff returned an error for a silent connection: %v", err)
	}
	if res.Domain != "" {
		t.Fatalf("Domain = %q, want empty", res.Domain)
	}
	if conn == nil {
		t.Fatal("Sniff returned a nil connection")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Sniff took %s, which is far beyond its timeout", elapsed)
	}
}

// TestSniffClearsTheDeadline verifies that the sniff timeout does not become
// the tunnel's timeout.
//
// Leaving the deadline in place would abort a healthy connection shortly after
// it started, which is a failure that appears only under a slow remote. The
// test reads well past the sniff window without setting a deadline of its own:
// if the sniff deadline were still armed, the read would fail rather than
// block, and blocking is what the timeout below detects.
func TestSniffClearsTheDeadline(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	s := New(Options{Timeout: 50 * time.Millisecond})
	conn, _, err := s.Sniff(server)
	if err != nil {
		t.Fatalf("Sniff: %v", err)
	}

	// The write and the read need separate goroutines. net.Pipe is
	// synchronous, so a write returns only once the peer reads it; doing both
	// in one goroutine would deadlock against itself and the failure would
	// look like a broken deadline.
	written := make(chan error, 1)
	go func() {
		// Delayed past the sniff window, so the read below necessarily spans
		// the moment the sniff deadline would have fired.
		time.Sleep(5 * s.timeout)
		_, err := client.Write([]byte("after the sniff window"))
		written <- err
	}()

	read := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		read <- err
	}()

	select {
	case err := <-read:
		if err != nil {
			t.Fatalf("reading after the sniff window failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the connection was unusable after sniffing")
	}

	if err := <-written; err != nil {
		t.Fatalf("writing to the peer failed: %v", err)
	}
}

// TestSniffNilConnection verifies the guard.
func TestSniffNilConnection(t *testing.T) {
	s := New(Options{})
	if _, _, err := s.Sniff(nil); err == nil {
		t.Fatal("Sniff accepted a nil connection")
	}
}

// TestSniffIntoHonoursCancellation verifies that a cancelled request does not
// spend the sniff timeout.
func TestSniffIntoHonoursCancellation(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	s := New(Options{Timeout: 5 * time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	conn, res := s.SniffInto(ctx, server)
	elapsed := time.Since(start)

	if res.Domain != "" {
		t.Fatalf("Domain = %q, want empty", res.Domain)
	}
	if conn != server {
		t.Error("a cancelled sniff replaced the connection")
	}
	if elapsed > time.Second {
		t.Fatalf("a cancelled sniff waited %s", elapsed)
	}
}

// TestBufferedConnWritesThrough verifies that writes reach the underlying
// connection.
//
// Reads are redirected to the buffer, so a wrapper that also redirected writes
// would swallow everything the proxy sends back.
func TestBufferedConnWritesThrough(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		_, _ = client.Write([]byte("GET / HTTP/1.1\r\nHost: write.example\r\n\r\n"))
	}()

	s := New(Options{Timeout: time.Second})
	conn, _ := s.SniffInto(context.Background(), server)

	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 32)
		n, _ := client.Read(buf)
		got <- string(buf[:n])
	}()

	if _, err := conn.Write([]byte("response-to-client")); err != nil {
		t.Fatalf("writing through the buffered connection: %v", err)
	}

	select {
	case s := <-got:
		if s != "response-to-client" {
			t.Fatalf("the peer received %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the write never reached the peer")
	}
}
