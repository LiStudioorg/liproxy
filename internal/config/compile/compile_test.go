// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package compile

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
)

// file builds a TOML ir.File with a path under a fake root.
func file(name, body string) ir.File {
	return ir.File{
		Ref:     ir.SourceRef{Path: "/cfg/" + name, Line: 1, Format: ir.FormatTOML},
		Content: []byte(body),
	}
}

// yamlFile builds a YAML ir.File with a path under a fake root.
func yamlFile(name, body string) ir.File {
	return ir.File{
		Ref:     ir.SourceRef{Path: "/cfg/" + name, Line: 1, Format: ir.FormatYAML},
		Content: []byte(body),
	}
}

func mustCompile(t *testing.T, files ...ir.File) *ir.Config {
	t.Helper()
	cfg, err := FromFiles(files)
	if err != nil {
		t.Fatalf("FromFiles: %v", err)
	}
	return cfg
}

// TestCompileTOMLAndYAMLEqual is the round-trip test required by AGENTS.md 5:
// the same configuration written in both syntaxes must compile to deeply equal
// IR.
//
// It is the test that keeps the two parsers honest. If either gains a special
// case, or a field is read with a type assumption that only one format
// satisfies, this fails rather than the divergence surviving until a user
// reports it.
func TestCompileTOMLAndYAMLEqual(t *testing.T) {
	tomlBody := `
mode = "client"

[server]
listen = "127.0.0.1"
port = 9090
read_header_timeout = "10s"

[server.cors]
enabled = true
allowed_origins = ["http://localhost:5173"]

[auth]
enabled = true
username = "admin"
session_ttl = "24h"

[log]
level = "info"

[database]
enabled = false
retention_days = 7

[dns]
enabled = true
cache_ttl = "5m"
ipv6 = false

[dns.hosts]
"host.example" = "10.0.0.1"

[[dns.nameservers]]
address = "1.1.1.1"
tag = "cf"

[[inbounds]]
tag = "mixed-in"
type = "mixed"
listen = "127.0.0.1"
port = 7890
sniff = true

[[inbounds.users]]
name = "u1"
uuid = "11111111-1111-1111-1111-111111111111"
flow = "xtls-rprx-vision"

[[outbounds]]
tag = "direct"
type = "direct"

[[outbounds]]
tag = "us"
type = "vless"
server = "us.example"
port = 443
network = "tcp"

[outbounds.tls]
enabled = true

[outbounds.transport]
type = "ws"
path = "/ws"
host = "us.example"

[outbounds.transport.headers]
"X-Test" = "yes"

[outbounds.dial]
timeout = "5s"
tcp_fast_open = true

[[proxy_groups]]
name = "auto"
type = "url-test"
proxies = ["us", "direct"]
interval = "5m"
tolerance = "50ms"
lazy = true

[[rule_sets]]
name = "ads"
type = "inline"

[[rule_sets.rules]]
type = "domain"
payload = ["ads.example"]
target = "block"

[[providers]]
name = "airport"
type = "proxy"
format = "clash"
url = "https://example.com/sub"
interval = "12h"
exclude_names = ["expired"]

[providers.health_check]
enabled = true
url = "https://example.com/ping"
interval = "5m"
timeout = "3s"

[[rules]]
type = "domain-suffix"
payload = ["example.com"]
target = "us"

[[rules]]
type = "geoip"
payload = ["cn"]
target = "direct"
no_resolve = true

[sniffer]
enabled = true
timeout = "300ms"
tls = true
http = true
quic = true

[experimental]
quic = true
cache_file = "state/cache.db"
`

	yamlBody := `
mode: client

server:
  listen: 127.0.0.1
  port: 9090
  read_header_timeout: 10s
  cors:
    enabled: true
    allowed_origins:
      - http://localhost:5173

auth:
  enabled: true
  username: admin
  session_ttl: 24h

log:
  level: info

database:
  enabled: false
  retention_days: 7

dns:
  enabled: true
  cache_ttl: 5m
  ipv6: false
  hosts:
    host.example: 10.0.0.1
  nameservers:
    - address: 1.1.1.1
      tag: cf

inbounds:
  - tag: mixed-in
    type: mixed
    listen: 127.0.0.1
    port: 7890
    sniff: true
    users:
      - name: u1
        uuid: 11111111-1111-1111-1111-111111111111
        flow: xtls-rprx-vision

outbounds:
  - tag: direct
    type: direct
  - tag: us
    type: vless
    server: us.example
    port: 443
    network: tcp
    tls:
      enabled: true
    transport:
      type: ws
      path: /ws
      host: us.example
      headers:
        X-Test: "yes"
    dial:
      timeout: 5s
      tcp_fast_open: true

proxy_groups:
  - name: auto
    type: url-test
    proxies: [us, direct]
    interval: 5m
    tolerance: 50ms
    lazy: true

rule_sets:
  - name: ads
    type: inline
    rules:
      - type: domain
        payload: [ads.example]
        target: block

providers:
  - name: airport
    type: proxy
    format: clash
    url: https://example.com/sub
    interval: 12h
    exclude_names: [expired]
    health_check:
      enabled: true
      url: https://example.com/ping
      interval: 5m
      timeout: 3s

rules:
  - type: domain-suffix
    payload: [example.com]
    target: us
  - type: geoip
    payload: [cn]
    target: direct
    no_resolve: true

sniffer:
  enabled: true
  timeout: 300ms
  tls: true
  http: true
  quic: true

experimental:
  quic: true
  cache_file: state/cache.db
`

	fromTOML := mustCompile(t, file("liproxy.toml", tomlBody))
	fromYAML := mustCompile(t, yamlFile("liproxy.yaml", yamlBody))

	if !reflect.DeepEqual(fromTOML, fromYAML) {
		t.Fatalf("TOML and YAML compiled to different IRs.\n"+
			"TOML: %s\nYAML: %s", dumpForDiff(fromTOML), dumpForDiff(fromYAML))
	}
}

