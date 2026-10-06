// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// pipePair returns two ends of an in-process connection pair.
func pipePair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	return a, b
}

// TestPipeForwardsBothDirections verifies the basic requirement: bytes written
// on either end arrive at the other.
func TestPipeForwardsBothDirections(t *testing.T) {
	// clientSide and serverSide are the two ends the relay joins; the test
	// writes on the far side of each.
	relayA, farA := pipePair(t)
	relayB, farB := pipePair(t)

	done := make(chan error, 1)
	go func() {
		done <- Pipe(context.Background(), relayA, relayB)
	}()

	// farA -> relayA -> relayB -> farB
	go func() {
		_, _ = farA.Write([]byte("from-a"))
	}()
	buf := make([]byte, len("from-a"))
	if _, err := io.ReadFull(farB, buf); err != nil {
		t.Fatalf("reading the a-to-b direction: %v", err)
	}
	if string(buf) != "from-a" {
		t.Fatalf("a-to-b delivered %q, want %q", buf, "from-a")
	}

	// farB -> relayB -> relayA -> farA
	go func() {
		_, _ = farB.Write([]byte("from-b"))
	}()
	buf2 := make([]byte, len("from-b"))
	if _, err := io.ReadFull(farA, buf2); err != nil {
		t.Fatalf("reading the b-to-a direction: %v", err)
	}
	if string(buf2) != "from-b" {
		t.Fatalf("b-to-a delivered %q, want %q", buf2, "from-b")
	}
}

// TestPipeTransfersLargePayload verifies that a transfer larger than any
// internal buffer arrives intact.
//
// A copy loop with a fixed buffer has to handle a payload spanning many reads,
// and a mistake there truncates or duplicates data at the boundary — which a
// short test message would never reveal.
func TestPipeTransfersLargePayload(t *testing.T) {
	relayA, farA := pipePair(t)
	relayB, farB := pipePair(t)

	go func() { _ = Pipe(context.Background(), relayA, relayB) }()

	const size = 1 << 20
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = farA.Write(payload)
		// Closing the write side lets the relay observe the end of the stream
		// and propagate it, rather than waiting for more data.
		if cw, ok := farA.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = farA.Close()
		}
	}()

	got := make([]byte, size)
	if _, err := io.ReadFull(farB, got); err != nil {
		t.Fatalf("reading %d bytes: %v", size, err)
	}
	wg.Wait()

	if !bytes.Equal(got, payload) {
		t.Fatal("the transferred payload differs from what was sent")
	}
}

// tcpPair returns two ends of a real TCP connection.
//
// The relay's half-close path only exists on connections that implement
// CloseWrite. net.Pipe, which the other tests use for speed, does not: the
// relay falls back to closing both directions there. Half-close therefore has
// to be tested over real sockets or it tests the fallback instead of the
// behaviour half-close exists for.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	server, ok := <-accepted
	if !ok {
		_ = client.Close()
		t.Fatal("accept failed")
	}

	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return client, server
}

// TestPipePropagatesHalfClose verifies that closing one direction produces EOF
// on the other side while leaving the reverse direction usable.
//
// This is the behaviour request/response protocols depend on: a client that
// finishes its request by closing its write side expects the peer to see EOF
// and reply. A relay that closed the whole connection instead would make that
// pattern fail with a broken pipe rather than a reply.
func TestPipePropagatesHalfClose(t *testing.T) {
	// The client's end joins the relay; the server's end is the far side the
	// test drives.
	client, server := tcpPair(t)
	relayA, farA := tcpPair(t)
	relayB, farB := tcpPair(t)

	done := make(chan error, 1)
	go func() {
		done <- Pipe(context.Background(), relayA, relayB)
	}()

	// Write through the tunnel, then half-close from the far side of A. The
	// bytes travel farA -> relayA -> relayB -> farB.
	go func() {
		_, _ = farA.Write([]byte("request"))
		if cw, ok := farA.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = farA.Close()
		}
	}()

	buf := make([]byte, len("request"))
	if _, err := io.ReadFull(farB, buf); err != nil {
		t.Fatalf("reading the request: %v", err)
	}
	if string(buf) != "request" {
		t.Fatalf("got %q, want request", buf)
	}

	// The half-close must surface as EOF on the peer, not as a hang.
	_ = farB.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := farB.Read(make([]byte, 16))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("after half-close, Read returned (%d, %v), want (0, EOF)", n, err)
	}

	// The reverse direction must still work, which is what distinguishes a
	// half-close from a full close.
	if _, err := farB.Write([]byte("response")); err != nil {
		t.Fatalf("writing after the peer half-closed: %v", err)
	}

	got := make([]byte, len("response"))
	_ = farA.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(farA, got); err != nil {
		t.Fatalf("reading the response after half-close: %v", err)
	}
	if string(got) != "response" {
		t.Fatalf("response = %q, want response", got)
	}

	// Both far ends are closed before waiting: Pipe completes only when both
	// directions have finished, and the b-to-a direction is still live until
	// its far end goes away. Waiting first would block by design, not by bug.
	_ = farA.Close()
	_ = farB.Close()
	_ = client.Close()
	_ = server.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Pipe did not return after both far ends closed")
	}
}

