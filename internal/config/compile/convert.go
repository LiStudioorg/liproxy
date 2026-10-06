// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package compile

import (
	"errors"
	"fmt"
	"time"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
)

// Sentinel errors for this package.
var (
	// ErrNilFileSet reports a nil input, which is a programming error rather
	// than a configuration one.
	ErrNilFileSet = errors.New("config: nil FileSet")

	// ErrFieldType reports a field holding a value of the wrong shape, such
	// as a table where a string is expected.
	ErrFieldType = errors.New("config: wrong field type")

	// ErrDuration reports a duration string that cannot be parsed.
	ErrDuration = errors.New("config: invalid duration")
)

// field is one named value during conversion, carrying its own path so that a
// type error can name the exact field.
type field struct {
	path string
	val  any
}

// table adapts a merged map into a converter with path tracking.
type table struct {
	path   string
	fields map[string]any
}

// tableAt returns a converter for a nested table, or a zero table when the key
// is absent. A zero table's lookups all miss, which makes "field not present"
// flow through the same code path as "field present but empty".
func tableAt(parent table, key string) table {
	raw, ok := parent.fields[key]
	if !ok {
		return table{path: parent.child(key)}
	}
	m, ok := raw.(map[string]any)
	if !ok {
		// A non-table here is reported by the field accessors when they try
		// to read something out of it, with a better message than this level
		// could produce.
		return table{path: parent.child(key)}
	}
	return table{path: parent.child(key), fields: m}
}

// child builds the dotted path of a nested key.
func (t table) child(key string) string {
	if t.path == "" {
		return key
	}
	return t.path + "." + key
}

// has reports whether a key is present.
func (t table) has(key string) bool {
	_, ok := t.fields[key]
	return ok
}

// str reads a string field.
func (t table) str(key string) (string, error) {
	raw, ok := t.fields[key]
	if !ok || raw == nil {
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", t.typeErr(key, raw, "string")
	}
	return s, nil
}

// strList reads a list of strings.
//
// A bare string is accepted as a one-element list, because both TOML and YAML
// make the bracketed form noise for the common single-value case, and users
// write `payload = "example.com"` without thinking of it as a list.
func (t table) strList(key string) ([]string, error) {
	raw, ok := t.fields[key]
	if !ok || raw == nil {
		return nil, nil
	}
	if s, ok := raw.(string); ok {
		return []string{s}, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, t.typeErr(key, raw, "array of strings")
	}
	out := make([]string, 0, len(list))
	for i, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, t.indexErr(key, i, item, "string")
		}
		out = append(out, s)
	}
	return out, nil
}

// strMap reads a table of strings.
func (t table) strMap(key string) (map[string]string, error) {
	raw, ok := t.fields[key]
	if !ok || raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, t.typeErr(key, raw, "table of strings")
	}
	out := make(map[string]string, len(m))
	for _, k := range sortedKeys(m) {
		s, ok := m[k].(string)
		if !ok {
			return nil, t.typeErr(key+"."+k, m[k], "string")
		}
		out[k] = s
	}
	return out, nil
}

// bool reads a boolean field.
func (t table) bool(key string) (bool, error) {
	raw, ok := t.fields[key]
	if !ok || raw == nil {
		return false, nil
	}
	b, ok := raw.(bool)
	if !ok {
		return false, t.typeErr(key, raw, "boolean")
	}
	return b, nil
}

// boolPtr reads an optional boolean, distinguishing absent from false.
//
// The pointer is what keeps "not set" distinct from "set to false", which the
// IR needs for fields that override a global default in either direction, such
// as an inbound's sniff setting.
func (t table) boolPtr(key string) (*bool, error) {
	raw, ok := t.fields[key]
	if !ok || raw == nil {
		return nil, nil
	}
	b, ok := raw.(bool)
	if !ok {
		return nil, t.typeErr(key, raw, "boolean")
	}
	return &b, nil
}