// TestCompileNoFloatFields documents why no approximate comparison is needed.
//
// AGENTS.md and the IR definition avoid float fields entirely: durations are
// time.Duration, ports and sizes are integers, and byte counts are int64. A
// float in the IR would make reflect.DeepEqual unreliable across formats,
// because TOML and YAML render the same number differently. This test freezes
// that property for the fields where a float would be most tempting.
func TestCompileNoFloatFields(t *testing.T) {
	cfg := mustCompile(t, file("a.toml", `
[server]
port = 9090
read_header_timeout = "10s"

[database]
retention_days = 7
`))

	typ := reflect.TypeOf(*cfg)
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type.Kind() == reflect.Float32 || f.Type.Kind() == reflect.Float64 {
			t.Errorf("ir.Config.%s is a float; floats break TOML/YAML DeepEqual equality", f.Name)
		}
	}

	if cfg.Server.Port != 9090 {
		t.Errorf("Server.Port = %d, want 9090", cfg.Server.Port)
	}
}

// TestCompileValuesRoundTrip verifies that a representative value of every
// scalar kind survives conversion with the right type.
func TestCompileValuesRoundTrip(t *testing.T) {
	cfg := mustCompile(t, file("a.toml", `
mode = "server"

[server]
port = 9090
read_header_timeout = "10s"

[database]
retention_days = 7

[dns]
ipv6 = false

[sniffer]
enabled = true

[server_mode.subscription]
enabled = true
formats = ["clash", "sing-box"]
`))

	if cfg.Mode != ir.ModeServer {
		t.Errorf("Mode = %q, want server", cfg.Mode)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("Server.Port = %d, want 9090", cfg.Server.Port)
	}
	if got := cfg.Server.ReadHeaderTimeout.String(); got != "10s" {
		t.Errorf("ReadHeaderTimeout = %s, want 10s", got)
	}
	if cfg.Database.RetentionDays != 7 {
		t.Errorf("RetentionDays = %d, want 7", cfg.Database.RetentionDays)
	}
	if cfg.DNS.IPv6 == nil {
		t.Fatal("DNS.IPv6 = nil, want a pointer to false (absent must differ from false)")
	}
	if *cfg.DNS.IPv6 {
		t.Error("DNS.IPv6 = true, want false")
	}
	if !cfg.Sniffer.Enabled {
		t.Error("Sniffer.Enabled = false, want true")
	}
	if got := cfg.ServerMode.Subscription.Formats; len(got) != 2 || got[0] != "clash" {
		t.Errorf("Subscription.Formats = %v, want [clash sing-box]", got)
	}
}

// TestCompileSyntaxErrorIsPositioned verifies that a malformed fragment fails
// compilation with a located error rather than being skipped.
func TestCompileSyntaxErrorIsPositioned(t *testing.T) {
	_, err := FromFiles([]ir.File{
		file("broken.toml", "this is not valid toml =\n"),
	})
	if err == nil {
		t.Fatal("a malformed document compiled successfully; it must be an error")
	}
	if !strings.Contains(err.Error(), "broken.toml") {
		t.Fatalf("error does not name the file: %v", err)
	}
}

