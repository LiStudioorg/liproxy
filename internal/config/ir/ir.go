// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package ir

import "time"

// Mode selects the top-level routing posture of the process.
type Mode string

const (
	// ModeClient runs as a client-side proxy: inbound listeners accept local
	// traffic and route it through outbounds. This is the Mihomo-aligned mode
	// and the only one that needs no database.
	ModeClient Mode = "client"

	// ModeServer runs as a server-side node: users, quotas, ACME and
	// subscription generation are active. This is the sing-box-aligned mode.
	ModeServer Mode = "server"

	// ModeTun runs as a transparent gateway with a TUN device.
	ModeTun Mode = "tun"

	// ModeDNS runs only the DNS server component.
	ModeDNS Mode = "dns"
)

// Config is the root of the Intermediate Representation.
//
// It is the single in-memory form of a LiProxy configuration: TOML and YAML
// are parsed into it, the engine is built from it, and import/export work on
// it. Every field here is the merged result of the loading chain, with the
// comments and file ordering of the sources already resolved away.
//
// A compiled Config is immutable. Hot reload compiles a new Config and swaps
// it in wholesale rather than mutating the existing one, which is what lets
// readers run without locks (AGENTS.md 5).
type Config struct {
	// Mode selects which subsystems are active. It is the one field that
	// changes the meaning of the others, so validation runs against it first.
	Mode Mode `json:"mode,omitempty"`

	// Server holds the process-level parameters: where the Web UI listens and
	// how it is secured. These are the fields a user is most likely to set in
	// the main config rather than in a fragment.
	Server Server `json:"server,omitempty"`

	// Auth holds Web/API authentication. When Auth.Enabled is false, Listen
	// must stay on a loopback address or startup is refused (AGENTS.md 14).
	Auth Auth `json:"auth,omitempty"`

	// Log holds the logging parameters. Only the level is configurable:
	// LiProxy writes to stdout and never to a file, so there is deliberately
	// no path or rotation field here (AGENTS.md 4 and 6).
	Log Log `json:"log,omitempty"`

	// Database holds the optional runtime-state store. When Enabled is false
	// no database is instantiated at all, and sessions, audit records and
	// traffic counters degrade to in-memory behaviour (AGENTS.md 5).
	Database Database `json:"database,omitempty"`

	// DNS holds the resolver configuration used for outbound name lookups
	// and for the built-in DNS server.
	DNS DNS `json:"dns,omitempty"`

	// Inbounds are the listeners this process accepts traffic on. The slice
	// is appended across fragments, never replaced (AGENTS.md 21.4).
	Inbounds []Inbound `json:"inbounds,omitempty"`

	// Outbounds are the upstream proxies and direct/block terminals that
	// rules select between. Tags must be unique across all sources; a
	// duplicate is an error, not an override (AGENTS.md 21.4).
	Outbounds []Outbound `json:"outbounds,omitempty"`

	// ProxyGroups are named collections of outbounds with a selection
	// strategy, referenced by rules exactly like a single outbound.
	ProxyGroups []ProxyGroup `json:"proxy_groups,omitempty"`

	// Rules are the routing rules, evaluated in order against each
	// connection. Their order is semantically significant.
	Rules []Rule `json:"rules,omitempty"`

	// Providers are external sources of nodes or rule sets, refreshed on a
	// schedule and merged into Outbounds and RuleSets at runtime.
	Providers []Provider `json:"providers,omitempty"`

	// RuleSets are named, reusable groups of rules that Rules may reference
	// by name instead of inlining every match condition.
	RuleSets []RuleSet `json:"rule_sets,omitempty"`

	// Sniffer controls protocol sniffing, which recovers domain names from
	// connections whose destination is a bare IP and lets domain-based rules
	// apply to them.
	Sniffer Sniffer `json:"sniffer,omitempty"`

	// ServerMode holds the multi-user, quota, ACME and subscription settings
	// used only when Mode is ModeServer.
	ServerMode ServerMode `json:"server_mode,omitempty"`

	// Experimental holds opt-in features whose behaviour may still change.
	// Anything promoted to stable moves out of here rather than being relied
	// upon in place.
	Experimental Experimental `json:"experimental,omitempty"`
}

