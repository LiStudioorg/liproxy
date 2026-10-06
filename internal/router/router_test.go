// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package router

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/metadata"
)

// cfg builds a configuration with the given rules and a fixed outbound set.
func cfg(rules ...ir.Rule) *ir.Config {
	return &ir.Config{
		Outbounds: []ir.Outbound{
			{Tag: "direct", Type: "direct"},
			{Tag: "proxy", Type: "vless"},
			{Tag: "block", Type: "block"},
		},
		ProxyGroups: []ir.ProxyGroup{
			{Name: "auto", Type: "url-test"},
		},
		Rules: rules,
	}
}

// rule is a shorthand for building a rule.
func rule(typ string, payload []string, target string) ir.Rule {
	return ir.Rule{Type: typ, Payload: payload, Target: target}
}

// domainMeta builds metadata for a connection to a named host.
func domainMeta(domain string) metadata.Metadata {
	return metadata.Metadata{
		Network:    "tcp",
		Domain:     domain,
		DestAddr:   &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443},
		InboundTag: "in",
	}
}

// --- matchers -------------------------------------------------------------

// TestDomainSuffixMatching verifies the suffix matcher, including the boundary
// that distinguishes it from a naive HasSuffix.
func TestDomainSuffixMatching(t *testing.T) {
	cases := []struct {
		name    string
		payload []string
		domain  string
		want    bool
	}{
		{"exact match", []string{"example.com"}, "example.com", true},
		{"subdomain", []string{"example.com"}, "a.example.com", true},
		{"deep subdomain", []string{"example.com"}, "a.b.example.com", true},
		{"case insensitive", []string{"Example.COM"}, "a.example.com", true},
		{"leading dot in payload", []string{".example.com"}, "a.example.com", true},
		{"leading dot exact", []string{".example.com"}, "example.com", true},
		{"one of several", []string{"other.com", "example.com"}, "a.example.com", true},
		{"unrelated domain", []string{"example.com"}, "other.com", false},

		// The case a plain HasSuffix gets wrong: "notexample.com" ends with
		// "example.com", but is a different registrable domain. Matching it
		// would let an attacker-controlled name inherit a rule meant for
		// someone else.
		{"suffix without a label boundary", []string{"example.com"}, "notexample.com", false},
		{"prefix overlap", []string{"example.com"}, "myexample.com", false},
		{"suffix as a prefix", []string{"example.com"}, "example.com.evil.net", false},
		{"empty payload", []string{}, "example.com", false},
		{"empty domain", []string{"example.com"}, "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := (domainSuffixMatcher{}).match(rule("domain-suffix", c.payload, "direct"), domainMeta(c.domain))
			if got != c.want {
				t.Errorf("domainSuffix%v against %q = %v, want %v", c.payload, c.domain, got, c.want)
			}
		})
	}
}

// TestDomainKeywordMatching verifies the substring matcher.
func TestDomainKeywordMatching(t *testing.T) {
	cases := []struct {
		name    string
		payload []string
		domain  string
		want    bool
	}{
		{"keyword inside", []string{"ads"}, "ads.example.com", true},
		{"keyword in the middle", []string{"ads"}, "cdn.ads.example.com", true},
		{"keyword at the end", []string{"ads"}, "example-ads", true},
		{"case insensitive", []string{"ADS"}, "cdn.ads.example.com", true},
		{"one of several", []string{"track", "ads"}, "cdn.ads.example.com", true},
		{"absent", []string{"ads"}, "example.com", false},
		{"empty payload", []string{}, "ads.example.com", false},
		{"empty keyword ignored", []string{""}, "issue.example", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := (domainKeywordMatcher{}).match(rule("domain-keyword", c.payload, "direct"), domainMeta(c.domain))
			if got != c.want {
				t.Errorf("domainKeyword%v against %q = %v, want %v", c.payload, c.domain, got, c.want)
			}
		})
	}
}

// TestFinalMatchesEverything verifies that the final matcher is unconditional.
func TestFinalMatchesEverything(t *testing.T) {
	r := rule("final", nil, "direct")
	metas := []metadata.Metadata{
		domainMeta("anything.example"),
		{Network: "tcp"},
		{DestAddr: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 80}},
		{},
	}
	for i, m := range metas {
		if !(finalMatcher{}).match(r, m) {
			t.Errorf("case %d: final did not match %+v", i, m)
		}
	}
}

