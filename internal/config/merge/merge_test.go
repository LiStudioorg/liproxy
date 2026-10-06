// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package merge

import (
	"errors"
	"strings"
	"testing"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
)

// docFile builds an ir.File from TOML text, so merge tests can be written as
// the configuration a user would actually write.
func docFile(path, body string) ir.File {
	return ir.File{
		Ref:     ir.SourceRef{Path: path, Line: 1, Format: ir.FormatTOML},
		Content: []byte(body),
	}
}

// yamlFile builds an ir.File from YAML text.
func yamlFile(path, body string) ir.File {
	return ir.File{
		Ref:     ir.SourceRef{Path: path, Line: 1, Format: ir.FormatYAML},
		Content: []byte(body),
	}
}

func mustMerge(t *testing.T, files ...ir.File) *Doc {
	t.Helper()
	doc, err := FromFiles(files)
	if err != nil {
		t.Fatalf("FromFiles: %v", err)
	}
	return doc
}

// TestMergeMapDeepMerge verifies that tables merge key-wise rather than being
// replaced wholesale.
func TestMergeMapDeepMerge(t *testing.T) {
	doc := mustMerge(t,
		docFile("a.toml", `
[server]
listen = "127.0.0.1"
port = 9090
`),
		docFile("b.toml", `
[server]
port = 8080
`),
	)

	server, ok := doc.Body["server"].(map[string]any)
	if !ok {
		t.Fatalf("server is %T, want table", doc.Body["server"])
	}

	// The key b.toml never mentioned must survive: replacing the whole table
	// would lose it, which is the failure this test exists to catch.
	if got := server["listen"]; got != "127.0.0.1" {
		t.Errorf("listen = %v, want 127.0.0.1 (must survive a partial override)", got)
	}
	if got := server["port"]; got != int64(8080) {
		t.Errorf("port = %v (%T), want 8080", got, got)
	}
}

// TestMergeScalarOverrideRecordsShadow verifies that the losing value's source
// is retained, which is what makes "I changed it but nothing happened"
// answerable.
func TestMergeScalarOverrideRecordsShadow(t *testing.T) {
	doc := mustMerge(t,
		docFile("a.toml", `log_level = "info"`),
		docFile("b.toml", `log_level = "debug"`),
	)

	if got := doc.Body["log_level"]; got != "debug" {
		t.Fatalf("log_level = %v, want debug (later file wins)", got)
	}

	shadowed := doc.Shadowed["log_level"]
	if len(shadowed) != 1 {
		t.Fatalf("shadowed log_level = %d entries, want 1", len(shadowed))
	}
	if !strings.HasSuffix(shadowed[0].Path, "a.toml") {
		t.Errorf("shadowed source = %q, want the earlier file a.toml", shadowed[0].Path)
	}

	origin := doc.OriginFor("log_level")
	if !strings.HasSuffix(origin.Source.Path, "b.toml") {
		t.Errorf("origin source = %q, want b.toml", origin.Source.Path)
	}
	if len(origin.Overridden) != 1 {
		t.Errorf("origin overridden = %d, want 1", len(origin.Overridden))
	}
}

// TestMergeListAppends verifies that lists accumulate across documents.
func TestMergeListAppends(t *testing.T) {
	doc := mustMerge(t,
		docFile("a.toml", `
[[rules]]
type = "domain"
payload = ["a.example"]
target = "direct"
`),
		docFile("b.toml", `
[[rules]]
type = "domain"
payload = ["b.example"]
target = "proxy"
`),
	)

	rules, ok := doc.Body["rules"].([]any)
	if !ok {
		t.Fatalf("rules is %T, want array", doc.Body["rules"])
	}
	if len(rules) != 2 {
		t.Fatalf("len(rules) = %d, want 2 (append, not replace)", len(rules))
	}

	// Order is semantically significant: the earlier file's rule comes first.
	first, _ := rules[0].(map[string]any)
	second, _ := rules[1].(map[string]any)
	if first["target"] != "direct" {
		t.Errorf("rules[0].target = %v, want direct", first["target"])
	}
	if second["target"] != "proxy" {
		t.Errorf("rules[1].target = %v, want proxy", second["target"])
	}
}