// Server holds the Web UI and API listener parameters.
type Server struct {
	// Listen is the address the HTTP server binds to. It is a bare address,
	// without a port; Port carries the port separately so that the two can be
	// overridden independently by different fragments.
	Listen string `json:"listen,omitempty"`

	// Port is the TCP port of the HTTP server.
	Port int `json:"port,omitempty"`

	// TLS holds the optional HTTPS settings for the Web UI. Terminating TLS
	// here is convenient for a self-contained deployment, but a reverse proxy
	// is the recommended setup for anything exposed to a network.
	TLS TLS `json:"tls,omitempty"`

	// ReadHeaderTimeout is how long the server waits for request headers.
	// Zero means the built-in default, which is deliberately finite to avoid
	// being trivially slow-lorisable.
	ReadHeaderTimeout time.Duration `json:"read_header_timeout,omitempty"`

	// IdleTimeout bounds how long an idle keep-alive connection is retained.
	IdleTimeout time.Duration `json:"idle_timeout,omitempty"`

	// CORS holds cross-origin settings. They matter only when the Web UI is
	// served from a different origin than the API.
	CORS CORS `json:"cors,omitempty"`
}

// TLS holds certificate material for a TLS-terminating listener.
type TLS struct {
	// Enabled turns on TLS for the listener this block belongs to.
	Enabled bool `json:"enabled,omitempty"`

	// Cert is the filesystem path to the PEM-encoded certificate chain.
	Cert string `json:"cert,omitempty"`

	// Key is the filesystem path to the PEM-encoded private key.
	Key string `json:"key,omitempty"`
}

// CORS holds cross-origin resource sharing settings for the API.
type CORS struct {
	// Enabled turns on CORS header emission.
	Enabled bool `json:"enabled,omitempty"`

	// AllowedOrigins lists the origins permitted to call the API. A wildcard
	// is intentionally not implied by Enabled, because the API carries
	// credentials.
	AllowedOrigins []string `json:"allowed_origins,omitempty"`
}

// Auth holds authentication settings for the Web UI and the API.
type Auth struct {
	// Enabled turns on authentication. Turning it off is only permitted while
	// the listener stays on a loopback address (AGENTS.md 14).
	Enabled bool `json:"enabled,omitempty"`

	// Username is the administrator account name.
	Username string `json:"username,omitempty"`

	// Password is the plaintext password as written in the config file.
	//
	// It is never logged and never copied into the IR beyond this field.
	// An empty value means "keep the stored password hash", which is how a
	// user avoids writing the password into a backup-visible file.
	Password string `json:"password,omitempty"`

	// JWTSecret is the signing key for session tokens.
	//
	// Leaving it empty makes LiProxy generate and persist one, so that
	// sessions survive a restart without the user managing a secret by hand.
	JWTSecret string `json:"jwt_secret,omitempty"`

	// SessionTTL is how long a session token stays valid.
	SessionTTL time.Duration `json:"session_ttl,omitempty"`

	// APIKeys are long-lived credentials for automation, each with its own
	// scope set and optional expiry (AGENTS.md 10).
	APIKeys []APIKey `json:"api_keys,omitempty"`
}

