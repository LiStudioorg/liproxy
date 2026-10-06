// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package metadata

import "net"

// Metadata describes one connection's intended destination.
//
// It is constructed by the inbound that accepted the connection and read by
// the router and the outbound that serves it. The zero value is not
// meaningful: an outbound needs at least one of Domain or DestAddr to have
// anything to dial.
//
// The struct is deliberately small and allocated once per connection. Fields
// are added only when a real protocol needs them, because every field is paid
// for on every connection.
type Metadata struct {
	// Network is the transport to use, "tcp" or "udp". It is a string rather
	// than a typed constant because it is passed straight to net.Dial, and a
	// protocol that needs a new network can express it without changing this
	// package.
	Network string

	// SourceAddr is the client's address. It is recorded for logging and for
	// rules that match on the source, and is never used for dialing.
	SourceAddr net.Addr

	// DestAddr is the resolved destination, or nil when only a domain is
	// known.
	//
	// The HTTP CONNECT method gives a host:port string and an ordinary proxy
	// request gives a Host header; neither guarantees that the host is an IP.
	// A nil DestAddr with a non-empty Domain is the normal case, not an error.
	DestAddr net.Addr

	// Domain is the destination host name, when one is known.
	//
	// It may come from the protocol itself or from sniffing the first bytes of
	// the connection. It is empty for connections that addressed the proxy by
	// IP and carried nothing that reveals a name, which is why an outbound
	// must be able to work from DestAddr alone.
	Domain string

	// InboundTag identifies the inbound that accepted the connection, so that
	// rules and logs can distinguish traffic arriving on different listeners.
	InboundTag string

	// Protocol records how Domain was recovered, when sniffing supplied it:
	// "tls" for a ClientHello SNI, "http" for a Host header.
	//
	// It is empty when the name came from the protocol itself or when there is
	// no name at all. It exists for diagnostics — "the rule did not fire" is
	// answered differently when the name was never recovered than when it was
	// recovered from the wrong layer.
	Protocol string
}

// Destination returns the host:port an outbound should dial.
//
// Domain takes priority over DestAddr. That ordering is the whole point of the
// type: when sniffing has recovered a name for a connection that arrived
// addressed to an IP, dialing the name restores the routing and TLS behaviour
// the client intended, and dialing the IP would silently defeat it.
//
// It returns an empty string when neither is known, which an outbound reports
// as a configuration or protocol error rather than dialing something
// arbitrary.
func (m Metadata) Destination() string {
	if m.Domain != "" {
		port := "0"
		if m.DestAddr != nil {
			if _, p, err := net.SplitHostPort(m.DestAddr.String()); err == nil && p != "" {
				port = p
			}
		}
		return net.JoinHostPort(m.Domain, port)
	}
	if m.DestAddr != nil {
		return m.DestAddr.String()
	}
	return ""
}
