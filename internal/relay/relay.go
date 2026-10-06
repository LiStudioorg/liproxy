// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
)

// closeWriter is implemented by connections that can close one direction.
//
// TCP connections and most wrapped connections implement it. When it is
// available the relay uses it, because closing only the write side lets the
// peer see EOF while this side keeps reading — which is what half-close means
// and what request/response protocols rely on.
type closeWriter interface {
	CloseWrite() error
}

// Pipe copies data in both directions between a and b until both directions
// finish, the context is cancelled, or a copy fails.
//
// It blocks until the transfer is over and owns neither connection: both are
// closed before Pipe returns, because a caller that has to close them itself
// would have to duplicate the same cancellation and error handling.
//
// Exactly two goroutines are used, one per direction. That is the minimum for
// a bidirectional copy: a single goroutine cannot read from both sides at
// once, and a protocol that streams continuously in both directions would
// deadlock against a sequential implementation.
func Pipe(ctx context.Context, a, b net.Conn) error {
	// A cancelled context has to unblock both copy goroutines, and closing is
	// the only way to interrupt a blocked Read. Closing here — rather than
	// waiting for the copies — is what turns cancellation into prompt exit.
	//
	// The closed flag keeps this from firing after a normal return, where
	// closing again is merely redundant but would race with the caller
	// inspecting the connections.
	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}

	stop := make(chan struct{})
	defer close(stop)

	go func() {
		select {
		case <-ctx.Done():
			closeBoth()
		case <-stop:
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	errCh := make(chan error, 2)

	go func() {
		defer wg.Done()
		errCh <- copyDirection(b, a)
	}()
	go func() {
		defer wg.Done()
		errCh <- copyDirection(a, b)
	}()

	wg.Wait()
	closeBoth()

	// A cancelled context is the normal way a tunnel ends during shutdown, so
	// it is reported as such rather than as a copy failure. The distinction
	// matters: the engine logs a genuine copy failure but not a cancellation.
	if err := ctx.Err(); err != nil {
		return err
	}

	// Both directions are drained before deciding: one direction ending
	// cleanly while the other fails is still a failure, and returning early
	// would hide it.
	var firstErr error
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// copyDirection copies from src to dst and propagates the end of the stream.
//
// io.Copy is used deliberately rather than a hand-rolled loop: it already
// handles short writes and uses ReadFrom/WriteTo fast paths when the
// underlying types provide them. Replacing it with a manual buffer loop now
// would be slower, not faster.
func copyDirection(dst, src net.Conn) error {
	_, err := io.Copy(dst, src)

	// Whatever happened, the other direction has to learn that no more data is
	// coming. Without this the peer waits for an EOF that never arrives, and a
	// proxy that never hangs up is indistinguishable from a hung one.
	switch cw := dst.(type) {
	case closeWriter:
		_ = cw.CloseWrite()
	default:
		// No half-close available, so the whole connection is closed. This
		// loses the ability to read a late reply, which is why it is the
		// fallback rather than the default.
		_ = dst.Close()
	}

	if err != nil && !isBenignCopyError(err) {
		return err
	}
	return nil
}

// isBenignCopyError reports whether a copy error is an ordinary end of stream.
//
// A peer that resets the connection after sending its response is extremely
// common — some HTTP clients do it every time — and reporting it as a failure
// would fill the log with errors for traffic that worked.
func isBenignCopyError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	// A closed write side surfaces as a write error on an otherwise finished
	// transfer.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return errors.Is(opErr.Err, net.ErrClosed)
	}
	return false
}
