// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package http

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/metadata"
)

// defaultReadHeaderTimeout bounds how long a client may take to send its
// request line and headers.
const defaultReadHeaderTimeout = 10 * time.Second

// readHeaderTimeout returns the header timeout for an inbound.
//
// ir.Inbound has no timeout field of its own; the value lives in the inbound's
// open Options map, which is where a protocol's own settings belong until
// protocol field sets are defined. Reading it here keeps the timeout
// configurable without inventing an IR field that only this protocol uses.
func readHeaderTimeout(cfg ir.Inbound) time.Duration {
	raw, ok := cfg.Options["read_header_timeout"]
	if !ok {
		return defaultReadHeaderTimeout
	}

	switch v := raw.(type) {
	case string:
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			// A malformed value falls back rather than refusing to start: the
			// default is safe, and strict mode already reports unknown keys.
			return defaultReadHeaderTimeout
		}
		return d
	case int64:
		if v > 0 {
			return time.Duration(v) * time.Second
		}
	}
	return defaultReadHeaderTimeout
}

// serveProxy dispatches an HTTP proxy request.
//
// CONNECT and ordinary requests are handled differently because they produce
// different things: CONNECT yields a raw byte stream that this server must
// stop interpreting, while an ordinary request yields another HTTP exchange
// that can be forwarded with the standard client.
func (in *Inbound) serveProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		in.serveConnect(w, r)
		return
	}
	in.serveForward(w, r)
}

// serveConnect handles the CONNECT method by hijacking the connection.
//
// The response the client expects is sent before the tunnel carries data, and
// the ordering is not negotiable: a client that receives no 200 will not send
// its payload, so a proxy that hijacks first and replies via the hijacked
// connection must still write the status line itself.
func (in *Inbound) serveConnect(w http.ResponseWriter, r *http.Request) {
	// A CONNECT request carries no body. A client that sent one is either
	// confused or probing for request smuggling, so the connection is refused
	// rather than forwarded.
	if r.ContentLength > 0 {
		http.Error(w, "CONNECT with a request body is not supported", http.StatusBadRequest)
		return
	}

	target := r.Host
	if target == "" {
		target = r.URL.Host
	}
	host, port, err := splitHostPort(target, "443")
	if err != nil {
		http.Error(w, "invalid CONNECT target: "+err.Error(), http.StatusBadRequest)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		// Unreachable with net/http's own server, but an http.ResponseWriter
		// wrapped by middleware may not implement it, and that would otherwise
		// surface as a confusing 500 for every HTTPS request.
		http.Error(w, "connection hijacking is not supported", http.StatusInternalServerError)
		return
	}

	client, rw, err := hijacker.Hijack()
	if err != nil {
		// The connection is already unusable; the server has no way to send a
		// response at this point.
		in.log().Debug().Err(err).Str("inbound", in.tag).Msg("hijack failed")
		return
	}

	// Write the 200 directly: after hijacking, the standard response writer no
	// longer applies, and the client is blocked waiting for this line.
	_, err = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	if err == nil {
		err = rw.Flush()
	}
	if err != nil {
		_ = client.Close()
		return
	}

	// Any bytes the client sent immediately after its headers are already in
	// the buffered reader. They belong to the tunnel, and dropping them would
	// truncate the client's first flight — a TLS ClientHello fits in exactly
	// this window, so losing it breaks every HTTPS request.
	buffered := rw.Reader.Buffered()
	var prefix []byte
	if buffered > 0 {
		prefix = make([]byte, buffered)
		if _, err := readFull(rw, prefix); err != nil {
			_ = client.Close()
			return
		}
	}

	meta := metadata.Metadata{
		Network:    "tcp",
		SourceAddr: remoteAddr(r),
		Domain:     host,
		InboundTag: in.tag,
	}
	if ip := net.ParseIP(host); ip != nil {
		// The target was already an address, so there is no name to resolve.
		meta.DestAddr = &net.TCPAddr{IP: ip, Port: port}
		meta.Domain = ""
	} else {
		meta.DestAddr = &net.TCPAddr{Port: port}
	}

	if len(prefix) > 0 {
		client = &prefixConn{Conn: client, prefix: prefix}
	}

	if err := in.handler.Handle(r.Context(), client, meta); err != nil {
		in.log().Debug().Err(err).Str("inbound", in.tag).Str("target", target).
			Msg("tunnel closed with error")
	}
}