// APIKey is a single automation credential.
type APIKey struct {
	// Name identifies the key in listings and audit records.
	Name string `json:"name,omitempty"`

	// Key is the secret value. It is shown once on creation and stored only
	// as a hash afterwards (AGENTS.md 14).
	Key string `json:"key,omitempty"`

	// Scopes are the permissions granted to this key, such as
	// "config:read" or "config:write".
	Scopes []string `json:"scopes,omitempty"`

	// ExpiresAt is when the key stops being accepted. A zero value means the
	// key does not expire, which should be the exception rather than the rule.
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// Log holds logging parameters.
//
// There is deliberately no file path or rotation setting: LiProxy writes
// structured logs to stdout and leaves collection to the deployment layer
// (AGENTS.md 4, 6 and 16 pitfall 11).
type Log struct {
	// Level is one of "debug", "info", "warn" or "error".
	Level string `json:"level,omitempty"`

	// TimestampFormat overrides how timestamps are rendered. It exists for
	// readability during local debugging; the default is RFC 3339 so that
	// log shippers parse it without extra configuration.
	TimestampFormat string `json:"timestamp_format,omitempty"`
}

// Database holds the optional runtime-state store.
//
// Nothing in this block affects configuration correctness: the configuration
// lives in files, and the database is a side channel (AGENTS.md 5).
type Database struct {
	// Enabled turns the store on. When false the database is never opened,
	// which is what makes pure client mode work with no persistent state.
	Enabled bool `json:"enabled,omitempty"`

	// Path is the SQLite file location, usually under state/.
	Path string `json:"path,omitempty"`

	// BusyTimeout is how long a writer waits for a lock before failing.
	// SQLite has a single writer, so a small timeout avoids spurious errors
	// when the metrics flush and a user request collide.
	BusyTimeout time.Duration `json:"busy_timeout,omitempty"`

	// RetentionDays bounds how long traffic_logs rows are kept. Audit logs
	// are explicitly *not* subject to it: they are append-only and must not
	// be silently pruned (AGENTS.md 14).
	RetentionDays int `json:"retention_days,omitempty"`
}

// DNS holds resolver configuration for outbound lookups and, when server
// mode is active, for the built-in DNS server.
type DNS struct {
	// Enabled turns on the internal resolver path. When false, names are
	// resolved by the host resolver and rules keyed on domains still work
	// via sniffing.
	Enabled bool `json:"enabled,omitempty"`

	// Listen is the address the built-in DNS server binds to, used in the
	// dns and tun modes.
	Listen string `json:"listen,omitempty"`

	// Nameservers are the upstream resolvers, in preference order.
	Nameservers []NameServer `json:"nameservers,omitempty"`

	// Hosts are static name-to-address mappings applied before any upstream
	// query, so they work with no network at all.
	Hosts map[string]string `json:"hosts,omitempty"`

	// Fallback nameservers are consulted when the primary ones return no
	// usable answer, typically to survive poisoned responses.
	Fallback []NameServer `json:"fallback,omitempty"`

	// FakeIP controls the synthetic-address scheme that lets a connection be
	// routed by domain before the real lookup happens.
	FakeIP FakeIP `json:"fake_ip,omitempty"`

	// CacheTTL bounds how long a resolved answer is reused.
	CacheTTL time.Duration `json:"cache_ttl,omitempty"`

	// IPv6 enables AAAA lookups. Disabling it makes IPv4-only upstreams
	// quieter by suppressing queries that will never be answered.
	IPv6 *bool `json:"ipv6,omitempty"`
}

// NameServer is one upstream resolver.
type NameServer struct {
	// Address is the resolver endpoint. The accepted schemes are plain
	// UDP/TCP addresses plus doh://, dot://, doq:// and tls:// URLs.
	Address string `json:"address,omitempty"`

	// Tag names this resolver so that rules and other blocks can refer to it.
	Tag string `json:"tag,omitempty"`

	// Domains restricts this resolver to the listed suffixes, which is how
	// split-horizon lookups are expressed.
	Domains []string `json:"domains,omitempty"`

	// Proxy routes the lookup through an outbound tag, for resolvers that
	// are only reachable through the proxy itself.
	Proxy string `json:"proxy,omitempty"`
}

// FakeIP holds the synthetic-address settings.
type FakeIP struct {
	// Enabled turns the scheme on.
	Enabled bool `json:"enabled,omitempty"`

	// IPv4Range is the CIDR pool synthetic IPv4 answers are drawn from.
	IPv4Range string `json:"ipv4_range,omitempty"`

	// IPv6Range is the CIDR pool synthetic IPv6 answers are drawn from.
	IPv6Range string `json:"ipv6_range,omitempty"`

	// Filter excludes domains from synthetic answers, so that destinations
	// that must see their real address keep working.
	Filter []string `json:"filter,omitempty"`
}

// Inbound is a listener that accepts traffic into the process.
type Inbound struct {
	// Tag uniquely names this inbound. Duplicates across sources are an
	// error rather than an override (AGENTS.md 21.4).
	Tag string `json:"tag,omitempty"`

	// Type selects the protocol, and is the key the registry uses to build
	// the implementation. Core engine code must never switch on it
	// (AGENTS.md 5 and 16, pitfall 2).
	Type string `json:"type,omitempty"`

	// Listen is the bind address. An empty value combined with an empty Port
	// means "no TCP listener", which is the normal shape for a UDP-only or
	// tun-only inbound.
	Listen string `json:"listen,omitempty"`

	// Port is the listen port.
	Port int `json:"port,omitempty"`

	// Network restricts the listener to "tcp", "udp" or both. An empty value
	// means both.
	Network string `json:"network,omitempty"`

	// Sniff turns protocol sniffing on for connections from this inbound,
	// overriding the global setting in either direction.
	Sniff *bool `json:"sniff,omitempty"`

	// Users are the credentials accepted by this inbound, for protocols that
	// authenticate per user, such as vless or trojan.
	Users []User `json:"users,omitempty"`

	// TLS holds transport security parameters for this listener.
	TLS TLS `json:"tls,omitempty"`

	// Transport selects the stream transport layered under the protocol,
	// such as ws, grpc or http.
	Transport Transport `json:"transport,omitempty"`

	// Options carries protocol-specific settings that are not yet modelled as
	// typed fields. See the package comment: the full protocol field set is
	// tracked as separate work, and this map is the placeholder that keeps
	// unknown-but-valid keys from being rejected while it lands.
	Options map[string]any `json:"options,omitempty"`

	// Clash is the per-listener behaviour overrides for HTTP and SOCKS
	// compatibility, such as header rewriting for LAN clients.
	Clash ClashInbound `json:"clash,omitempty"`
}

// User is one credential accepted by an inbound or granted by a server.
type User struct {
	// Name identifies the user in listings, quotas and audit records.
	Name string `json:"name,omitempty"`

	// UUID is the credential for UUID-authenticated protocols.
	UUID string `json:"uuid,omitempty"`

	// Password is the credential for password-authenticated protocols.
	Password string `json:"password,omitempty"`

	// Flow selects a protocol flow variant, such as xtls-rprx-vision.
	Flow string `json:"flow,omitempty"`
}

// ClashInbound holds HTTP/SOCKS compatibility tweaks for an inbound.
type ClashInbound struct {
	// Mode selects the proxy behaviour for HTTP listeners, either "http" or
	// "socks5", for clients that speak only one of the two.
	Mode string `json:"mode,omitempty"`
}

// Outbound is a destination the router can select for a connection.
type Outbound struct {
	// Tag uniquely names this outbound; rules refer to it by this name.
	Tag string `json:"tag,omitempty"`

	// Type selects the protocol or terminal kind, and is the registry key.
	// The reserved terminals "direct", "block" and "dns" need no dialer.
	Type string `json:"type,omitempty"`

	// Server is the remote host to dial.
	Server string `json:"server,omitempty"`

	// Port is the remote port to dial.
	Port int `json:"port,omitempty"`

	// UUID is the credential for UUID-authenticated protocols.
	UUID string `json:"uuid,omitempty"`

	// Password is the credential for password-authenticated protocols.
	Password string `json:"password,omitempty"`

	// Username is the credential for username-authenticated protocols.
	Username string `json:"username,omitempty"`

	// Flow selects a protocol flow variant, such as xtls-rprx-vision.
	Flow string `json:"flow,omitempty"`

	// Network restricts the outbound to "tcp" or "udp". An empty value means
	// both; protocols that cannot carry one of them say so at build time.
	Network string `json:"network,omitempty"`

	// TLS holds transport security parameters for this destination.
	TLS TLS `json:"tls,omitempty"`

	// Transport selects the stream transport layered under the protocol.
	Transport Transport `json:"transport,omitempty"`

	// Dial holds connection-establishment tunables shared by all protocols.
	Dial Dial `json:"dial,omitempty"`

	// Options carries protocol-specific settings not yet modelled as typed
	// fields. Same rationale and same caveat as Inbound.Options.
	Options map[string]any `json:"options,omitempty"`

	// Provider names the provider this outbound was materialised from, when
	// it came from a subscription rather than from a hand-written file. It is
	// what lets a refresh replace exactly the nodes it owns.
	Provider string `json:"provider,omitempty"`
}

// Dial holds connection-establishment tunables.
type Dial struct {
	// Timeout bounds a single dial attempt, across all its phases.
	Timeout time.Duration `json:"timeout,omitempty"`

	// Interface binds outgoing sockets to a network interface, by name or by
	// address.
	Interface string `json:"interface,omitempty"`

	// RoutingMark sets the fwmark on outgoing sockets, for policy routing.
	RoutingMark int `json:"routing_mark,omitempty"`

	// TCPFastOpen enables TFO where the platform supports it.
	TCPFastOpen bool `json:"tcp_fast_open,omitempty"`

	// MPTCP enables multipath TCP. It is opt-in because middleboxes handle
	// it inconsistently.
	MPTCP bool `json:"mptcp,omitempty"`

	// UDPFragment controls IP fragmentation for UDP, which some networks
	// require for QUIC to pass.
	UDPFragment bool `json:"udp_fragment,omitempty"`
}

// Transport describes the stream transport a protocol runs over.
type Transport struct {
	// Type is "tcp", "ws", "grpc", "http", "quic" or "httpupgrade". An empty
	// value means the protocol's native transport.
	Type string `json:"type,omitempty"`

	// Host is the Host header or authority to present, part of the disguise.
	Host string `json:"host,omitempty"`

	// Path is the request path for HTTP-like transports.
	Path string `json:"path,omitempty"`

	// Headers are extra request headers for HTTP-like transports.
	Headers map[string]string `json:"headers,omitempty"`

	// ServiceName is the gRPC service name.
	ServiceName string `json:"service_name,omitempty"`

	// EarlyDataHeaderName carries early data in an HTTP header, for
	// transports that would otherwise need a full round trip first.
	EarlyDataHeaderName string `json:"early_data_header_name,omitempty"`
}

// ProxyGroup is a named set of outbounds with a selection strategy.
type ProxyGroup struct {
	// Name uniquely names the group. Rules may reference it exactly like an
	// outbound tag, so the two share a namespace and must not collide.
	Name string `json:"name,omitempty"`

	// Type is the selection strategy: "select", "url-test", "fallback" or
	// "load-balance".
	Type string `json:"type,omitempty"`

	// Proxies lists the member outbound tags, in the order they are tried or
	// displayed.
	Proxies []string `json:"proxies,omitempty"`

	// Use lists provider names whose nodes are added to the group, which is
	// how a subscription populates a group without enumerating its members.
	Use []string `json:"use,omitempty"`

	// URL is the probe target for the strategies that measure reachability.
	URL string `json:"url,omitempty"`

	// Interval is how often the probe runs.
	Interval time.Duration `json:"interval,omitempty"`

	// Tolerance is the delay difference below which a url-test group keeps
	// its current member, to avoid flapping between near-equal nodes.
	Tolerance time.Duration `json:"tolerance,omitempty"`

	// Lazy makes probing happen only while the group is in use, which saves
	// bandwidth and wakeups on battery-powered hosts.
	Lazy bool `json:"lazy,omitempty"`
}

// Rule is one routing decision.
type Rule struct {
	// Type is the match kind, such as "domain", "domain-suffix", "geoip",
	// "ip-cidr", "process" or "rule-set".
	Type string `json:"type,omitempty"`

	// Payload is the value(s) the matcher compares against. A slice because
	// one rule line often lists several domains or CIDRs.
	Payload []string `json:"payload,omitempty"`

	// Target is the outbound or group tag chosen when this rule matches.
	Target string `json:"target,omitempty"`

	// RuleSet names a RuleSet to match against, for the "rule-set" type.
	RuleSet string `json:"rule_set,omitempty"`

	// NoResolve skips DNS resolution before matching, which matters for
	// rules that must not leak a lookup.
	NoResolve bool `json:"no_resolve,omitempty"`

	// Inbound restricts the rule to connections arriving on the named
	// inbounds, for rules that should only apply to one entry point.
	Inbound []string `json:"inbound,omitempty"`

	// Position controls where this rule is inserted during the merge. Rules
	// append by default; prepend exists because a user adding an exception
	// needs it to be evaluated before the rules already present
	// (AGENTS.md 21.4).
	Position Position `json:"position,omitempty"`

	// Params carries matcher settings that do not generalise across rule
	// types, such as the ASN list for an asn rule.
	Params map[string]any `json:"params,omitempty"`
}

// Position controls where a list entry is inserted during the merge.
type Position string

const (
	// PositionAppend adds the entry at the end of the existing list. It is
	// the zero value and the default, because most fragments contribute
	// independent entries rather than overrides (AGENTS.md 21.4).
	PositionAppend Position = ""

	// PositionPrepend adds the entry at the front, giving it higher priority.
	PositionPrepend Position = "prepend"
)

// RuleSet is a named, reusable group of rules.
type RuleSet struct {
	// Name uniquely names the set. Rules reference it by name.
	Name string `json:"name,omitempty"`

	// Type selects how the set is matched and where it is sourced from:
	// "inline" for rules written in place, "file" for a local file, or a
	// provider-backed kind such as "clash" or "sing-box".
	Type string `json:"type,omitempty"`

	// Format is the syntax of the backing file, needed when Type names a
	// source format rather than a matcher strategy.
	Format string `json:"format,omitempty"`

	// Path is the local file to load when Type is "file".
	Path string `json:"path,omitempty"`

	// URL is the remote location to fetch and cache, when the set is remote.
	URL string `json:"url,omitempty"`

	// Interval is how often a remote set is refreshed.
	Interval time.Duration `json:"interval,omitempty"`

	// Rules are the inlined members, used when Type is "inline".
	Rules []Rule `json:"rules,omitempty"`

	// Proxy restricts fetching to a specific outbound tag, for sources that
	// are only reachable through the proxy.
	Proxy string `json:"proxy,omitempty"`
}

// Provider is an external source of nodes or rule sets.
type Provider struct {
	// Name uniquely names the provider and is what ProxyGroup.Use refers to.
	Name string `json:"name,omitempty"`

	// Type is the kind of content: "proxy" for nodes, "rule" for rule sets.
	Type string `json:"type,omitempty"`

	// Format is the payload syntax, such as "clash", "v2ray", "sing-box" or
	// "uri". It is inferred from the URL only when structurally obvious;
	// otherwise it must be stated (AGENTS.md 21.2).
	Format string `json:"format,omitempty"`

	// URL is the subscription location.
	URL string `json:"url,omitempty"`

	// Path loads the provider from a local file instead of the network.
	Path string `json:"path,omitempty"`

	// Interval is how often the provider is refreshed.
	Interval time.Duration `json:"interval,omitempty"`

	// Filter keeps or drops nodes by name pattern during import, so that a
	// large subscription can be narrowed without editing it.
	Filter string `json:"filter,omitempty"`

	// ExcludeNames drops nodes whose names match, applied after Filter.
	ExcludeNames []string `json:"exclude_names,omitempty"`

	// HealthCheck configures liveness probing of the imported nodes.
	HealthCheck HealthCheck `json:"health_check,omitempty"`

	// Proxy routes the fetch through an outbound tag, for sources that are
	// only reachable through the proxy.
	Proxy string `json:"proxy,omitempty"`

	// Headers are extra request headers, for sources that require a token or
	// a particular user agent.
	Headers map[string]string `json:"headers,omitempty"`
}

// HealthCheck configures liveness probing for provider-supplied nodes.
type HealthCheck struct {
	// Enabled turns probing on.
	Enabled bool `json:"enabled,omitempty"`

	// URL is the probe target. It should be a small, reliable endpoint,
	// since every node is asked for it on each interval.
	URL string `json:"url,omitempty"`

	// Interval is how often to probe.
	Interval time.Duration `json:"interval,omitempty"`

	// Timeout bounds a single probe.
	Timeout time.Duration `json:"timeout,omitempty"`

	// Lazy probes only while the provider is in use.
	Lazy bool `json:"lazy,omitempty"`
}

// Sniffer controls protocol sniffing.
type Sniffer struct {
	// Enabled turns sniffing on globally. Individual inbounds may override
	// it in either direction.
	Enabled bool `json:"enabled,omitempty"`

	// OverrideDestination replaces the destination address with the sniffed
	// domain, so later rules and the outbound see the name rather than the
	// IP. It is off by default because it changes observable behaviour.
	OverrideDestination bool `json:"override_destination,omitempty"`

	// Timeout bounds how long sniffing may delay a connection before it is
	// forwarded unsniffed. Forwarding must never wait indefinitely on it.
	Timeout time.Duration `json:"timeout,omitempty"`

	// TLS enables SNI extraction from TLS ClientHello messages.
	TLS bool `json:"tls,omitempty"`

	// HTTP enables Host extraction from HTTP requests.
	HTTP bool `json:"http,omitempty"`

	// QUIC enables SNI extraction from QUIC Initial packets.
	QUIC bool `json:"quic,omitempty"`

	// SkipDomain drops sniffed values that are bare IP literals, which are
	// never useful as a rule input.
	SkipDomain []string `json:"skip_domain,omitempty"`
}

// ServerMode holds the settings used only when Mode is ModeServer.
type ServerMode struct {
	// Users are the accounts this node serves, with their quotas.
	Users []ServerUser `json:"users,omitempty"`

	// ACME holds automatic certificate issuance settings.
	ACME ACME `json:"acme,omitempty"`

	// Subscription holds the generated subscription endpoints.
	Subscription Subscription `json:"subscription,omitempty"`
}

// ServerUser is one account served by a server-mode node.
type ServerUser struct {
	// Name identifies the user in traffic records and audit logs.
	Name string `json:"name,omitempty"`

	// UUID is the credential for UUID-authenticated protocols.
	UUID string `json:"uuid,omitempty"`

	// Password is the credential for password-authenticated protocols.
	Password string `json:"password,omitempty"`

	// Level groups users for policy purposes, such as priority.
	Level int `json:"level,omitempty"`

	// QuotaBytes caps total traffic. Zero means unlimited.
	QuotaBytes int64 `json:"quota_bytes,omitempty"`

	// ExpiresAt disables the account after this time. A zero value means the
	// account does not expire.
	ExpiresAt time.Time `json:"expires_at,omitempty"`

	// Inbounds restricts the user to the named inbounds, so that one account
	// does not silently work on every entry point.
	Inbounds []string `json:"inbounds,omitempty"`
}

// ACME holds automatic certificate issuance settings.
type ACME struct {
	// Enabled turns automatic issuance and renewal on.
	Enabled bool `json:"enabled,omitempty"`

	// Domains are the names to obtain certificates for.
	Domains []string `json:"domains,omitempty"`

	// Email is the contact address the CA uses for expiry warnings.
	Email string `json:"email,omitempty"`

	// Provider names the DNS-01 challenge provider, needed when the host is
	// not reachable on port 80.
	Provider string `json:"provider,omitempty"`

	// CAServer overrides the ACME directory URL, for staging runs that avoid
	// burning the production rate limit.
	CAServer string `json:"ca_server,omitempty"`
}

// Subscription holds the generated subscription endpoints.
type Subscription struct {
	// Enabled turns subscription generation on.
	Enabled bool `json:"enabled,omitempty"`

	// Path is the URL prefix the subscription endpoints are served under.
	Path string `json:"path,omitempty"`

	// Formats lists the output syntaxes to offer, such as "clash",
	// "v2ray" and "sing-box".
	Formats []string `json:"formats,omitempty"`

	// BaseURL is the externally reachable origin used when building the
	// links handed to clients.
	BaseURL string `json:"base_url,omitempty"`
}

// Experimental holds opt-in features whose behaviour may still change.
type Experimental struct {
	// QUIC disables QUIC where the stack supports it, which some clients
	// need when a network blocks UDP outright.
	QUIC *bool `json:"quic,omitempty"`

	// CacheFile stores selected runtime caches on disk under state/, so that
	// Fake-IP mappings and probe results survive a restart.
	CacheFile string `json:"cache_file,omitempty"`
}