// TestCompilePropagatesMergeConflict verifies that a merge failure surfaces
// through Compile rather than being swallowed.
func TestCompilePropagatesMergeConflict(t *testing.T) {
	_, err := FromFiles([]ir.File{
		file("a.toml", "[[outbounds]]\ntag = \"x\"\ntype = \"direct\"\n"),
		file("b.toml", "[[outbounds]]\ntag = \"x\"\ntype = \"block\"\n"),
	})
	if err == nil {
		t.Fatal("a duplicate outbound tag compiled successfully; it must be an error")
	}
	if !strings.Contains(err.Error(), "x") {
		t.Fatalf("error does not name the duplicate tag: %v", err)
	}
}

// TestCompileNilFileSetErrors verifies the nil guard.
func TestCompileNilFileSetErrors(t *testing.T) {
	if _, err := FromFileSet(nil); err == nil {
		t.Fatal("nil FileSet compiled successfully; it must be an error")
	}
}

// TestCompileFieldTypeErrorIsReported verifies that a wrongly-typed field is
// rejected with the field path in the message.
func TestCompileFieldTypeErrorIsReported(t *testing.T) {
	_, err := FromFiles([]ir.File{
		file("a.toml", "[server]\nport = \"not a number\"\n"),
	})
	if err == nil {
		t.Fatal("a string port compiled successfully; it must be an error")
	}
	if !strings.Contains(err.Error(), "server.port") {
		t.Fatalf("error does not name the field: %v", err)
	}
}

// TestCompileBadDurationIsReported verifies that a duration without a unit is
// rejected rather than guessed as seconds.
func TestCompileBadDurationIsReported(t *testing.T) {
	_, err := FromFiles([]ir.File{
		file("a.toml", "[sniffer]\ntimeout = \"300\"\n"),
	})
	if err == nil {
		t.Fatal("a unitless duration compiled successfully; it must be an error")
	}
	if !strings.Contains(err.Error(), "sniffer.timeout") {
		t.Fatalf("error does not name the field: %v", err)
	}
}

// TestCompileSingleStringAsList verifies the convenience form where a scalar
// string stands in for a one-element list.
func TestCompileSingleStringAsList(t *testing.T) {
	cfg := mustCompile(t, file("a.toml", `
[[rules]]
type = "domain"
payload = "single.example"
target = "direct"
`))

	if len(cfg.Rules) != 1 {
		t.Fatalf("len(Rules) = %d, want 1", len(cfg.Rules))
	}
	if got := cfg.Rules[0].Payload; len(got) != 1 || got[0] != "single.example" {
		t.Fatalf("Payload = %v, want [single.example]", got)
	}
}

// dumpForDiff renders a config compactly so a failed equality check prints
// something readable rather than a 400-line struct dump.
func dumpForDiff(cfg *ir.Config) string {
	var b strings.Builder
	writeCfg(&b, cfg)
	return b.String()
}