// serveForward handles an ordinary proxy request with an absolute URI.
//
// It is substantially less common than CONNECT — most traffic is HTTPS — but
// plain HTTP through a proxy still occurs, so it is supported rather than
// refused. The exchange is handed to the engine's handler as a byte stream,
// which is the same path a tunnel takes: the engine decides the outbound, and
// this package stays out of the routing decision.
func (in *Inbound) serveForward(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() || r.URL.Host == "" {
		// A request without an absolute URI is addressed to this server, not
		// through it. A proxy has nothing of its own to serve.
		http.Error(w, "this is a proxy; absolute-form request URI required", http.StatusBadRequest)
		return
	}

	meta := metadata.Metadata{
		Network:    "tcp",
		SourceAddr: remoteAddr(r),
		Domain:     r.URL.Hostname(),
		InboundTag: in.tag,
	}

	port := 80
	if p := r.URL.Port(); p != "" {
		parsed, err := strconv.Atoi(p)
		if err != nil {
			http.Error(w, "invalid port in request URI", http.StatusBadRequest)
			return
		}
		port = parsed
	}

	if ip := net.ParseIP(meta.Domain); ip != nil {
		// Already an address: there is no name to resolve.
		meta.DestAddr = &net.TCPAddr{IP: ip, Port: port}
		meta.Domain = ""
	} else {
		meta.DestAddr = &net.TCPAddr{Port: port}
	}

	// The request is replayed over the dialed connection by the engine, so
	// what this package hands over is the serialized request head followed by
	// any body. r.Write does exactly that and is the standard library's own
	// serializer, which keeps request framing out of this package.
	var head bytes.Buffer
	if err := r.Write(&head); err != nil {
		http.Error(w, "cannot serialize request: "+err.Error(), http.StatusInternalServerError)
		return
	}

	client := &responseConn{
		Conn:   nopConn{},
		source: bytes.NewReader(head.Bytes()),
		writer: w,
	}

	if err := in.handler.Handle(r.Context(), client, meta); err != nil {
		in.log().Debug().Err(err).Str("inbound", in.tag).Str("url", r.URL.String()).
			Msg("forward request failed")
	}
}

// nopConn is a net.Conn with no kernel socket behind it.
//
// A forwarded request has no hijacked connection: the client is still attached
// to the http.ResponseWriter. It exists so that the engine receives one type
// of stream for both tunnel and forward cases, rather than branching on which
// kind of request produced it.
type nopConn struct{}

func (nopConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (nopConn) Write(b []byte) (int, error)      { return len(b), nil }
func (nopConn) Close() error                     { return nil }
func (nopConn) LocalAddr() net.Addr              { return stringAddr("") }
func (nopConn) RemoteAddr() net.Addr             { return stringAddr("") }
func (nopConn) SetDeadline(time.Time) error      { return nil }
func (nopConn) SetReadDeadline(time.Time) error  { return nil }
func (nopConn) SetWriteDeadline(time.Time) error { return nil }

// responseConn feeds a serialized request to the engine and writes the reply
// back through the ResponseWriter.
type responseConn struct {
	net.Conn
	source *bytes.Reader
	writer http.ResponseWriter
}

// Read serves the serialized request bytes.
func (c *responseConn) Read(p []byte) (int, error) { return c.source.Read(p) }

// Write sends bytes back to the client and flushes them immediately, since a
// proxied response has no framing this package can rely on.
func (c *responseConn) Write(p []byte) (int, error) {
	n, err := c.writer.Write(p)
	if f, ok := c.writer.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}

// splitHostPort splits a CONNECT target, supplying a default port.
//
// CONNECT requires host:port, but a client that omits the port is a common
// enough interop case that guessing 443 is friendlier than refusing.
func splitHostPort(target, defaultPort string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		// No port present: treat the whole string as the host.
		host = target
		portStr = defaultPort
	}
	if host == "" {
		return "", 0, fmt.Errorf("%q has no host", target)
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("port %q is not a number", portStr)
	}
	if port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("port %d is out of range", port)
	}
	return host, port, nil
}

// remoteAddr parses the request's remote address into a net.Addr.
//
// The string form is used when it does not parse, so that metadata always
// carries something identifying the client rather than silently dropping it.
func remoteAddr(r *http.Request) net.Addr {
	addr, err := net.ResolveTCPAddr("tcp", r.RemoteAddr)
	if err != nil {
		return stringAddr(r.RemoteAddr)
	}
	return addr
}

// stringAddr is a net.Addr backed by a bare string.
type stringAddr string

func (a stringAddr) Network() string { return "tcp" }
func (a stringAddr) String() string  { return string(a) }

// readFull fills buf from the hijacked reader.
func readFull(rw interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := rw.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// prefixConn replays bytes that were read from the connection before it was
// handed to the tunnel.
type prefixConn struct {
	net.Conn
	prefix []byte
}

// Read returns the buffered prefix first, then reads from the connection.
func (c *prefixConn) Read(p []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}
