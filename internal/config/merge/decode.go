// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package merge

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
)

// errors returned by the decoders. They are distinguished so that a caller
// can tell a syntax error from a structural one.
var (
	// ErrSyntax reports that a document could not be parsed in its declared
	// format.
	ErrSyntax = errors.New("config: syntax error")

	// ErrNotTable reports that a document's root is not a table. A config
	// file with a scalar or array at its root has no keys to merge, so it is
	// rejected rather than coerced.
	ErrNotTable = errors.New("config: document root is not a table")

	// ErrDuplicateKey reports that one document defines the same key twice.
	//
	// TOML rejects this at parse time; YAML accepts it and keeps the last
	// value. Raising it in the shared tree builder gives both formats the same
	// rule, which is the only way "write it once" can mean the same thing in a
	// .toml and a .yaml fragment.
	ErrDuplicateKey = errors.New("config: duplicate key")
)

// decode parses one file's content according to its declared format.
//
// The result is always an untyped tree so that the merge logic downstream
// never has to know which syntax produced it. Both formats are decoded into
// map[string]any / []any / scalars, and the conversions that harmonize their
// scalar types live here rather than in the merge.
func decode(file ir.File) (map[string]any, error) {
	if len(bytes.TrimSpace(file.Content)) == 0 {
		// An empty file is legal and contributes nothing. It happens
		// routinely with fragments that were created but not yet filled in,
		// and with a main config that only holds includes.
		return map[string]any{}, nil
	}

	switch file.Ref.Format {
	case ir.FormatTOML:
		return decodeTOML(file)
	case ir.FormatYAML:
		return decodeYAML(file)
	default:
		// detectFormat rejects unknown formats during load, so reaching here
		// means a FileSet was constructed by hand. It is an error rather than
		// a guess: AGENTS.md 21.2 requires that an undeterminable format is
		// reported, never assumed.
		return nil, fmt.Errorf("%w: format %q is not known for %s",
			ErrSyntax, file.Ref.Format, file.Ref.Path)
	}
}

// decodeTOML decodes a TOML document into an untyped tree.
func decodeTOML(file ir.File) (map[string]any, error) {
	var root map[string]any
	md, err := toml.NewDecoder(bytes.NewReader(file.Content)).Decode(&root)
	if err != nil {
		return nil, tomlSyntaxError(file, err)
	}
	_ = md

	if root == nil {
		return map[string]any{}, nil
	}

	// BurntSushi/toml decodes an array of tables into []map[string]any, while
	// yaml.v3 produces []any. The shared merge only understands []any, so the
	// TOML shape is normalized here rather than in every consumer.
	//
	// This is the single most important reason the tree merge exists: without
	// it, every list would need two implementations and the two formats would
	// drift apart in ways no test would reliably catch.
	return normalize(root).(map[string]any), nil
}

// normalize rewrites TOML's concretely-typed containers into the untyped shape
// the rest of the pipeline uses.
func normalize(v any) any {
	switch t := v.(type) {
	case []map[string]any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = normalize(item)
		}
		return out

	case []any:
		for i, item := range t {
			t[i] = normalize(item)
		}
		return t

	case map[string]any:
		for k, item := range t {
			t[k] = normalize(item)
		}
		return t

	default:
		return v
	}
}

// tomlSyntaxError attaches the file path to a TOML parse error.
//
// BurntSushi/toml reports line and column on ParseError, so the position is
// taken from there and the error is wrapped in a positioned Error. Other error
// shapes are passed through with the path only.
func tomlSyntaxError(file ir.File, err error) error {
	ref := file.Ref
	var perr *toml.ParseError
	if errors.As(err, &perr) {
		// ParseError.Position is the only structured position BurntSushi/toml
		// exposes; MetaData has no per-key position method, which is why the
		// loader recovers include line numbers by scanning the source text.
		ref.Line = perr.Position.Line
		ref.Column = perr.Position.Col
	}
	// err is used rather than perr: not every failure BurntSushi/toml returns
	// is a *ParseError, and formatting perr unconditionally would panic on the
	// ones that are not — turning a reportable syntax error into a crash.
	return &Error{
		Op:  "parse",
		Ref: ref,
		Err: fmt.Errorf("%w: %v", ErrSyntax, err),
	}
}

// decodeYAML decodes a YAML document into an untyped tree.
//
// The tree is built by walking yaml.Node rather than by decoding into any,
// because only the node graph carries line numbers for individual keys, and
// provenance needs those. It also lets repeated keys be detected, which
// yaml.v3 would otherwise resolve silently in favour of the last one.
func decodeYAML(file ir.File) (map[string]any, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(file.Content, &root); err != nil {
		return nil, yamlSyntaxError(file, err)
	}

	node := &root
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return map[string]any{}, nil
		}
		node = node.Content[0]
	}

	// A document that is explicitly null (for example a file containing only
	// "---") is empty rather than invalid.
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return map[string]any{}, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, &Error{
			Op:  "parse",
			Ref: sourceOf(file, node),
			Err: fmt.Errorf("%w: got %s", ErrNotTable, yamlKindName(node)),
		}
	}

	body, err := yamlToTree(file, node, nil)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// yamlSyntaxError attaches the file path to a YAML parse error.
func yamlSyntaxError(file ir.File, err error) error {
	ref := file.Ref
	// yaml.v3 reports "line N:" inside the message text and exposes no
	// structured position for syntax errors. The line is recovered from the
	// message so that the common case still points at a line; when the text
	// does not parse, the error degrades to naming only the file.
	if line := yamlLineFromMessage(err.Error()); line > 0 {
		ref.Line = line
	}
	return &Error{
		Op:  "parse",
		Ref: ref,
		Err: fmt.Errorf("%w: %v", ErrSyntax, err),
	}
}