// TestMergeRulesPrepend verifies that a rule marked prepend lands ahead of the
// rules already merged, because an exception must be evaluated first.
func TestMergeRulesPrepend(t *testing.T) {
	doc := mustMerge(t,
		docFile("base.toml", `
[[rules]]
type = "domain"
payload = ["existing.example"]
target = "proxy"
`),
		docFile("exception.toml", `
[[rules]]
type = "domain"
payload = ["bypass.example"]
target = "direct"
position = "prepend"
`),
	)

	rules, _ := doc.Body["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("len(rules) = %d, want 2", len(rules))
	}

	first, _ := rules[0].(map[string]any)
	if first["target"] != "direct" {
		t.Fatalf("rules[0].target = %v, want direct (prepended rule must come first)", first["target"])
	}

	// The directive itself must not survive into the merged tree: it exists to
	// influence ordering and has no meaning to the engine.
	if _, present := first["position"]; present {
		t.Error("position key survived into the merged tree; it must be consumed by the merge")
	}
}

// TestMergeRulesPrependKeepsWrittenOrder verifies that several prepends from
// one file stay in the order they were written.
func TestMergeRulesPrependKeepsWrittenOrder(t *testing.T) {
	doc := mustMerge(t,
		docFile("base.toml", `
[[rules]]
type = "domain"
payload = ["base.example"]
target = "proxy"
`),
		docFile("two.toml", `
[[rules]]
type = "domain"
payload = ["first.example"]
target = "alpha"
position = "prepend"

[[rules]]
type = "domain"
payload = ["second.example"]
target = "beta"
position = "prepend"
`),
	)

	rules, _ := doc.Body["rules"].([]any)
	if len(rules) != 3 {
		t.Fatalf("len(rules) = %d, want 3", len(rules))
	}

	got := make([]string, 0, 3)
	for _, r := range rules {
		m, _ := r.(map[string]any)
		got = append(got, m["target"].(string))
	}
	want := []string{"alpha", "beta", "proxy"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rule order = %v, want %v (prepends keep written order)", got, want)
		}
	}
}

// TestMergeDuplicateOutboundErrors verifies the hard failure required by
// AGENTS.md 21.4, including that both source lines are named.
func TestMergeDuplicateOutboundErrors(t *testing.T) {
	_, err := FromFiles([]ir.File{
		docFile("a.toml", `
[[outbounds]]
tag = "us-node"
type = "vless"
server = "a.example"

`),
		docFile("b.toml", `
[[outbounds]]
tag = "us-node"
type = "trojan"
server = "b.example"
`),
	})

	if err == nil {
		t.Fatal("duplicate outbound tag was accepted; it must be an error")
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}

	var mErr *Error
	if !errors.As(err, &mErr) {
		t.Fatalf("err is %T, want *Error", err)
	}
	if len(mErr.Related) == 0 {
		t.Fatal("Related is empty; the message must name the other definition too")
	}

	// Both files must appear: the one being rejected and the one that already
	// claimed the tag. Naming only one leaves the user unable to find the pair.
	rendered := mErr.Error() + " " + formatRef(mErr.Related[0])
	if !strings.Contains(rendered, "a.toml") || !strings.Contains(rendered, "b.toml") {
		t.Fatalf("error does not name both definitions:\n%s", rendered)
	}
	if !strings.Contains(rendered, "us-node") {
		t.Fatalf("error does not name the tag:\n%s", rendered)
	}
}

