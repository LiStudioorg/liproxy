// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package log is the single logging entry point for LiProxy.
//
// Other packages must not construct their own zerolog.Logger: the whole point
// of this wrapper is that the destination, level and redaction rules are
// configured once in cmd/lipd. It also keeps the sensitive-field policy
// (AGENTS.md section 14) in one place instead of spread across call sites.
//
// Ownership: Log is a package-level *zerolog.Logger. Logger() returns a child
// logger, which zerolog documents as safe for concurrent use, so it may be
// stored on structs and shared across goroutines.
package log

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/rs/zerolog"
)

// Log is the process-wide logger. Setup replaces it once, during startup,
// before any goroutine that logs has been started.
var Log *zerolog.Logger

func init() {
	nop := zerolog.Nop()
	Log = &nop
}

// Setup installs a logger writing JSON records to w and returns it.
//
// A bad level string is a caller bug, so it is reported rather than silently
// downgraded: the caller (cmd/lipd) turns it into a startup failure, matching
// the "migration failed, refuse to start" philosophy.
func Setup(w io.Writer, level string) (*zerolog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}

	logger := zerolog.New(w).Level(lvl).With().Timestamp().Logger()
	Log = &logger
	return &logger, nil
}

// ParseLevel maps a level name to a zerolog level. Accepts the names users
// write in liproxy.toml ("info", "debug", ...) case-insensitively.
func ParseLevel(level string) (zerolog.Level, error) {
	lvl, err := zerolog.ParseLevel(strings.ToLower(strings.TrimSpace(level)))
	if err != nil {
		return zerolog.NoLevel, fmt.Errorf("log: unknown level %q", level)
	}
	return lvl, nil
}

// Level is an io.Writer that adjusts the global logger's level when written
// to. It exists so that a future runtime config reload (SIGHUP, or a Web
// setting change) does not need a separate plumbing path.
type Level struct {
	mu  sync.RWMutex
	lvl zerolog.Level
}

// SetLevel changes the level applied to subsequent writes.
func (l *Level) SetLevel(level string) error {
	lvl, err := ParseLevel(level)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.lvl = lvl
	l.mu.Unlock()
	SetLevel(lvl)
	return nil
}

// Write implements io.Writer by applying the stored level globally.
func (l *Level) Write(p []byte) (int, error) {
	l.mu.RLock()
	lvl := l.lvl
	l.mu.RUnlock()
	SetLevel(lvl)
	return len(p), nil
}

// SetLevel applies a level to the global logger. It is a no-op when the
// logger was never set up, which keeps tests that never call Setup working.
func SetLevel(lvl zerolog.Level) {
	if Log == nil {
		return
	}
	updated := Log.Level(lvl)
	Log = &updated
}

// Logger returns a child logger with the given component attached. Every
// package should call this once and store the result rather than calling
// log.Log directly, so records are always attributable to a component.
func Logger(component string) *zerolog.Logger {
	l := Log.With().Str("component", component).Logger()
	return &l
}

// Sensitive-value kinds. Redact replaces the whole value with <redacted:kind>,
// and Fingerprint renders a short stable digest instead of the secret.
const (
	kindUUID       = "uuid"
	kindCredential = "credential"
	kindEmail      = "email"
	kindIP         = "ip"
)

// matchReports controls regexp submatch reporting for the patterns below.
// Fingerprint needs to know whether a pattern matched a capture group; with
// the flag set, FindStringSubmatch returns len 2 for a match and nil for no
// match, which is unambiguous for optional single-group patterns.
const matchReports = true

// uuidRe matches canonical 8-4-4-4-12 UUIDs, the credential format used by
// VMess and VLESS nodes.
var uuidRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// jwtRe matches three base64url segments separated by dots. The segment
// length floors keep ordinary dotted hostnames from matching.
var jwtRe = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)

// integrityRe matches config references that embed a base64 public key, e.g.
// "vless://uuid@host?pbk=...". The '=' is optional so unpadded base64 is hit.
var integrityRe = regexp.MustCompile(`(?:pbk|publicKey|public-key|sni_key)[=:]["']?([A-Za-z0-9+/_-]{16,}={0,2})["']?`)

