// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package block

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/metadata"
	"github.com/LiStudioorg/liproxy/internal/outbound/outbounderr"
	"github.com/LiStudioorg/liproxy/internal/registry"
)

// TestDialAlwaysFails verifies that every dial is refused.
func TestDialAlwaysFails(t *testing.T) {
	ob, err := New(ir.Outbound{Tag: "block", Type: "block"}, registry.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	metas := []metadata.Metadata{
		{Domain: "ads.example"},
		{DestAddr: &net.TCPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 80}},
		{},
	}
	for i, m := range metas {
		conn, err := ob.Dial(context.Background(), m)
		if err == nil {
			conn.Close()
			t.Fatalf("case %d: Dial succeeded with %+v", i, m)
		}
		if !errors.Is(err, outbounderr.ErrBlocked) {
			t.Errorf("case %d: err = %v, want ErrBlocked", i, err)
		}
	}
}

// TestDialNamesTheTarget verifies that the refusal says what was blocked.
//
// The user's question when traffic stops is "what did it block", and an error
// that only says "blocked" leaves them searching the rule list.
func TestDialNamesTheTarget(t *testing.T) {
	ob, _ := New(ir.Outbound{Tag: "block", Type: "block"}, registry.Deps{})

	_, err := ob.Dial(context.Background(), metadata.Metadata{
		Domain:   "ads.example",
		DestAddr: &net.TCPAddr{Port: 443},
	})
	if err == nil {
		t.Fatal("Dial succeeded")
	}
	if !strings.Contains(err.Error(), "ads.example") {
		t.Fatalf("error does not name the blocked target: %v", err)
	}
}

// TestErrBlockedIsTheSharedSentinel verifies that the exported sentinel is the
// same value the engine compares against.
//
// The engine is forbidden from importing this package, so a locally declared
// sentinel would compile and then never match. This test is what keeps the
// re-export honest.
func TestErrBlockedIsTheSharedSentinel(t *testing.T) {
	if !errors.Is(ErrBlocked, outbounderr.ErrBlocked) {
		t.Fatal("block.ErrBlocked is not the shared sentinel; the engine's check would never fire")
	}
}

// TestBlockOutboundIsCreatedThroughTheRegistry verifies the registration path,
// which is how the engine actually builds it.
func TestBlockOutboundIsCreatedThroughTheRegistry(t *testing.T) {
	ob, err := registry.NewOutbound(ir.Outbound{Tag: "b", Type: "block"}, registry.Deps{})
	if err != nil {
		t.Fatalf("NewOutbound: %v", err)
	}
	if _, err := ob.Dial(context.Background(), metadata.Metadata{Domain: "x.example"}); !errors.Is(err, outbounderr.ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
}

// TestCloseIsSafe verifies that Close may be called repeatedly.
func TestCloseIsSafe(t *testing.T) {
	ob, _ := New(ir.Outbound{Tag: "block", Type: "block"}, registry.Deps{})
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
		if name == "block" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("the block outbound did not register itself; have %v", registry.OutboundTypes())
	}
}