// yamlLineFromMessage extracts the line number yaml.v3 embeds in its error
// text. It returns 0 when the message has no recognizable "line N" prefix.
func yamlLineFromMessage(msg string) int {
	const marker = "line "
	i := strings.Index(msg, marker)
	if i < 0 {
		return 0
	}
	rest := msg[i+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return n
}

// yamlToTree converts a YAML node into the untyped tree, rejecting repeated
// keys and unsupported node kinds.
//
// trail carries the key path for error messages, so a duplicate is reported
// with the location of the *second* occurrence plus the line of the first —
// which is the pair a user needs to resolve it.
func yamlToTree(file ir.File, node *yaml.Node, trail []string) (map[string]any, error) {
	out := make(map[string]any, len(node.Content)/2)
	seen := make(map[string]int, len(node.Content)/2)

	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]

		key := keyNode.Value
		if prev, dup := seen[key]; dup {
			return nil, &Error{
				Op:  "parse",
				Ref: sourceOf(file, keyNode),
				Err: fmt.Errorf(
					"%w: key %q is already defined at line %d; "+
						"YAML keeps only one of the two, so this is rejected rather than guessed",
					ErrDuplicateKey, key, prev),
			}
		}
		seen[key] = keyNode.Line

		val, err := yamlValue(file, valNode, append(trail, key))
		if err != nil {
			return nil, err
		}
		out[key] = val
	}
	return out, nil
}

// yamlValue converts one YAML node to its Go representation.
func yamlValue(file ir.File, node *yaml.Node, trail []string) (any, error) {
	switch node.Kind {
	case yaml.MappingNode:
		return yamlToTree(file, node, trail)

	case yaml.SequenceNode:
		seq := make([]any, 0, len(node.Content))
		for i, item := range node.Content {
			v, err := yamlValue(file, item, append(trail, fmt.Sprintf("[%d]", i)))
			if err != nil {
				return nil, err
			}
			seq = append(seq, v)
		}
		return seq, nil

	case yaml.ScalarNode:
		return yamlScalar(file, node)

	case yaml.AliasNode:
		// Anchors and aliases are expanded at parse time by yaml.v3 only when
		// decoding into a Go value. Walking the node graph keeps the alias
		// node, so it is followed here. The graph is guaranteed acyclic by
		// yaml.v3, which rejects self-referential anchors.
		if node.Alias == nil {
			return nil, &Error{
				Op:  "parse",
				Ref: sourceOf(file, node),
				Err: fmt.Errorf("%w: alias %q has no target", ErrSyntax, node.Value),
			}
		}
		return yamlValue(file, node.Alias, trail)

	default:
		return nil, &Error{
			Op:  "parse",
			Ref: sourceOf(file, node),
			Err: fmt.Errorf("%w: unsupported node kind %s at %s",
				ErrSyntax, yamlKindName(node), joinBreadcrumb(trail)),
		}
	}
}

// yamlScalar converts a scalar node to the value the IR expects.
//
// Resolution is done against the node's resolved tag rather than its text, so
// that YAML's own typing rules apply: "yes" is a boolean, "0x10" is a number,
// and a quoted "8080" stays a string. Guessing from the text instead would
// disagree with what a YAML user sees in their file.
func yamlScalar(file ir.File, node *yaml.Node) (any, error) {
	switch node.Tag {
	case "!!null":
		return nil, nil
	case "!!bool":
		var b bool
		if err := node.Decode(&b); err != nil {
			return nil, &Error{Op: "parse", Ref: sourceOf(file, node),
				Err: fmt.Errorf("%w: %v", ErrSyntax, err)}
		}
		return b, nil
	case "!!int":
		var i int64
		if err := node.Decode(&i); err != nil {
			// An integer too large for int64 lands here. It is kept as a
			// float rather than dropped, and the field-level conversion will
			// reject it if the target field cannot hold it.
			var f float64
			if ferr := node.Decode(&f); ferr != nil {
				return nil, &Error{Op: "parse", Ref: sourceOf(file, node),
					Err: fmt.Errorf("%w: %v", ErrSyntax, err)}
			}
			return f, nil
		}
		return i, nil
	case "!!float":
		var f float64
		if err := node.Decode(&f); err != nil {
			return nil, &Error{Op: "parse", Ref: sourceOf(file, node),
				Err: fmt.Errorf("%w: %v", ErrSyntax, err)}
		}
		return f, nil
	default:
		return node.Value, nil
	}
}

// yamlKindName names a YAML node kind for error messages.
func yamlKindName(node *yaml.Node) string {
	switch node.Kind {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "array"
	case yaml.MappingNode:
		return "table"
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			return "null"
		}
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	default:
		return "node"
	}
}

// sourceOf builds a SourceRef for one YAML node.
func sourceOf(file ir.File, node *yaml.Node) ir.SourceRef {
	return ir.SourceRef{
		Path:   file.Ref.Path,
		Line:   node.Line,
		Column: node.Column,
		Format: ir.FormatYAML,
	}
}

// decodeReader is used by tests to parse a document from raw bytes without
// constructing an ir.File. It exists so that decoder edge cases can be
// exercised directly.
func decodeReader(format ir.Format, content []byte) (map[string]any, error) {
	return decode(ir.File{
		Ref:     ir.SourceRef{Path: "<test>", Line: 1, Format: format},
		Content: content,
	})
}