// TestMergeDuplicateProviderErrors verifies the same rule for providers, which
// are keyed by name rather than tag.
func TestMergeDuplicateProviderErrors(t *testing.T) {
	_, err := FromFiles([]ir.File{
		docFile("a.toml", `
[[providers]]
name = "airport"
type = "proxy"
url = "https://a.example/sub"
`),
		docFile("b.toml", `
[[providers]]
name = "airport"
type = "proxy"
url = "https://b.example/sub"
`),
	})

	if err == nil {
		t.Fatal("duplicate provider name was accepted; it must be an error")
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}

	var mErr *Error
	if errors.As(err, &mErr) {
		if len(mErr.Related) == 0 {
			t.Error("Related is empty; both provider definitions must be named")
		}
		if !strings.Contains(mErr.Error(), "airport") {
			t.Errorf("error does not name the provider: %v", mErr)
		}
	}
}

// TestMergeDuplicateRuleSetMerges verifies that rule sets combine rather than
// conflict, which is the deliberate opposite of the outbound rule.
func TestMergeDuplicateRuleSetMerges(t *testing.T) {
	doc := mustMerge(t,
		docFile("a.toml", `
[[rule_sets]]
name = "ads"
type = "inline"

[[rule_sets.rules]]
type = "domain"
payload = ["a.example"]
target = "block"
`),
		docFile("b.toml", `
[[rule_sets]]
name = "ads"
type = "inline"

[[rule_sets.rules]]
type = "domain"
payload = ["b.example"]
target = "block"
`),
	)

	sets, ok := doc.Body["rule_sets"].([]any)
	if !ok {
		t.Fatalf("rule_sets is %T, want array", doc.Body["rule_sets"])
	}
	if len(sets) != 1 {
		t.Fatalf("len(rule_sets) = %d, want 1 (same name must combine, not duplicate)", len(sets))
	}

	set, _ := sets[0].(map[string]any)
	rules, _ := set["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("len(rules) = %d, want 2 (both files' rules must be present)", len(rules))
	}
}

// TestMergeDuplicateProxyGroupMerges verifies the same for proxy groups.
func TestMergeDuplicateProxyGroupMerges(t *testing.T) {
	doc := mustMerge(t,
		docFile("a.toml", `
[[proxy_groups]]
name = "auto"
type = "url-test"
proxies = ["n1"]
`),
		docFile("b.toml", `
[[proxy_groups]]
name = "auto"
type = "url-test"
proxies = ["n2"]
`),
	)

	groups, _ := doc.Body["proxy_groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("len(proxy_groups) = %d, want 1", len(groups))
	}
	group, _ := groups[0].(map[string]any)
	proxies, _ := group["proxies"].([]any)
	if len(proxies) != 2 {
		t.Fatalf("len(proxies) = %d, want 2 (members from both files)", len(proxies))
	}
}

// TestMergeTypeMismatchErrors verifies that incompatible shapes are reported
// rather than resolved by precedence.
func TestMergeTypeMismatchErrors(t *testing.T) {
	_, err := FromFiles([]ir.File{
		docFile("a.toml", `
[sniffer]
enabled = true
`),
		docFile("b.toml", `
sniffer = ["not", "a", "table"]
`),
	})

	if err == nil {
		t.Fatal("conflicting shapes were accepted; they must be an error")
	}
	if !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("err = %v, want ErrTypeMismatch", err)
	}
}

// TestMergeDuplicateKeyInOneFileErrors verifies that a key written twice in a
// single YAML document is rejected.
//
// YAML would otherwise keep the last value silently, and the same mistake in
// TOML is a parse error. Rejecting in the shared tree builder is what makes
// the rule identical in both formats.
func TestMergeDuplicateKeyInOneFileErrors(t *testing.T) {
	_, err := FromFiles([]ir.File{
		yamlFile("dup.yaml", `
log:
  level: info
  level: debug
`),
	})

	if err == nil {
		t.Fatal("duplicate key was accepted; it must be an error")
	}
	if !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("err = %v, want ErrDuplicateKey", err)
	}
}