// int reads an integer field.
func (t table) int(key string) (int, error) {
	raw, ok := t.fields[key]
	if !ok || raw == nil {
		return 0, nil
	}
	n, err := toInt(raw)
	if err != nil {
		return 0, t.typeErr(key, raw, "integer")
	}
	return n, nil
}

// int64 reads a 64-bit integer field.
func (t table) int64(key string) (int64, error) {
	raw, ok := t.fields[key]
	if !ok || raw == nil {
		return 0, nil
	}
	switch v := raw.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	default:
		return 0, t.typeErr(key, raw, "integer")
	}
}

// toInt converts a decoded numeric value to int.
//
// Floats are accepted only when they are integral. A value like 1.5 for a port
// is a mistake worth reporting, while 8080 decoded as a float (which happens
// for a YAML value such as 8.08e3) is not.
func toInt(raw any) (int, error) {
	switch v := raw.(type) {
	case int64:
		return int(v), nil
	case int:
		return v, nil
	case float64:
		if v != float64(int(v)) {
			return 0, fmt.Errorf("value %v is not a whole number", v)
		}
		return int(v), nil
	default:
		return 0, fmt.Errorf("value of type %T is not a number", raw)
	}
}

// duration reads a duration field.
//
// Durations are written as strings with units ("30s", "5m") rather than as
// bare numbers, so that a value cannot be misread as seconds when the reader
// assumed milliseconds. A bare number is rejected instead of guessed.
func (t table) duration(key string) (time.Duration, error) {
	raw, ok := t.fields[key]
	if !ok || raw == nil {
		return 0, nil
	}
	s, ok := raw.(string)
	if !ok {
		return 0, t.typeErr(key, raw, `duration string such as "30s"`)
	}
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, errorAt("convert", ir.SourceRef{},
			fmt.Errorf("%w: %s: %q", ErrDuration, t.child(key), s))
	}
	return d, nil
}

// time reads an RFC 3339 timestamp field.
func (t table) time(key string) (time.Time, error) {
	raw, ok := t.fields[key]
	if !ok || raw == nil {
		return time.Time{}, nil
	}
	s, ok := raw.(string)
	if !ok {
		return time.Time{}, t.typeErr(key, raw, "RFC 3339 timestamp string")
	}
	if s == "" {
		return time.Time{}, nil
	}
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, errorAt("convert", ir.SourceRef{},
			fmt.Errorf("%w: %s: %q is not an RFC 3339 timestamp", ErrFieldType, t.child(key), s))
	}
	return ts, nil
}

// anyMap reads a free-form table, used for the protocol option maps that are
// not yet modelled as typed fields.
func (t table) anyMap(key string) (map[string]any, error) {
	raw, ok := t.fields[key]
	if !ok || raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, t.typeErr(key, raw, "table")
	}
	return m, nil
}

// list reads a raw list for iteration.
func (t table) list(key string) ([]any, bool, error) {
	raw, ok := t.fields[key]
	if !ok || raw == nil {
		return nil, false, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, false, t.typeErr(key, raw, "array")
	}
	return list, true, nil
}

// typeErr builds a positioned type error naming the field and both shapes.
func (t table) typeErr(key string, raw any, want string) error {
	return errorAt("convert", ir.SourceRef{}, fmt.Errorf(
		"%w: %s must be a %s, got %s",
		ErrFieldType, t.child(key), want, kindName(raw)))
}

// indexErr builds a positioned type error for one list element.
func (t table) indexErr(key string, i int, raw any, want string) error {
	return errorAt("convert", ir.SourceRef{}, fmt.Errorf(
		"%w: %s[%d] must be a %s, got %s",
		ErrFieldType, t.child(key), i, want, kindName(raw)))
}

// kindName names a decoded value's shape for error messages.
func kindName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "table"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case int64, int, float64:
		return "number"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// decodeStringEnum converts a string field to a named string type.
func decodeStringEnum[T ~string](t table, key string) (T, error) {
	s, err := t.str(key)
	if err != nil {
		return "", err
	}
	return T(s), nil
}
