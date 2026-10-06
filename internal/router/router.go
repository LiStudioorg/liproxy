// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package router

import (
	"fmt"
	"strings"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/metadata"
	"github.com/LiStudioorg/liproxy/internal/registry"
)

// matcher decides whether one rule applies to a connection.
//
// It is an interface rather than a switch inside the match loop so that adding
// a matcher is adding a function, and so that the set of supported matchers is
// derived from this map rather than duplicated in a validation list that can
// drift from the implementation.
type matcher interface {
	// name is the rule type string, matching ir.Rule.Type.
	name() string

	// match reports whether the rule applies to this connection.
	match(r ir.Rule, meta metadata.Metadata) bool
}

// matchers maps a rule type name to its implementation.
//
// A rule whose type is absent from this map is rejected when the router is
// built. This version supports the three types needed for a working
// end-to-end path; everything else is reported rather than ignored.
var matchers = map[string]matcher{
	"domain-suffix":  domainSuffixMatcher{},
	"domain-keyword": domainKeywordMatcher{},
	"final":          finalMatcher{},
}

// domainSuffixMatcher matches a domain equal to a listed suffix or ending in
// ".suffix".
//
// The leading-dot requirement is what stops "notexample.com" from matching a
// rule for "example.com". A plain HasSuffix check is a common and serious bug
// here: it lets an attacker-controlled domain match a rule intended for
// someone else's, which in a proxy can mean bypassing a blocking rule.
type domainSuffixMatcher struct{}

func (domainSuffixMatcher) name() string { return "domain-suffix" }

func (domainSuffixMatcher) match(r ir.Rule, meta metadata.Metadata) bool {
	host := strings.ToLower(hostOf(meta))
	if host == "" {
		return false
	}
	for _, raw := range r.Payload {
		suffix := strings.ToLower(strings.TrimPrefix(raw, "."))
		if suffix == "" {
			continue
		}
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// domainKeywordMatcher matches a domain containing a listed substring.
type domainKeywordMatcher struct{}

func (domainKeywordMatcher) name() string { return "domain-keyword" }

func (domainKeywordMatcher) match(r ir.Rule, meta metadata.Metadata) bool {
	host := strings.ToLower(hostOf(meta))
	if host == "" {
		return false
	}
	for _, raw := range r.Payload {
		keyword := strings.ToLower(raw)
		if keyword != "" && strings.Contains(host, keyword) {
			return true
		}
	}
	return false
}

// finalMatcher matches every connection.
//
// A final rule ignores its payload because it has nothing to test; it is a
// position in the list, not a condition.
type finalMatcher struct{}

func (finalMatcher) name() string { return "final" }

func (finalMatcher) match(ir.Rule, metadata.Metadata) bool { return true }

// hostOf returns the domain a rule should match against.
//
// Only a real name counts. When no domain is known — the connection arrived
// addressed to a literal IP and sniffing recovered nothing — this returns
// empty and every domain matcher declines.
//
// Falling back to the destination address's text is the tempting alternative
// and it is wrong. It makes "34" a keyword match for 93.184.216.34 and "1.1"
// a suffix match for 1.1.1.1, so a rule written against a domain would fire on
// whichever addresses happen to contain those characters. The failure is both
// silent and traffic-dependent, which is the worst combination.
func hostOf(meta metadata.Metadata) string {
	return meta.Domain
}

// Router selects an outbound for a connection.
type Router struct {
	// rules are the compiled rules in evaluation order. The order is the
	// user's, because rule order is semantically significant: a prepended
	// exception has to be reachable before the rule it excepts.
	rules []compiledRule

	// tags is the set of outbound tags the configuration declared.
	//
	// It is kept so that NewOutbound can be asked for a tag that does not
	// exist during construction rather than during the first connection that
	// selects it. A rule pointing at a typo is a configuration error.
	tags map[string]bool
}

// compiledRule pairs a rule with the matcher that evaluates it.
type compiledRule struct {
	rule    ir.Rule
	target  string
	matcher matcher
}

// New builds a router from the compiled configuration.
//
// It reports every problem it can find before any connection arrives: an
// unsupported matcher type, a rule with no target, and a target that names no
// declared outbound. These are all configuration errors, and configuration
// errors belong at startup — a router that was built and then fails on the
// first matching connection has already accepted traffic it could not serve.
//
// When the configuration has no final rule, New does not invent one. Route
// reports ErrNoMatch instead, so the decision to drop traffic stays the user's.
func New(cfg *ir.Config) (*Router, error) {
	if cfg == nil {
		return nil, fmt.Errorf("router: nil configuration")
	}

	tags := make(map[string]bool, len(cfg.Outbounds)+len(cfg.ProxyGroups))
	for _, ob := range cfg.Outbounds {
		tags[ob.Tag] = true
	}
	// A proxy group is a routing target like any single outbound: rules name
	// it, and which member it resolves to is the group's business.
	for _, g := range cfg.ProxyGroups {
		tags[g.Name] = true
	}

	r := &Router{tags: tags}
	r.rules = make([]compiledRule, 0, len(cfg.Rules))

	for i, rule := range cfg.Rules {
		m, ok := matchers[rule.Type]
		if !ok {
			return nil, fmt.Errorf("%w: rule %d has type %q, which this version cannot evaluate (supported: %s)",
				ErrUnsupportedMatcher, i, rule.Type, supportedMatchers())
		}
		if rule.Target == "" {
			return nil, fmt.Errorf("router: rule %d (type %s) has no target", i, rule.Type)
		}
		if !tags[rule.Target] {
			return nil, fmt.Errorf("router: rule %d (type %s) targets outbound %q, which is not defined",
				i, rule.Type, rule.Target)
		}

		r.rules = append(r.rules, compiledRule{
			rule:    rule,
			target:  rule.Target,
			matcher: m,
		})
	}

	return r, nil
}

// Route returns the outbound tag that should serve this connection.
//
// Rules are evaluated in order and the first match wins, which is what makes a
// prepended exception effective. The scan is linear and allocates nothing: no
// copying of rules, no string construction, no slice growth. That matters
// because this runs once per connection and is one of the functions AGENTS.md
// 16 pitfall 4 names as hot.
func (r *Router) Route(meta metadata.Metadata) (string, error) {
	for i := range r.rules {
		cr := &r.rules[i]
		if cr.matcher.match(cr.rule, meta) {
			return cr.target, nil
		}
	}
	return "", fmt.Errorf("%w: no rule matched %s", ErrNoMatch, meta.Destination())
}

// Len returns the number of compiled rules, for diagnostics and tests.
func (r *Router) Len() int { return len(r.rules) }

// supportedMatchers lists the supported types, sorted, for error messages.
func supportedMatchers() string {
	names := make([]string, 0, len(matchers))
	for name := range matchers {
		names = append(names, name)
	}
	// Insertion sort: the list is three entries and this avoids pulling in
	// sort for a message-building helper.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return strings.Join(names, ", ")
}

// Ensure Router satisfies the registry's routing capability.
//
// The assertion is here rather than in the engine so that a signature change
// fails in this package, next to the method, instead of in whichever consumer
// happens to be compiled first.
var _ registry.Router = (*Router)(nil)