// TestDomainRulesSkipIPOnlyConnections verifies that a domain rule does not
// match a connection carrying only an address.
//
// The alternative — matching the address text against the domain patterns —
// would produce accidental matches and, worse, would be invisible in tests
// that always set a domain.
func TestDomainRulesSkipIPOnlyConnections(t *testing.T) {
	m := metadata.Metadata{DestAddr: &net.TCPAddr{IP: net.IPv4(93, 184, 216, 34), Port: 443}}

	if (domainSuffixMatcher{}).match(rule("domain-suffix", []string{"34"}, "direct"), m) {
		t.Error("domain-suffix matched an IP-only connection")
	}
	if (domainKeywordMatcher{}).match(rule("domain-keyword", []string{"216"}, "direct"), m) {
		t.Error("domain-keyword matched an IP-only connection")
	}
}

// TestHostOfReturnsDomainOnly verifies that only a real name is matched.
//
// The address is deliberately not a fallback. Substituting it would make the
// address text matchable by domain patterns, so a rule for "34" would fire on
// 93.184.216.34 — a silent, traffic-dependent wrong match.
func TestHostOfReturnsDomainOnly(t *testing.T) {
	m := metadata.Metadata{
		Domain:   "sniffed.example",
		DestAddr: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 443},
	}
	if got := hostOf(m); got != "sniffed.example" {
		t.Errorf("hostOf = %q, want the sniffed domain", got)
	}

	m2 := metadata.Metadata{DestAddr: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 443}}
	if got := hostOf(m2); got != "" {
		t.Errorf("hostOf = %q, want empty for an IP-only connection", got)
	}

	if got := hostOf(metadata.Metadata{}); got != "" {
		t.Errorf("hostOf on empty metadata = %q, want empty", got)
	}
}

// --- routing --------------------------------------------------------------

// TestRouteUsesRuleOrder verifies that the first matching rule wins.
//
// Order is the user's only mechanism for expressing precedence, so a router
// that preferred a later rule would silently invert every exception the user
// wrote.
func TestRouteUsesRuleOrder(t *testing.T) {
	r, err := New(cfg(
		rule("domain-suffix", []string{"example.com"}, "proxy"),
		rule("domain-suffix", []string{"special.example.com"}, "block"),
	))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// special.example.com matches both rules; the earlier one is declared to
	// win. This asserts the mechanism, and the prepend test below asserts the
	// user-facing way to get the other outcome.
	got, err := r.Route(domainMeta("special.example.com"))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got != "proxy" {
		t.Fatalf("Route = %q, want proxy (the first matching rule)", got)
	}
}

// TestRoutePrefersPrependedException verifies the intended way to write an
// exception: the merge moves a prepended rule to the front, so the exception
// is reached first.
func TestRoutePrefersPrependedException(t *testing.T) {
	// This is the order the merger produces for a prepended rule.
	r, err := New(cfg(
		rule("domain-suffix", []string{"special.example.com"}, "block"),
		rule("domain-suffix", []string{"example.com"}, "proxy"),
	))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := r.Route(domainMeta("special.example.com"))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got != "block" {
		t.Fatalf("Route = %q, want block (the exception must be reachable first)", got)
	}
}

// TestRouteFinalFallback verifies that a final rule catches everything.
func TestRouteFinalFallback(t *testing.T) {
	r, err := New(cfg(
		rule("domain-suffix", []string{"example.com"}, "proxy"),
		rule("final", nil, "direct"),
	))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cases := []struct {
		name string
		meta metadata.Metadata
		want string
	}{
		{"matched rule wins", domainMeta("a.example.com"), "proxy"},
		{"unmatched falls through", domainMeta("other.net"), "direct"},
		{"ip-only falls through", metadata.Metadata{DestAddr: &net.TCPAddr{IP: net.IPv4(1, 1, 1, 1), Port: 443}}, "direct"},
		{"empty metadata falls through", metadata.Metadata{}, "direct"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := r.Route(c.meta)
			if err != nil {
				t.Fatalf("Route: %v", err)
			}
			if got != c.want {
				t.Fatalf("Route = %q, want %q", got, c.want)
			}
		})
	}
}