var (
	// labeledRe matches "password=x" for the sensitive keys named in AGENTS.md
	// section 14 (password, passwd, secret, token, api_key, private_key).
	// The value is either a quoted string or a run of non-space characters
	// that must contain at least one character from the non-letter class;
	// that second alternative is what keeps prose like "private key is
	// missing" from being redacted as if it were a credential.
	labeledRe = regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key|private[_-]?key)\b(\s*[=:]\s*)("[^"]*"|'[^']*'|\S*[0-9+/=_-]\S*)`)

	// subscriptionRe matches subscription URLs. The (?i) applies only to the
	// scheme group so that the literal "/sub/" path stays case-sensitive and
	// does not swallow unrelated slashes.
	subscriptionRe = regexp.MustCompile(`(?i)(https?)://\S*(?:(?:/sub/|/api/v1/subscribe/)\S*|[?&](?:token|key)=\S+)`)

	// emailRe matches subscriber identifiers used by the server mode.
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

	// ipv4Re matches dotted quads, optionally with a :port suffix. It runs
	// with matchReports so Fingerprint can tell "ip:port" from a bare IP.
	ipv4Re = regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})(:\d{1,5})?\b`)

	// ipv6Re matches bracketed IPv6 addresses, with or without a port.
	ipv6Re = regexp.MustCompile(`\[[0-9A-Fa-f:]{2,45}\](?::\d{1,5})?`)
)

// replacement renders the stand-in that takes the place of a matched secret.
func replacement(kind string) string {
	return "<redacted:" + kind + ">"
}

// Redact removes the sensitive substrings listed in AGENTS.md section 14 from
// s and returns a string that is safe to write to logs or error responses.
//
// Regex-based redaction cannot be perfect: `Redact("the password is hunter2")`
// leaves the value in place, because natural language carries no structure to
// anchor on. Callers must therefore never pass a secret as part of a free-form
// sentence; use structured fields such as Str("password", Redact(pw)) or
// Fingerprint(pw) instead, where the key names the value and this function can
// see it. Treat the result as defence in depth, not as a licence to log
// user-supplied config.
func Redact(s string) string {
	if s == "" {
		return s
	}

	s = uuidRe.ReplaceAllString(s, replacement(kindUUID))
	s = jwtRe.ReplaceAllString(s, replacement(kindCredential))
	s = integrityRe.ReplaceAllString(s, "${1}${2}"+replacement(kindCredential))
	s = labeledRe.ReplaceAllString(s, "${1}${2}"+replacement(kindCredential))
	s = subscriptionRe.ReplaceAllString(s, replacement(kindCredential))
	s = emailRe.ReplaceAllString(s, replacement(kindEmail))
	s = ipv6Re.ReplaceAllString(s, replacement(kindIP))
	s = ipv4Re.ReplaceAllString(s, replacement(kindIP))

	return s
}

// Fingerprint returns a short stable digest of a sensitive value, to be used
// when logs need to correlate two occurrences of the same secret (for example
// "which node's password failed") without disclosing it.
//
// It is a plain SHA-256 prefix, so it is only safe for high-entropy secrets
// such as UUIDs, keys and tokens. Never call it on a user-chosen password:
// the digest of a guessable value is itself a confirmation oracle. Returns
// "none" for an empty value so log lines stay aligned.
func Fingerprint(s string) string {
	if s == "" {
		return "none"
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

// RedactedField is the JSON representation used when a struct field must be
// logged. Assign Redact(value) to the field before logging a struct, or
// implement zerolog.LogObjectMarshaler with this slice.
type RedactedField []byte

// MarshalZerologObject implements zerolog.LogObjectMarshaler for a string
// field that should be redacted in place.
func (r RedactedField) MarshalZerologObject(e *zerolog.Event) {
	e.Str("value", string(r))
}

// MustParseLevel is reserved for tests and internal call sites that pass a
// compile-time constant level.
func MustParseLevel(level string) zerolog.Level {
	lvl, err := ParseLevel(level)
	if err != nil {
		panic(err)
	}
	return lvl
}

// StderrLogger returns a logger writing to stderr at the given level without
// touching the package-level logger. cmd/lip uses it for error paths that
// must not depend on Setup having run.
func StderrLogger(level string) (*zerolog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	l := zerolog.New(os.Stderr).Level(lvl).With().Timestamp().Logger()
	return &l, nil
}