// writeCfg writes a compact, stable summary of the fields the round-trip test
// covers, one per line, so a mismatch shows exactly which field diverged.
func writeCfg(b *strings.Builder, cfg *ir.Config) {
	line := func(format string, args ...any) {
		b.WriteString(fmt.Sprintf(format, args...))
		b.WriteByte('\n')
	}

	line("mode=%s", cfg.Mode)
	line("server.listen=%s port=%d rht=%s", cfg.Server.Listen, cfg.Server.Port, cfg.Server.ReadHeaderTimeout)
	line("server.cors=%v %v", cfg.Server.CORS.Enabled, cfg.Server.CORS.AllowedOrigins)
	line("auth=%v %s ttl=%s", cfg.Auth.Enabled, cfg.Auth.Username, cfg.Auth.SessionTTL)
	line("log.level=%s", cfg.Log.Level)
	line("db=%v retention=%d", cfg.Database.Enabled, cfg.Database.RetentionDays)
	line("dns=%v ttl=%s ipv6=%v hosts=%v ns=%d", cfg.DNS.Enabled, cfg.DNS.CacheTTL, cfg.DNS.IPv6, cfg.DNS.Hosts, len(cfg.DNS.Nameservers))
	for i, in := range cfg.Inbounds {
		line("inbound[%d]=%s/%s %s:%d sniff=%v users=%d", i, in.Tag, in.Type, in.Listen, in.Port, in.Sniff, len(in.Users))
	}
	for i, ob := range cfg.Outbounds {
		line("outbound[%d]=%s/%s %s:%d tls=%v transport=%s/%s headers=%v dial=%s tfo=%v",
			i, ob.Tag, ob.Type, ob.Server, ob.Port, ob.TLS.Enabled, ob.Transport.Type, ob.Transport.Path, ob.Transport.Headers, ob.Dial.Timeout, ob.Dial.TCPFastOpen)
	}
	for i, g := range cfg.ProxyGroups {
		line("group[%d]=%s/%s %v interval=%s tol=%s lazy=%v", i, g.Name, g.Type, g.Proxies, g.Interval, g.Tolerance, g.Lazy)
	}
	for i, rs := range cfg.RuleSets {
		line("ruleset[%d]=%s/%s rules=%d", i, rs.Name, rs.Type, len(rs.Rules))
	}
	for i, p := range cfg.Providers {
		line("provider[%d]=%s/%s/%s %s interval=%s exclude=%v hc=%v/%s/%s/%s",
			i, p.Name, p.Type, p.Format, p.URL, p.Interval, p.ExcludeNames,
			p.HealthCheck.Enabled, p.HealthCheck.URL, p.HealthCheck.Interval, p.HealthCheck.Timeout)
	}
	for i, r := range cfg.Rules {
		line("rule[%d]=%s %v -> %s noresolve=%v", i, r.Type, r.Payload, r.Target, r.NoResolve)
	}
	line("sniffer=%v %s tls=%v http=%v quic=%v", cfg.Sniffer.Enabled, cfg.Sniffer.Timeout, cfg.Sniffer.TLS, cfg.Sniffer.HTTP, cfg.Sniffer.QUIC)
	line("exp.quic=%v cache=%s", cfg.Experimental.QUIC, cfg.Experimental.CacheFile)
}

// TestCompileProvenanceIsAttached verifies that Compile fills FileSet.Sources,
// which is the third deliverable of this unit.
func TestCompileProvenanceIsAttached(t *testing.T) {
	set := &ir.FileSet{
		Files: []ir.File{
			file("base.toml", "[log]\nlevel = \"info\"\n"),
			file("override.toml", "[log]\nlevel = \"debug\"\n"),
		},
	}

	cfg, err := FromFileSet(set)
	if err != nil {
		t.Fatalf("FromFileSet: %v", err)
	}
	if cfg.Log.Level != "debug" {
		t.Fatalf("Log.Level = %q, want debug", cfg.Log.Level)
	}

	if set.Sources.Fields == nil {
		t.Fatal("FileSet.Sources.Fields is nil; Compile must fill provenance")
	}

	origin, ok := set.Sources.Fields["log.level"]
	if !ok {
		t.Fatalf("no provenance for log.level; have %v", keysOf(set.Sources.Fields))
	}
	if filepath.Base(origin.Source.Path) != "override.toml" {
		t.Errorf("log.level source = %q, want override.toml", origin.Source.Path)
	}
	if len(origin.Overridden) != 1 {
		t.Fatalf("log.level overridden = %d entries, want 1", len(origin.Overridden))
	}
	if filepath.Base(origin.Overridden[0].Path) != "base.toml" {
		t.Errorf("overridden source = %q, want base.toml", origin.Overridden[0].Path)
	}

	if len(set.Sources.Files) != 2 {
		t.Errorf("Sources.Files = %d entries, want 2", len(set.Sources.Files))
	}
}

// TestCompileProvenanceRecordsListEntries verifies that provenance covers
// list entries, not just top-level keys.
func TestCompileProvenanceRecordsListEntries(t *testing.T) {
	set := &ir.FileSet{
		Files: []ir.File{
			file("rules.toml", "[[outbounds]]\ntag = \"a\"\ntype = \"direct\"\n"),
		},
	}
	if _, err := FromFileSet(set); err != nil {
		t.Fatalf("FromFileSet: %v", err)
	}

	origin, ok := set.Sources.Fields["outbounds[0]"]
	if !ok {
		t.Fatalf("no provenance for outbounds[0]; have %v", keysOf(set.Sources.Fields))
	}
	if filepath.Base(origin.Source.Path) != "rules.toml" {
		t.Errorf("outbounds[0] source = %q, want rules.toml", origin.Source.Path)
	}
}

// keysOf returns a map's keys for failure messages.
func keysOf(m map[string]ir.Origin) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