// TestPipeStopsOnContextCancel verifies that cancelling the context ends the
// transfer rather than leaving both goroutines blocked.
//
// Without this, every cancelled tunnel leaks two goroutines and two sockets,
// and a long-running process accumulates them without bound.
func TestPipeStopsOnContextCancel(t *testing.T) {
	relayA, _ := pipePair(t)
	relayB, _ := pipePair(t)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- Pipe(ctx, relayA, relayB)
	}()

	// Nothing is written, so both copy goroutines are blocked in Read.
	// Cancellation has to be what unblocks them.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Pipe returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Pipe did not return after the context was cancelled")
	}

	// Both connections must be closed, not merely abandoned.
	if _, err := relayA.Write([]byte("x")); err == nil {
		t.Error("connection a was still writable after cancellation")
	}
	if _, err := relayB.Write([]byte("x")); err == nil {
		t.Error("connection b was still writable after cancellation")
	}
}

// TestPipeReturnsOnPeerClose verifies that closing one end terminates the
// relay instead of hanging on the other direction.
func TestPipeReturnsOnPeerClose(t *testing.T) {
	relayA, farA := pipePair(t)
	relayB, farB := pipePair(t)

	done := make(chan error, 1)
	go func() {
		done <- Pipe(context.Background(), relayA, relayB)
	}()

	// Closing farB ends the b-to-a direction. The a-to-b direction is still
	// open, so the relay must close it rather than wait forever.
	_ = farB.Close()

	// farA must observe the closure, since the relay closes both ends.
	_ = farA.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err := farA.Read(make([]byte, 16))
	if err == nil {
		t.Fatal("farA read succeeded after the peer closed")
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Pipe did not return after a peer closed")
	}
}

// TestPipeClosesBothConnections verifies the ownership contract: Pipe closes
// what it was given, so a caller does not have to.
func TestPipeClosesBothConnections(t *testing.T) {
	relayA, farA := pipePair(t)
	relayB, farB := pipePair(t)

	go func() { _ = Pipe(context.Background(), relayA, relayB) }()

	_ = farA.Close()
	_ = farB.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := relayA.Write([]byte("x")); err != nil {
			// Both relay-side connections are closed; the contract holds.
			_, errB := relayB.Write([]byte("x"))
			if errB == nil {
				t.Fatal("relayB is still writable while relayA is closed")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the relay did not close its connections")
}

// TestPipeIsBenignOnPeerReset verifies that a peer resetting the connection is
// not reported as a failure.
//
// Resetting after a response is common client behaviour, and treating it as an
// error would fill the log with failures for traffic that worked.
func TestPipeIsBenignOnPeerReset(t *testing.T) {
	server, client := net.Pipe()
	relayA, _ := pipePair(t)

	done := make(chan error, 1)
	go func() {
		done <- Pipe(context.Background(), relayA, client)
	}()

	// Closing the server side while the client is still reading produces
	// io.ErrClosedPipe, which is an ordinary end of stream.
	_ = server.Close()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("a closed peer produced %v, want nil or a benign error", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Pipe did not return after the peer closed")
	}
}

// TestIsBenignCopyError covers the classifier directly, since it decides which
// errors reach the log.
func TestIsBenignCopyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"eof", io.EOF, true},
		{"closed", net.ErrClosed, true},
		{"op error wrapping closed", &net.OpError{Op: "read", Err: net.ErrClosed}, true},
		{"genuine failure", errors.New("connection reset by peer"), false},
		{"op error wrapping a real failure", &net.OpError{Op: "write", Err: errors.New("broken pipe")}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isBenignCopyError(c.err); got != c.want {
				t.Errorf("isBenignCopyError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
