// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package all_test

import (
	"testing"

	"github.com/LiStudioorg/liproxy/internal/registry"
	_ "github.com/LiStudioorg/liproxy/internal/registry/all"
)

// TestImportingThisPackageRegistersProtocols verifies that the aggregation
// package does what the core packages cannot: pull in the protocol
// implementations.
//
// The engine is forbidden from importing a protocol package, so if this
// package ever stopped importing one, nothing would fail to build — the
// protocol would simply be missing at runtime, and a configuration naming it
// would be rejected as unknown. Asserting the list here is what turns that
// silent omission into a test failure.
func TestImportingThisPackageRegistersProtocols(t *testing.T) {
	wantInbound := []string{"http"}
	wantOutbound := []string{"block", "direct"}

	assertSet(t, "inbound", registry.InboundTypes(), wantInbound)
	assertSet(t, "outbound", registry.OutboundTypes(), wantOutbound)
}

// assertSet compares a registered type list against what this package promises
// to register.
//
// The comparison is exact in both directions: a missing entry means a protocol
// was dropped, and an extra one means a protocol was added without updating
// this list, which would let the next person believe the build contains less
// than it does.
func assertSet(t *testing.T, kind string, got, want []string) {
	t.Helper()

	gotSet := make(map[string]bool, len(got))
	for _, name := range got {
		gotSet[name] = true
	}
	wantSet := make(map[string]bool, len(want))
	for _, name := range want {
		wantSet[name] = true
	}

	for _, name := range want {
		if !gotSet[name] {
			t.Errorf("%s %q is not registered; have %v", kind, name, got)
		}
	}
	for _, name := range got {
		if !wantSet[name] {
			t.Errorf("%s %q is registered but not listed in this test; have %v", kind, name, got)
		}
	}
}
