// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package compile

import (
	"fmt"
	"sort"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
	"github.com/LiStudioorg/liproxy/internal/config/merge"
)

// convert turns a merged tree into the typed IR.
//
// Every key is read through the same accessors, so a TOML file and a YAML file
// with equivalent content produce identical structures. Nothing here consults
// the source format, which is what makes the two-format equality required by
// AGENTS.md 5 hold by construction rather than by test alone.
func convert(doc *merge.Doc) (*ir.Config, error) {
	root := table{fields: doc.Body}
	cfg := &ir.Config{}

	var err error

	if cfg.Mode, err = decodeStringEnum[ir.Mode](root, "mode"); err != nil {
		return nil, err
	}
	if cfg.Server, err = convertServer(tableAt(root, "server")); err != nil {
		return nil, err
	}
	if cfg.Auth, err = convertAuth(tableAt(root, "auth")); err != nil {
		return nil, err
	}
	if cfg.Log, err = convertLog(tableAt(root, "log")); err != nil {
		return nil, err
	}
	if cfg.Database, err = convertDatabase(tableAt(root, "database")); err != nil {
		return nil, err
	}
	if cfg.DNS, err = convertDNS(tableAt(root, "dns")); err != nil {
		return nil, err
	}
	if cfg.Sniffer, err = convertSniffer(tableAt(root, "sniffer")); err != nil {
		return nil, err
	}
	if cfg.ServerMode, err = convertServerMode(tableAt(root, "server_mode")); err != nil {
		return nil, err
	}
	if cfg.Experimental, err = convertExperimental(tableAt(root, "experimental")); err != nil {
		return nil, err
	}

	if cfg.Inbounds, err = convertList(root, "inbounds", convertInbound); err != nil {
		return nil, err
	}
	if cfg.Outbounds, err = convertList(root, "outbounds", convertOutbound); err != nil {
		return nil, err
	}
	if cfg.ProxyGroups, err = convertList(root, "proxy_groups", convertProxyGroup); err != nil {
		return nil, err
	}
	if cfg.Providers, err = convertList(root, "providers", convertProvider); err != nil {
		return nil, err
	}
	if cfg.RuleSets, err = convertList(root, "rule_sets", convertRuleSet); err != nil {
		return nil, err
	}
	if cfg.Rules, err = convertList(root, "rules", convertRule); err != nil {
		return nil, err
	}

	return cfg, nil
}

