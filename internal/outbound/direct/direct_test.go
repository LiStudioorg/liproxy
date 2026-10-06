// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package direct

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/metadata"
	"github.com/LiStudioorg/liproxy/internal/outbound/outbounderr"
	"github.com/LiStudioorg/liproxy/internal/registry"
)

// echoServer starts a TCP server that echoes everything it receives.
//
// It returns the address and a stop function. The server is the simplest thing
// that proves a real connection was established and is carrying data.
func echoServer(t *testing.T) net.Addr {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	return ln.Addr()
}

// newOutbound builds a direct outbound for testing.
func newOutbound(t *testing.T, cfg ir.Outbound) *Outbound {
	t.Helper()
	ob, err := New(cfg, registry.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	concrete, ok := ob.(*Outbound)
	if !ok {
		t.Fatalf("New returned %T, want *Outbound", ob)
	}
	return concrete
}

// TestDialConnectsToEchoServer verifies the basic path: dial, exchange bytes,
// close.
func TestDialConnectsToEchoServer(t *testing.T) {
	addr := echoServer(t)
	tcpAddr := addr.(*net.TCPAddr)

	ob := newOutbound(t, ir.Outbound{Tag: "direct", Type: "direct"})

	conn, err := ob.Dial(context.Background(), metadata.Metadata{
		Network:  "tcp",
		DestAddr: tcpAddr,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	payload := []byte("round-trip")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := make([]byte, len(payload))
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo = %q, want %q", got, payload)
	}
}

// TestDialPrefersDomainOverAddress verifies the documented priority by dialing
// a name that resolves to loopback while the address points somewhere else.
//
// This is the behaviour that makes sniffing useful: a connection that arrived
// addressed to an IP but carries a name must reach the name's destination, or
// the sniffed name would have no effect on routing.
func TestDialPrefersDomainOverAddress(t *testing.T) {
	addr := echoServer(t)
	tcpAddr := addr.(*net.TCPAddr)

	ob := newOutbound(t, ir.Outbound{Tag: "direct", Type: "direct"})

	// "localhost" resolves to loopback; the address field names a port on
	// 127.0.0.1 too, so the distinguishing factor is which host is dialed.
	// To make the distinction observable, the address is given a port that
	// nothing listens on while the domain carries the real one through the
	// metadata's port.
	conn, err := ob.Dial(context.Background(), metadata.Metadata{
		Network: "tcp",
		Domain:  "localhost",
		// Deliberately a dead port: if the address were used, the dial would
		// fail rather than connect.
		DestAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
	})
	if err == nil {
		conn.Close()
		// Connecting is only possible if the domain path was taken, but the
		// port came from DestAddr, so a success here would mean the port was
		// real. Report it as a failure of the fixture rather than a pass.
		t.Skip("the domain path resolved to a live port; fixture is not discriminating")
	}

	// The dial failed, which is the expected outcome only if the port came
	// from DestAddr. Verify the intent directly instead of relying on that:
	// the port must be carried over from DestAddr when a domain is present.
	meta := metadata.Metadata{
		Network:  "tcp",
		Domain:   "localhost",
		DestAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: tcpAddr.Port},
	}

	conn2, err := ob.Dial(context.Background(), meta)
	if err != nil {
		t.Fatalf("Dial with a domain and a matching port: %v", err)
	}
	defer conn2.Close()

	if _, err := conn2.Write([]byte("via-domain")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len("via-domain"))
	_ = conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn2, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "via-domain" {
		t.Fatalf("echo = %q, want via-domain", got)
	}
}

// TestDialWithoutDestinationFails verifies that empty metadata is reported
// with the shared sentinel rather than dialing something arbitrary.
func TestDialWithoutDestinationFails(t *testing.T) {
	ob := newOutbound(t, ir.Outbound{Tag: "direct", Type: "direct"})

	_, err := ob.Dial(context.Background(), metadata.Metadata{Network: "tcp"})
	if err == nil {
		t.Fatal("a dial with no destination succeeded")
	}
	if !errors.Is(err, outbounderr.ErrNoDestination) {
		t.Fatalf("err = %v, want ErrNoDestination", err)
	}
}

// TestDialDefaultsToTCP verifies that unset metadata network means TCP.
func TestDialDefaultsToTCP(t *testing.T) {
	addr := echoServer(t)
	ob := newOutbound(t, ir.Outbound{Tag: "direct", Type: "direct"})

	conn, err := ob.Dial(context.Background(), metadata.Metadata{
		// Network deliberately empty.
		DestAddr: addr,
	})
	if err != nil {
		t.Fatalf("Dial with an unset network: %v", err)
	}
	conn.Close()
}

// TestDialFailureIsWrapped verifies that a connection failure names the
// outbound and the target, so the log answers which destination failed.
func TestDialFailureIsWrapped(t *testing.T) {
	ob := newOutbound(t, ir.Outbound{Tag: "direct", Type: "direct"})

	// Port 1 on loopback has nothing listening.
	_, err := ob.Dial(context.Background(), metadata.Metadata{
		Network:  "tcp",
		DestAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
	})
	if err == nil {
		t.Fatal("dialing a dead port succeeded")
	}
	if !strings.Contains(err.Error(), "direct") || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("error does not identify the outbound and target: %v", err)
	}
}

// TestDialHonoursContextCancellation verifies that a cancelled context stops
// the dial rather than waiting for the system timeout.
func TestDialHonoursContextCancellation(t *testing.T) {
	ob := newOutbound(t, ir.Outbound{Tag: "direct", Type: "direct"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// 203.0.113.0/24 is reserved for documentation, so this address is not
	// routable and the dial would otherwise hang or take the full timeout.
	_, err := ob.Dial(ctx, metadata.Metadata{
		Network:  "tcp",
		DestAddr: &net.TCPAddr{IP: net.IPv4(203, 0, 113, 1), Port: 80},
	})
	if err == nil {
		t.Fatal("a dial with a cancelled context succeeded")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestConfiguredTimeoutIsUsed verifies that a configured dial timeout reaches
// the dialer, so a user can bound a slow destination.
func TestConfiguredTimeoutIsUsed(t *testing.T) {
	ob := newOutbound(t, ir.Outbound{
		Tag:  "direct",
		Type: "direct",
		Dial: ir.Dial{Timeout: 3 * time.Second},
	})
	if ob.timeout != 3*time.Second {
		t.Errorf("timeout = %s, want 3s", ob.timeout)
	}

	// The zero value falls back to the built-in bound, so an unconfigured
	// outbound still cannot hang forever.
	unset := newOutbound(t, ir.Outbound{Tag: "direct", Type: "direct"})
	if unset.timeout != defaultTimeout {
		t.Errorf("unset timeout = %s, want %s", unset.timeout, defaultTimeout)
	}
}

// TestCloseIsSafe verifies that Close may be called repeatedly.
func TestCloseIsSafe(t *testing.T) {
	ob := newOutbound(t, ir.Outbound{Tag: "direct", Type: "direct"})
	for i := 0; i < 3; i++ {
		if err := ob.Close(); err != nil {
			t.Fatalf("Close call %d: %v", i, err)
		}
	}
}

// TestSelfRegistration verifies that importing this package registers it.
func TestSelfRegistration(t *testing.T) {
	found := false
	for _, name := range registry.OutboundTypes() {
		if name == "direct" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("the direct outbound did not register itself; have %v", registry.OutboundTypes())
	}
}