// TestMergeInvalidPositionErrors verifies that an unusable position directive
// is reported rather than ignored.
func TestMergeInvalidPositionErrors(t *testing.T) {
	_, err := FromFiles([]ir.File{
		docFile("a.toml", `
[[rules]]
type = "domain"
payload = ["x.example"]
target = "direct"
position = "middle"
`),
	})

	if err == nil {
		t.Fatal("invalid position was accepted; it must be an error")
	}
	if !errors.Is(err, ErrBadPosition) {
		t.Fatalf("err = %v, want ErrBadPosition", err)
	}
}

// TestMergeEmptyFileContributesNothing verifies that a fragment with no
// content is legal.
func TestMergeEmptyFileContributesNothing(t *testing.T) {
	doc := mustMerge(t,
		docFile("empty.toml", ""),
		docFile("a.toml", `log_level = "info"`),
	)
	if doc.Body["log_level"] != "info" {
		t.Fatalf("log_level = %v, want info", doc.Body["log_level"])
	}
}

// TestMergeDeterministicSources verifies that provenance paths do not depend on
// Go's randomized map iteration order.
func TestMergeDeterministicSources(t *testing.T) {
	build := func() []string {
		doc := mustMerge(t, docFile("a.toml", `
zeta = 1
alpha = 2
mid = 3

[nested]
b = 1
a = 2
`))
		out := make([]string, 0, len(doc.Sources))
		for k := range doc.Sources {
			out = append(out, k)
		}
		return out
	}

	// Two builds of the same input must produce the same key set. Map iteration
	// is randomized, so any unsorted walk shows up as a different length only
	// if keys are lost; duplicates would show as a longer one.
	first := build()
	second := build()
	if len(first) != len(second) {
		t.Fatalf("source paths differ between runs: %d vs %d", len(first), len(second))
	}
	for _, k := range first {
		found := false
		for _, o := range second {
			if k == o {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("path %q present in one run and not the other", k)
		}
	}
}

// TestDuplicateOutboundErrorNamesExactLines verifies the full shape of the
// duplicate-tag message, not just that it is an error.
//
// The requirement is that both source lines are named. A message that says
// "duplicate tag us-node" and names one file leaves the user hunting for the
// other definition across every fragment.
func TestDuplicateOutboundErrorNamesExactLines(t *testing.T) {
	_, err := FromFiles([]ir.File{
		docFile("010-nodes.toml", `# Primary node list.

[[outbounds]]
tag = "us-node"
type = "vless"
server = "us-a.example"
`),
		docFile("020-more.toml", `# Reuses the tag by mistake.

[[outbounds]]
tag = "us-node"
type = "trojan"
server = "us-b.example"
`),
	})

	if err == nil {
		t.Fatal("duplicate outbound tag was accepted")
	}

	msg := err.Error()
	// The reported line holds the tag itself, which is the line the user has
	// to edit. Both fixtures below have a leading comment and a blank line, so
	// the tag sits on line 4 of each.
	for _, want := range []string{
		"020-more.toml:4",
		"010-nodes.toml:4",
		"us-node",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q:\n%s", want, msg)
		}
	}

	// The related source must appear exactly once, so the message reads as
	// two definitions rather than three.
	if n := strings.Count(msg, "also defined at"); n != 1 {
		t.Errorf("related source rendered %d times, want exactly 1:\n%s", n, msg)
	}
}

// TestDuplicateOutboundInOneFileNamesOneLine verifies the single-file case,
// where the same file supplies both definitions.
func TestDuplicateOutboundInOneFileNamesOneLine(t *testing.T) {
	_, err := FromFiles([]ir.File{
		docFile("one.toml", `[[outbounds]]
tag = "dup"
type = "direct"

[[outbounds]]
tag = "dup"
type = "block"
`),
	})

	if err == nil {
		t.Fatal("duplicate tag within a single file was accepted")
	}
	if !strings.Contains(err.Error(), "one.toml") {
		t.Fatalf("error does not name the file: %v", err)
	}
}