// convertList converts a top-level array using one element converter.
//
// It is generic over the element type so that every list follows the same
// shape check and the same error wording; a per-list implementation would
// drift, and the drift would show up as inconsistent messages rather than as a
// test failure.
func convertList[T any](root table, key string, fn func(table) (T, error)) ([]T, error) {
	items, ok, err := root.list(key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	out := make([]T, 0, len(items))
	for i, item := range items {
		m, isTable := item.(map[string]any)
		if !isTable {
			return nil, root.indexErr(key, i, item, "table")
		}
		t := table{path: fmt.Sprintf("%s[%d]", key, i), fields: m}
		v, err := fn(t)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// convertServer converts the server block.
func convertServer(t table) (ir.Server, error) {
	var s ir.Server
	var err error
	if s.Listen, err = t.str("listen"); err != nil {
		return s, err
	}
	if s.Port, err = t.int("port"); err != nil {
		return s, err
	}
	if s.TLS, err = convertTLS(tableAt(t, "tls")); err != nil {
		return s, err
	}
	if s.ReadHeaderTimeout, err = t.duration("read_header_timeout"); err != nil {
		return s, err
	}
	if s.IdleTimeout, err = t.duration("idle_timeout"); err != nil {
		return s, err
	}
	if s.CORS, err = convertCORS(tableAt(t, "cors")); err != nil {
		return s, err
	}
	return s, nil
}

// convertTLS converts a TLS block.
func convertTLS(t table) (ir.TLS, error) {
	var v ir.TLS
	var err error
	if v.Enabled, err = t.bool("enabled"); err != nil {
		return v, err
	}
	if v.Cert, err = t.str("cert"); err != nil {
		return v, err
	}
	if v.Key, err = t.str("key"); err != nil {
		return v, err
	}
	return v, nil
}

// convertCORS converts a CORS block.
func convertCORS(t table) (ir.CORS, error) {
	var v ir.CORS
	var err error
	if v.Enabled, err = t.bool("enabled"); err != nil {
		return v, err
	}
	if v.AllowedOrigins, err = t.strList("allowed_origins"); err != nil {
		return v, err
	}
	return v, nil
}

// convertAuth converts the auth block.
func convertAuth(t table) (ir.Auth, error) {
	var v ir.Auth
	var err error
	if v.Enabled, err = t.bool("enabled"); err != nil {
		return v, err
	}
	if v.Username, err = t.str("username"); err != nil {
		return v, err
	}
	if v.Password, err = t.str("password"); err != nil {
		return v, err
	}
	if v.JWTSecret, err = t.str("jwt_secret"); err != nil {
		return v, err
	}
	if v.SessionTTL, err = t.duration("session_ttl"); err != nil {
		return v, err
	}
	if v.APIKeys, err = convertList(t, "api_keys", convertAPIKey); err != nil {
		return v, err
	}
	return v, nil
}

// convertAPIKey converts one API key entry.
func convertAPIKey(t table) (ir.APIKey, error) {
	var v ir.APIKey
	var err error
	if v.Name, err = t.str("name"); err != nil {
		return v, err
	}
	if v.Key, err = t.str("key"); err != nil {
		return v, err
	}
	if v.Scopes, err = t.strList("scopes"); err != nil {
		return v, err
	}
	if v.ExpiresAt, err = t.time("expires_at"); err != nil {
		return v, err
	}
	return v, nil
}

// convertLog converts the log block.
func convertLog(t table) (ir.Log, error) {
	var v ir.Log
	var err error
	if v.Level, err = t.str("level"); err != nil {
		return v, err
	}
	if v.TimestampFormat, err = t.str("timestamp_format"); err != nil {
		return v, err
	}
	return v, nil
}

// convertDatabase converts the database block.
func convertDatabase(t table) (ir.Database, error) {
	var v ir.Database
	var err error
	if v.Enabled, err = t.bool("enabled"); err != nil {
		return v, err
	}
	if v.Path, err = t.str("path"); err != nil {
		return v, err
	}
	if v.BusyTimeout, err = t.duration("busy_timeout"); err != nil {
		return v, err
	}
	if v.RetentionDays, err = t.int("retention_days"); err != nil {
		return v, err
	}
	return v, nil
}

// convertDNS converts the dns block.
func convertDNS(t table) (ir.DNS, error) {
	var v ir.DNS
	var err error
	if v.Enabled, err = t.bool("enabled"); err != nil {
		return v, err
	}
	if v.Listen, err = t.str("listen"); err != nil {
		return v, err
	}
	if v.Hosts, err = t.strMap("hosts"); err != nil {
		return v, err
	}
	if v.CacheTTL, err = t.duration("cache_ttl"); err != nil {
		return v, err
	}
	if v.IPv6, err = t.boolPtr("ipv6"); err != nil {
		return v, err
	}
	if v.Nameservers, err = convertList(t, "nameservers", convertNameServer); err != nil {
		return v, err
	}
	if v.Fallback, err = convertList(t, "fallback", convertNameServer); err != nil {
		return v, err
	}
	if v.FakeIP, err = convertFakeIP(tableAt(t, "fake_ip")); err != nil {
		return v, err
	}
	return v, nil
}

// convertNameServer converts one nameserver entry.
func convertNameServer(t table) (ir.NameServer, error) {
	var v ir.NameServer
	var err error
	if v.Address, err = t.str("address"); err != nil {
		return v, err
	}
	if v.Tag, err = t.str("tag"); err != nil {
		return v, err
	}
	if v.Domains, err = t.strList("domains"); err != nil {
		return v, err
	}
	if v.Proxy, err = t.str("proxy"); err != nil {
		return v, err
	}
	return v, nil
}

// convertFakeIP converts the fake_ip block.
func convertFakeIP(t table) (ir.FakeIP, error) {
	var v ir.FakeIP
	var err error
	if v.Enabled, err = t.bool("enabled"); err != nil {
		return v, err
	}
	if v.IPv4Range, err = t.str("ipv4_range"); err != nil {
		return v, err
	}
	if v.IPv6Range, err = t.str("ipv6_range"); err != nil {
		return v, err
	}
	if v.Filter, err = t.strList("filter"); err != nil {
		return v, err
	}
	return v, nil
}

// convertInbound converts one inbound entry.
func convertInbound(t table) (ir.Inbound, error) {
	var v ir.Inbound
	var err error
	if v.Tag, err = t.str("tag"); err != nil {
		return v, err
	}
	if v.Type, err = t.str("type"); err != nil {
		return v, err
	}
	if v.Listen, err = t.str("listen"); err != nil {
		return v, err
	}
	if v.Port, err = t.int("port"); err != nil {
		return v, err
	}
	if v.Network, err = t.str("network"); err != nil {
		return v, err
	}
	if v.Sniff, err = t.boolPtr("sniff"); err != nil {
		return v, err
	}
	if v.Users, err = convertList(t, "users", convertUser); err != nil {
		return v, err
	}
	if v.TLS, err = convertTLS(tableAt(t, "tls")); err != nil {
		return v, err
	}
	if v.Transport, err = convertTransport(tableAt(t, "transport")); err != nil {
		return v, err
	}
	if v.Options, err = t.anyMap("options"); err != nil {
		return v, err
	}
	if v.Clash, err = convertClashInbound(tableAt(t, "clash")); err != nil {
		return v, err
	}
	return v, nil
}

// convertUser converts one user entry.
func convertUser(t table) (ir.User, error) {
	var v ir.User
	var err error
	if v.Name, err = t.str("name"); err != nil {
		return v, err
	}
	if v.UUID, err = t.str("uuid"); err != nil {
		return v, err
	}
	if v.Password, err = t.str("password"); err != nil {
		return v, err
	}
	if v.Flow, err = t.str("flow"); err != nil {
		return v, err
	}
	return v, nil
}

// convertClashInbound converts the clash compatibility block.
func convertClashInbound(t table) (ir.ClashInbound, error) {
	var v ir.ClashInbound
	var err error
	if v.Mode, err = t.str("mode"); err != nil {
		return v, err
	}
	return v, nil
}

// convertOutbound converts one outbound entry.
func convertOutbound(t table) (ir.Outbound, error) {
	var v ir.Outbound
	var err error
	if v.Tag, err = t.str("tag"); err != nil {
		return v, err
	}
	if v.Type, err = t.str("type"); err != nil {
		return v, err
	}
	if v.Server, err = t.str("server"); err != nil {
		return v, err
	}
	if v.Port, err = t.int("port"); err != nil {
		return v, err
	}
	if v.UUID, err = t.str("uuid"); err != nil {
		return v, err
	}
	if v.Password, err = t.str("password"); err != nil {
		return v, err
	}
	if v.Username, err = t.str("username"); err != nil {
		return v, err
	}
	if v.Flow, err = t.str("flow"); err != nil {
		return v, err
	}
	if v.Network, err = t.str("network"); err != nil {
		return v, err
	}
	if v.TLS, err = convertTLS(tableAt(t, "tls")); err != nil {
		return v, err
	}
	if v.Transport, err = convertTransport(tableAt(t, "transport")); err != nil {
		return v, err
	}
	if v.Dial, err = convertDial(tableAt(t, "dial")); err != nil {
		return v, err
	}
	if v.Options, err = t.anyMap("options"); err != nil {
		return v, err
	}
	if v.Provider, err = t.str("provider"); err != nil {
		return v, err
	}
	return v, nil
}

// convertDial converts the dial block.
func convertDial(t table) (ir.Dial, error) {
	var v ir.Dial
	var err error
	if v.Timeout, err = t.duration("timeout"); err != nil {
		return v, err
	}
	if v.Interface, err = t.str("interface"); err != nil {
		return v, err
	}
	if v.RoutingMark, err = t.int("routing_mark"); err != nil {
		return v, err
	}
	if v.TCPFastOpen, err = t.bool("tcp_fast_open"); err != nil {
		return v, err
	}
	if v.MPTCP, err = t.bool("mptcp"); err != nil {
		return v, err
	}
	if v.UDPFragment, err = t.bool("udp_fragment"); err != nil {
		return v, err
	}
	return v, nil
}

// convertTransport converts a transport block.
func convertTransport(t table) (ir.Transport, error) {
	var v ir.Transport
	var err error
	if v.Type, err = t.str("type"); err != nil {
		return v, err
	}
	if v.Host, err = t.str("host"); err != nil {
		return v, err
	}
	if v.Path, err = t.str("path"); err != nil {
		return v, err
	}
	if v.Headers, err = t.strMap("headers"); err != nil {
		return v, err
	}
	if v.ServiceName, err = t.str("service_name"); err != nil {
		return v, err
	}
	if v.EarlyDataHeaderName, err = t.str("early_data_header_name"); err != nil {
		return v, err
	}
	return v, nil
}

// convertProxyGroup converts one proxy group entry.
func convertProxyGroup(t table) (ir.ProxyGroup, error) {
	var v ir.ProxyGroup
	var err error
	if v.Name, err = t.str("name"); err != nil {
		return v, err
	}
	if v.Type, err = t.str("type"); err != nil {
		return v, err
	}
	if v.Proxies, err = t.strList("proxies"); err != nil {
		return v, err
	}
	if v.Use, err = t.strList("use"); err != nil {
		return v, err
	}
	if v.URL, err = t.str("url"); err != nil {
		return v, err
	}
	if v.Interval, err = t.duration("interval"); err != nil {
		return v, err
	}
	if v.Tolerance, err = t.duration("tolerance"); err != nil {
		return v, err
	}
	if v.Lazy, err = t.bool("lazy"); err != nil {
		return v, err
	}
	return v, nil
}

// convertRule converts one rule entry.
//
// The position key is not read here: the merger consumes it to decide ordering
// and strips it, so by the time conversion runs it is gone. Reading it back
// would silently reintroduce a merge directive into the IR.
func convertRule(t table) (ir.Rule, error) {
	var v ir.Rule
	var err error
	if v.Type, err = t.str("type"); err != nil {
		return v, err
	}
	if v.Payload, err = t.strList("payload"); err != nil {
		return v, err
	}
	if v.Target, err = t.str("target"); err != nil {
		return v, err
	}
	if v.RuleSet, err = t.str("rule_set"); err != nil {
		return v, err
	}
	if v.NoResolve, err = t.bool("no_resolve"); err != nil {
		return v, err
	}
	if v.Inbound, err = t.strList("inbound"); err != nil {
		return v, err
	}
	if v.Params, err = t.anyMap("params"); err != nil {
		return v, err
	}
	return v, nil
}

// convertRuleSet converts one rule set entry.
func convertRuleSet(t table) (ir.RuleSet, error) {
	var v ir.RuleSet
	var err error
	if v.Name, err = t.str("name"); err != nil {
		return v, err
	}
	if v.Type, err = t.str("type"); err != nil {
		return v, err
	}
	if v.Format, err = t.str("format"); err != nil {
		return v, err
	}
	if v.Path, err = t.str("path"); err != nil {
		return v, err
	}
	if v.URL, err = t.str("url"); err != nil {
		return v, err
	}
	if v.Interval, err = t.duration("interval"); err != nil {
		return v, err
	}
	if v.Proxy, err = t.str("proxy"); err != nil {
		return v, err
	}
	if v.Rules, err = convertList(t, "rules", convertRule); err != nil {
		return v, err
	}
	return v, nil
}

// convertProvider converts one provider entry.
func convertProvider(t table) (ir.Provider, error) {
	var v ir.Provider
	var err error
	if v.Name, err = t.str("name"); err != nil {
		return v, err
	}
	if v.Type, err = t.str("type"); err != nil {
		return v, err
	}
	if v.Format, err = t.str("format"); err != nil {
		return v, err
	}
	if v.URL, err = t.str("url"); err != nil {
		return v, err
	}
	if v.Path, err = t.str("path"); err != nil {
		return v, err
	}
	if v.Interval, err = t.duration("interval"); err != nil {
		return v, err
	}
	if v.Filter, err = t.str("filter"); err != nil {
		return v, err
	}
	if v.ExcludeNames, err = t.strList("exclude_names"); err != nil {
		return v, err
	}
	if v.HealthCheck, err = convertHealthCheck(tableAt(t, "health_check")); err != nil {
		return v, err
	}
	if v.Proxy, err = t.str("proxy"); err != nil {
		return v, err
	}
	if v.Headers, err = t.strMap("headers"); err != nil {
		return v, err
	}
	return v, nil
}

// convertHealthCheck converts a health_check block.
func convertHealthCheck(t table) (ir.HealthCheck, error) {
	var v ir.HealthCheck
	var err error
	if v.Enabled, err = t.bool("enabled"); err != nil {
		return v, err
	}
	if v.URL, err = t.str("url"); err != nil {
		return v, err
	}
	if v.Interval, err = t.duration("interval"); err != nil {
		return v, err
	}
	if v.Timeout, err = t.duration("timeout"); err != nil {
		return v, err
	}
	if v.Lazy, err = t.bool("lazy"); err != nil {
		return v, err
	}
	return v, nil
}

// convertSniffer converts the sniffer block.
func convertSniffer(t table) (ir.Sniffer, error) {
	var v ir.Sniffer
	var err error
	if v.Enabled, err = t.bool("enabled"); err != nil {
		return v, err
	}
	if v.OverrideDestination, err = t.bool("override_destination"); err != nil {
		return v, err
	}
	if v.Timeout, err = t.duration("timeout"); err != nil {
		return v, err
	}
	if v.TLS, err = t.bool("tls"); err != nil {
		return v, err
	}
	if v.HTTP, err = t.bool("http"); err != nil {
		return v, err
	}
	if v.QUIC, err = t.bool("quic"); err != nil {
		return v, err
	}
	if v.SkipDomain, err = t.strList("skip_domain"); err != nil {
		return v, err
	}
	return v, nil
}

// convertServerMode converts the server_mode block.
func convertServerMode(t table) (ir.ServerMode, error) {
	var v ir.ServerMode
	var err error
	if v.Users, err = convertList(t, "users", convertServerUser); err != nil {
		return v, err
	}
	if v.ACME, err = convertACME(tableAt(t, "acme")); err != nil {
		return v, err
	}
	if v.Subscription, err = convertSubscription(tableAt(t, "subscription")); err != nil {
		return v, err
	}
	return v, nil
}

// convertServerUser converts one server-mode user entry.
func convertServerUser(t table) (ir.ServerUser, error) {
	var v ir.ServerUser
	var err error
	if v.Name, err = t.str("name"); err != nil {
		return v, err
	}
	if v.UUID, err = t.str("uuid"); err != nil {
		return v, err
	}
	if v.Password, err = t.str("password"); err != nil {
		return v, err
	}
	if v.Level, err = t.int("level"); err != nil {
		return v, err
	}
	if v.QuotaBytes, err = t.int64("quota_bytes"); err != nil {
		return v, err
	}
	if v.ExpiresAt, err = t.time("expires_at"); err != nil {
		return v, err
	}
	if v.Inbounds, err = t.strList("inbounds"); err != nil {
		return v, err
	}
	return v, nil
}

// convertACME converts the acme block.
func convertACME(t table) (ir.ACME, error) {
	var v ir.ACME
	var err error
	if v.Enabled, err = t.bool("enabled"); err != nil {
		return v, err
	}
	if v.Domains, err = t.strList("domains"); err != nil {
		return v, err
	}
	if v.Email, err = t.str("email"); err != nil {
		return v, err
	}
	if v.Provider, err = t.str("provider"); err != nil {
		return v, err
	}
	if v.CAServer, err = t.str("ca_server"); err != nil {
		return v, err
	}
	return v, nil
}

// convertSubscription converts the subscription block.
func convertSubscription(t table) (ir.Subscription, error) {
	var v ir.Subscription
	var err error
	if v.Enabled, err = t.bool("enabled"); err != nil {
		return v, err
	}
	if v.Path, err = t.str("path"); err != nil {
		return v, err
	}
	if v.Formats, err = t.strList("formats"); err != nil {
		return v, err
	}
	if v.BaseURL, err = t.str("base_url"); err != nil {
		return v, err
	}
	return v, nil
}

// convertExperimental converts the experimental block.
func convertExperimental(t table) (ir.Experimental, error) {
	var v ir.Experimental
	var err error
	if v.QUIC, err = t.boolPtr("quic"); err != nil {
		return v, err
	}
	if v.CacheFile, err = t.str("cache_file"); err != nil {
		return v, err
	}
	return v, nil
}

// sortedKeys returns a map's keys in byte-wise order, so that iteration for
// error and provenance purposes is deterministic.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