// TestRouteWithoutFinalReportsNoMatch verifies that unmatched traffic is
// reported rather than silently sent somewhere.
//
// Guessing a destination for traffic the configuration did not account for
// can send it through an outbound the user did not intend, which for a proxy
// means leaking traffic they expected to be tunnelled.
func TestRouteWithoutFinalReportsNoMatch(t *testing.T) {
	r, err := New(cfg(rule("domain-suffix", []string{"example.com"}, "proxy")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := r.Route(domainMeta("other.net"))
	if err == nil {
		t.Fatalf("Route returned %q with no error; unmatched traffic must be reported", got)
	}
	if !errors.Is(err, ErrNoMatch) {
		t.Fatalf("err = %v, want ErrNoMatch", err)
	}
}

// TestRouteProxyGroupTarget verifies that a rule may name a proxy group.
func TestRouteProxyGroupTarget(t *testing.T) {
	r, err := New(cfg(
		rule("domain-suffix", []string{"example.com"}, "auto"),
		rule("final", nil, "direct"),
	))
	if err != nil {
		t.Fatalf("New rejected a proxy group target: %v", err)
	}

	got, err := r.Route(domainMeta("a.example.com"))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got != "auto" {
		t.Fatalf("Route = %q, want auto", got)
	}
}

// --- construction errors --------------------------------------------------

// TestUnsupportedMatcherFailsAtConstruction verifies that an unroutable rule
// is rejected when the router is built.
//
// Reporting it on the first matching connection would mean the service had
// already been accepting traffic while unable to route part of it, and the
// failure would appear only for the users whose traffic hit that rule.
func TestUnsupportedMatcherFailsAtConstruction(t *testing.T) {
	_, err := New(cfg(rule("geoip", []string{"cn"}, "direct")))
	if err == nil {
		t.Fatal("an unsupported matcher was accepted")
	}
	if !errors.Is(err, ErrUnsupportedMatcher) {
		t.Fatalf("err = %v, want ErrUnsupportedMatcher", err)
	}
	msg := err.Error()
	for _, want := range []string{"geoip", "domain-suffix", "final"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q: %s", want, msg)
		}
	}
}

// TestRuleWithoutTargetFailsAtConstruction verifies that a targetless rule is
// rejected.
func TestRuleWithoutTargetFailsAtConstruction(t *testing.T) {
	_, err := New(cfg(rule("domain-suffix", []string{"example.com"}, "")))
	if err == nil {
		t.Fatal("a rule with no target was accepted")
	}
	if !strings.Contains(err.Error(), "no target") {
		t.Fatalf("error does not explain the problem: %v", err)
	}
}

// TestRuleTargetingUnknownOutboundFailsAtConstruction verifies that a rule
// pointing at a tag that does not exist is rejected.
func TestRuleTargetingUnknownOutboundFailsAtConstruction(t *testing.T) {
	_, err := New(cfg(rule("domain-suffix", []string{"example.com"}, "typoo")))
	if err == nil {
		t.Fatal("a rule targeting an unknown outbound was accepted")
	}
	if !strings.Contains(err.Error(), "typoo") {
		t.Fatalf("error does not name the missing tag: %v", err)
	}
}

// TestNilConfigRejected verifies the nil guard.
func TestNilConfigRejected(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("a nil configuration was accepted")
	}
}

// TestEmptyRulesIsValid verifies that a configuration with no rules builds,
// since it simply routes nothing.
func TestEmptyRulesIsValid(t *testing.T) {
	r, err := New(cfg())
	if err != nil {
		t.Fatalf("New on an empty rule list: %v", err)
	}
	if r.Len() != 0 {
		t.Fatalf("Len = %d, want 0", r.Len())
	}
	if _, err := r.Route(domainMeta("a.example.com")); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("Route = %v, want ErrNoMatch", err)
	}
}

// TestRouteAllocatesNothing verifies that the matching path does not allocate,
// since it runs once per connection.
//
// AGENTS.md 16 pitfall 4 calls this out explicitly: an allocation per
// connection is a direct cost against the memory targets.
func TestRouteAllocatesNothing(t *testing.T) {
	r, err := New(cfg(
		rule("domain-keyword", []string{"ads"}, "block"),
		rule("domain-suffix", []string{"example.com"}, "proxy"),
		rule("final", nil, "direct"),
	))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	meta := domainMeta("a.example.com")
	if allocs := testing.AllocsPerRun(1000, func() {
		if _, err := r.Route(meta); err != nil {
			t.Fatalf("Route: %v", err)
		}
	}); allocs > 0 {
		t.Fatalf("Route allocated %.1f times per call, want 0", allocs)
	}
}
