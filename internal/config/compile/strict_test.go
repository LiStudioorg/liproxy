// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package compile

import (
	"errors"
	"strings"
	"testing"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
)

func strictFile(name, body string) ir.File {
	return ir.File{
		Ref:     ir.SourceRef{Path: "/cfg/" + name, Line: 1, Format: ir.FormatTOML},
		Content: []byte(body),
	}
}

// TestStrictRejectsUnknownTopLevelKey verifies the basic case and that the
// message proposes the field the user probably meant.
func TestStrictRejectsUnknownTopLevelKey(t *testing.T) {
	_, err := FromFiles([]ir.File{
		strictFile("a.toml", "[log]\nlevle = \"info\"\n"),
	})

	if err == nil {
		t.Fatal("a mistyped key compiled successfully; strict mode must reject it")
	}
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}

	msg := err.Error()
	for _, want := range []string{"log.levle", `"level"`, "a.toml"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not contain %q: %s", want, msg)
		}
	}
}

// TestStrictRejectsUnknownNestedKey verifies that the walk descends into
// tables rather than checking only the top level.
func TestStrictRejectsUnknownNestedKey(t *testing.T) {
	_, err := FromFiles([]ir.File{
		strictFile("a.toml", "[server]\nport = 9090\nlissten = \"127.0.0.1\"\n"),
	})
	if err == nil {
		t.Fatal("a mistyped nested key compiled successfully")
	}
	if !strings.Contains(err.Error(), "server.lissten") {
		t.Fatalf("error does not name the full path: %v", err)
	}
	if !strings.Contains(err.Error(), `"listen"`) {
		t.Fatalf("error does not suggest the real field: %v", err)
	}
}

// TestStrictRejectsUnknownKeyInListElement verifies that list elements are
// checked too.
//
// This is the case that a naive top-level walk misses, and it is also the most
// common shape in a real configuration: inbounds, outbounds and rules are all
// lists of tables.
func TestStrictRejectsUnknownKeyInListElement(t *testing.T) {
	_, err := FromFiles([]ir.File{
		strictFile("a.toml", "[[inbounds]]\ntag = \"in\"\ntype = \"mixed\"\nprot = 7890\n"),
	})
	if err == nil {
		t.Fatal("a mistyped key inside a list element compiled successfully")
	}
	if !strings.Contains(err.Error(), "inbounds[0].prot") {
		t.Fatalf("error does not give the indexed path: %v", err)
	}
	if !strings.Contains(err.Error(), `"port"`) {
		t.Fatalf("error does not suggest the real field: %v", err)
	}
}

// TestStrictAllowsOpenMapFields verifies that the three intentionally open
// maps are not policed.
//
// Their key sets belong to the protocol that owns them, so rejecting keys
// there would flag correct configuration — the failure mode that makes an
// over-eager validator worse than none.
func TestStrictAllowsOpenMapFields(t *testing.T) {
	_, err := FromFiles([]ir.File{
		strictFile("a.toml", `
[[inbounds]]
tag = "in"
type = "mixed"

[inbounds.options]
handshake_timeout = "5s"
whatever_vendor_key = true

[[outbounds]]
tag = "out"
type = "vless"

[outbounds.options]
custom = 1

[[rules]]
type = "domain"
payload = ["x.example"]
target = "out"

[rules.params]
raw_expression = "something"
`),
	})
	if err != nil {
		t.Fatalf("keys inside an open map were rejected: %v", err)
	}
}

// TestStrictAllowsCorrectConfiguration verifies strict mode does not reject
// valid input, which is the property that makes it safe to enable by default.
func TestStrictAllowsCorrectConfiguration(t *testing.T) {
	_, err := FromFiles([]ir.File{
		strictFile("a.toml", `
mode = "client"

[log]
level = "info"

[server]
listen = "127.0.0.1"
port = 9090

[[inbounds]]
tag = "in"
type = "mixed"
port = 7890

[[outbounds]]
tag = "out"
type = "direct"

[[rules]]
type = "domain-suffix"
payload = ["example.com"]
target = "out"
`),
	})
	if err != nil {
		t.Fatalf("valid configuration was rejected in strict mode: %v", err)
	}
}

// TestStrictIsDefault verifies that a caller who does not think about options
// still gets the check.
func TestStrictIsDefault(t *testing.T) {
	if !DefaultOptions().Strict {
		t.Fatal("DefaultOptions().Strict = false; a mistyped key must be reported by default")
	}

	_, err := FromFiles([]ir.File{strictFile("a.toml", "[log]\nlevle = \"info\"\n")})
	if err == nil {
		t.Fatal("FromFiles accepted a mistyped key; strict must be the default")
	}
}

// TestNonStrictAcceptsUnknownKeys verifies the opt-out, which exists for
// loading configuration written against a different version.
func TestNonStrictAcceptsUnknownKeys(t *testing.T) {
	_, err := FromFilesWith(
		[]ir.File{strictFile("a.toml", "[log]\nlevle = \"info\"\nlevel = \"debug\"\n")},
		Options{Strict: false},
	)
	if err != nil {
		t.Fatalf("non-strict mode rejected an unknown key: %v", err)
	}
}

// TestStrictReportsDeterministicKey verifies that a file with several typos
// always reports the same one, so the message is stable across runs.
func TestStrictReportsDeterministicKey(t *testing.T) {
	body := "[log]\nzzz = 1\naaa = 2\nmmm = 3\n"

	first := ""
	for i := 0; i < 5; i++ {
		_, err := FromFiles([]ir.File{strictFile("a.toml", body)})
		if err == nil {
			t.Fatal("unknown keys were accepted")
		}
		if i == 0 {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Fatalf("error differs between runs:\n%s\n%s", first, err.Error())
		}
	}
	// Keys are visited in sorted order, so "aaa" is reported first.
	if !strings.Contains(first, "log.aaa") {
		t.Fatalf("expected the alphabetically first key to be reported: %s", first)
	}
}

// TestStrictSuggestionRespectsDistance verifies that an unrelated key gets no
// suggestion rather than a misleading one.
//
// Proposing a field that is obviously unrelated is worse than staying silent:
// the user follows the hint and arrives somewhere wrong.
func TestStrictSuggestionRespectsDistance(t *testing.T) {
	_, err := FromFiles([]ir.File{
		strictFile("a.toml", "[log]\ncompletely_different_name = 1\n"),
	})
	if err == nil {
		t.Fatal("unknown key was accepted")
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("a distant key produced a suggestion: %v", err)
	}
	if !strings.Contains(err.Error(), "valid keys here are") {
		t.Errorf("no fallback listing of valid keys: %v", err)
	}
}

// TestStrictUnknownKeyInListedFile verifies that the reported path is the file
// that actually declared the bad key, not the first file loaded.
func TestStrictUnknownKeyInListedFile(t *testing.T) {
	_, err := FromFiles([]ir.File{
		strictFile("010-base.toml", "[log]\nlevel = \"info\"\n"),
		strictFile("020-bad.toml", "[log]\nlevle = \"debug\"\n"),
	})
	if err == nil {
		t.Fatal("unknown key was accepted")
	}
	if !strings.Contains(err.Error(), "020-bad.toml") {
		t.Fatalf("error names the wrong file: %v", err)
	}
}

// TestEditDistance covers the helper directly, including the early exit that
// keeps an obvious mismatch from being scored as a near miss.
func TestEditDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"level", "level", 0},
		{"levle", "level", 2},    // transposition is two edits
		{"listen", "lissten", 1}, // one insertion
		{"port", "prot", 2},
		{"abc", "xyz", 3}, // exceeds the limit, reported as limit+1
	}
	for _, c := range cases {
		if got := editDistance(c.a, c.b, 2); got != c.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestIsOpenField covers the exemption matching, including the indexed form
// that a list element produces.
func TestIsOpenField(t *testing.T) {
	open := []string{
		"inbounds[].options",
		"inbounds[0].options",
		"inbounds[3].options.vendor_key",
		"outbounds[0].options",
		"rules[0].params",
		"rules[0].params.expression",
	}
	for _, p := range open {
		if !isOpenField(p) {
			t.Errorf("isOpenField(%q) = false, want true", p)
		}
	}

	closed := []string{
		"inbounds[0].tag",
		"inbounds[0].type",
		"log.level",
		"options",
		"outbounds[0].server",
	}
	for _, p := range closed {
		if isOpenField(p) {
			t.Errorf("isOpenField(%q) = true, want false", p)
		}
	}
}
