// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package loader

import (
	"errors"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/LiStudioorg/liproxy/internal/config/ir"
)

// includeKind distinguishes the two accepted spellings of an include.
type includeKind int

const (
	// kindPath is the shorthand form, a bare string in the include list.
	// Format is inferred from the extension and optional is false.
	kindPath includeKind = iota

	// kindStructured is the table/object form, which is required whenever the
	// format cannot be inferred or the file may legitimately be absent.
	kindStructured
)

// include is one resolved directive from a file's include list.
type include struct {
	// kind records which spelling produced this directive, for diagnostics.
	kind includeKind

	// Pattern is the path or glob exactly as the user wrote it. It stays
	// unexpanded so that error messages quote the user's own text rather than
	// a resolved absolute path they never typed.
	Pattern string

	// Format overrides extension-based detection. It is set only by the
	// structured form; an explicit value is mandatory for cross-format
	// includes, since a .toml file naming a path with no extension cannot be
	// inferred (AGENTS.md 21.2).
	Format ir.Format

	// Optional marks a file that may be absent without failing the load.
	Optional bool

	// Line is the 1-based line the directive appeared on. It is recorded at
	// parse time because it cannot be recovered afterwards, and it is what
	// makes an include failure point at the user's own line.
	Line int
}

// directives is the set of include and exclude instructions found in one file.
type directives struct {
	// Includes are the include patterns, in the order written. Order is
	// preserved because it determines load order for a single file's list.
	Includes []include

	// Excludes are patterns removed from every include expansion in this
	// file. They are applied to the expansion result rather than to the
	// includes themselves (AGENTS.md 21.2).
	Excludes []string

	// Misfiled records include/exclude keys that landed inside a table
	// instead of at document root. They are reported as warnings, not errors.
	Misfiled []misfiledDirective
}

// misfiledDirective is one include/exclude key found below the document root.
//
// In TOML a bare key written after a [table] header belongs to that table, so
//
//	[log]
//	level = "info"
//	include = ["extra.toml"]
//
// declares log.include, not include. The document still parses and the load
// still succeeds, which is precisely the problem: the user believes the
// fragment is included and nothing indicates otherwise. This type carries
// enough detail to say exactly where the mistake is.
type misfiledDirective struct {
	// Key is the full dotted path, such as "log.include". It is what the user
	// must search for, and it is not recoverable from the line alone.
	Key string

	// Line is the 1-based line the key was written on.
	Line int
}

// parseDirectives extracts include and exclude instructions from a file.
//
// It parses the document to read these keys but deliberately discards every
// other value: merging is not this package's job, and the raw bytes are
// already preserved on File.Content for whoever does it. Parsing the whole
// document here rather than scanning for key names means a malformed file is
// rejected at load time with a real position, instead of failing later in a
// merge step that has no line information.
func parseDirectives(f *ir.File) (directives, error) {
	var d directives
	var err error

	switch f.Ref.Format {
	case ir.FormatTOML:
		d, err = parseTOMLDirectives(f)
	case ir.FormatYAML:
		d, err = parseYAMLDirectives(f)
	default:
		// Unreachable: format is settled before a File is constructed. Kept
		// as an explicit failure rather than a default branch so that adding
		// a format cannot silently skip directive parsing.
		return directives{}, errorAt("parse", f.Ref,
			fmt.Errorf("%w: %q", ErrFormatUnknown, f.Ref.Path), nil)
	}
	if err != nil {
		return directives{}, err
	}
	return d, nil
}

// parseTOMLDirectives reads include and exclude from a TOML document.
func parseTOMLDirectives(f *ir.File) (directives, error) {
	var doc struct {
		Include []toml.Primitive `toml:"include"`
		Exclude []string         `toml:"exclude"`
	}
	md, err := toml.Decode(string(f.Content), &doc)
	if err != nil {
		return directives{}, errorAt("parse", refAtLine(f, errLine(err)),
			fmt.Errorf("%w: %v", ErrParse, err), nil)
	}

	var d directives
	d.Excludes = doc.Exclude

	// BurntSushi/toml exposes parsed values and a list of keys, but no
	// per-key position: only ParseError carries one, and by then the document
	// has already failed to parse. Positions for individual include entries
	// are therefore recovered by scanning the source text, which is reliable
	// here because every entry in the include list is a top-level key or
	// table with a literal name.
	lines := newLineScanner(string(f.Content))

	for _, prim := range doc.Include {
		inc, err := decodeTOMLInclude(prim, md, lines.nextIncludeLine())
		if err != nil {
			return directives{}, err
		}
		d.Includes = append(d.Includes, inc)
	}

	d.Misfiled = findMisfiledDirectives(string(f.Content), md)
	return d, nil
}

// findMisfiledDirectives reports include/exclude keys declared below the
// document root.
//
// The parsed key list is walked rather than the source text, because the key
// list is the authoritative account of where TOML actually filed each key. A
// text scan would have to reimplement TOML's table rules and would disagree
// with the parser exactly where it matters.
func findMisfiledDirectives(content string, md toml.MetaData) []misfiledDirective {
	var out []misfiledDirective
	lines := newLineScanner(content)

	for _, key := range md.Keys() {
		if len(key) < 2 {
			// len == 1 is the document root, which is the correct place.
			continue
		}
		last := key[len(key)-1]
		if last != "include" && last != "exclude" {
			continue
		}

		joined := strings.Join(key, ".")
		out = append(out, misfiledDirective{
			Key:  joined,
			Line: lines.nextLineOf(joined),
		})
	}
	return out
}

// lineScanner locates successive include directives in TOML source text.
//
// It walks the file once, tracking the line of each `include` key and of each
// `[[include]]` header, in the order they appear. The order matches the order
// BurntSushi/toml returns entries in, which is document order.
type lineScanner struct {
	lines []string
	next  int
	found []int
}

// newLineScanner indexes the positions of include directives in src.
func newLineScanner(src string) *lineScanner {
	s := &lineScanner{lines: strings.Split(src, "\n")}
	for i, raw := range s.lines {
		line := strings.TrimSpace(stripTOMLComment(raw))
		switch {
		case line == "[[include]]":
			s.found = append(s.found, i+1)
		case strings.HasPrefix(line, "include") && strings.Contains(line, "="):
			// Guard against keys that merely start with "include", such as
			// "includes" or "include_dir", by requiring the separator right
			// after the key or after whitespace.
			rest := strings.TrimSpace(strings.TrimPrefix(line, "include"))
			if strings.HasPrefix(rest, "=") {
				s.found = append(s.found, i+1)
			}
		}
	}
	return s
}

// nextIncludeLine returns the line of the next include directive, or zero
// once the recorded positions are exhausted.
func (s *lineScanner) nextIncludeLine() int {
	if s.next >= len(s.found) {
		return 0
	}
	line := s.found[s.next]
	s.next++
	return line
}

// nextLineOf returns the line where a dotted key path is written.
//
// The search tracks the current table header so that "log.include" is matched
// under [log] and not under an unrelated table. It returns zero when the key
// cannot be located, which leaves the warning usable even if the source has
// been reformatted in a way this scan does not anticipate.
func (s *lineScanner) nextLineOf(dotted string) int {
	last := dotted
	if i := strings.LastIndexByte(dotted, '.'); i >= 0 {
		last = dotted[i+1:]
	}

	var table string
	for i, raw := range s.lines {
		line := strings.TrimSpace(stripTOMLComment(raw))

		if strings.HasPrefix(line, "[") {
			table = strings.Trim(line, "[]")
			continue
		}

		if !strings.HasPrefix(line, last) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, last))
		if !strings.HasPrefix(rest, "=") {
			continue
		}

		// A key under [log] is written as log.include; one at the root has an
		// empty table prefix. Matching both parts is what keeps a nested key
		// from being reported at a top-level key of the same name.
		want := last
		if table != "" {
			want = table + "." + last
		}
		if want == dotted {
			return i + 1
		}
	}
	return 0
}

// stripTOMLComment removes a trailing comment from a line.
//
// It is deliberately naive about `#` inside quoted strings, because the only
// lines it is applied to are include keys and table headers, where a quoted
// `#` would have to appear inside a table name. Mis-stripping such a line
// would at worst lose a position, not corrupt a value.
func stripTOMLComment(line string) string {
	if i := strings.IndexByte(line, '#'); i >= 0 {
		return line[:i]
	}
	return line
}

// decodeTOMLInclude turns one include entry into a directive, accepting both
// the bare-string and the table spelling.
func decodeTOMLInclude(prim toml.Primitive, md toml.MetaData, fallbackLine int) (include, error) {
	// The shorthand form: a plain string.
	var asString string
	if err := md.PrimitiveDecode(prim, &asString); err == nil {
		return include{kind: kindPath, Pattern: asString, Line: fallbackLine}, nil
	}

	// The structured form: a table with path/format/optional.
	var asTable struct {
		Path     string `toml:"path"`
		Format   string `toml:"format"`
		Optional bool   `toml:"optional"`
	}
	if err := md.PrimitiveDecode(prim, &asTable); err != nil {
		return include{}, errorAt("parse", ir.SourceRef{Format: ir.FormatTOML, Line: fallbackLine},
			fmt.Errorf("%w: include entry must be a string or a table with a path key", ErrParse), nil)
	}
	if asTable.Path == "" {
		return include{}, errorAt("parse", ir.SourceRef{Format: ir.FormatTOML, Line: fallbackLine},
			fmt.Errorf("%w: structured include is missing the path key", ErrParse), nil)
	}

	inc := include{kind: kindStructured, Pattern: asTable.Path, Optional: asTable.Optional, Line: fallbackLine}
	if asTable.Format != "" {
		f, err := parseFormat(asTable.Format, ir.SourceRef{Line: fallbackLine})
		if err != nil {
			return include{}, err
		}
		inc.Format = f
	}
	return inc, nil
}

// parseYAMLDirectives reads include and exclude from a YAML document.
func parseYAMLDirectives(f *ir.File) (directives, error) {
	var doc struct {
		Include []yaml.Node `yaml:"include"`
		Exclude []string    `yaml:"exclude"`
	}
	if err := yaml.Unmarshal(f.Content, &doc); err != nil {
		return directives{}, errorAt("parse", refAtLine(f, yamlErrLine(err)),
			fmt.Errorf("%w: %v", ErrParse, err), nil)
	}

	var d directives
	d.Excludes = doc.Exclude

	for i := range doc.Include {
		node := &doc.Include[i]
		line := node.Line
		switch node.Kind {
		case yaml.ScalarNode:
			var s string
			if err := node.Decode(&s); err != nil {
				return directives{}, errorAt("parse", refAtLine(f, line),
					fmt.Errorf("%w: include entry is not a string: %v", ErrParse, err), nil)
			}
			d.Includes = append(d.Includes, include{kind: kindPath, Pattern: s, Line: line})

		case yaml.MappingNode:
			var tbl struct {
				Path     string `yaml:"path"`
				Format   string `yaml:"format"`
				Optional bool   `yaml:"optional"`
			}
			if err := node.Decode(&tbl); err != nil {
				return directives{}, errorAt("parse", refAtLine(f, line),
					fmt.Errorf("%w: %v", ErrParse, err), nil)
			}
			if tbl.Path == "" {
				return directives{}, errorAt("parse", refAtLine(f, line),
					fmt.Errorf("%w: structured include is missing the path key", ErrParse), nil)
			}
			inc := include{kind: kindStructured, Pattern: tbl.Path, Optional: tbl.Optional, Line: line}
			if tbl.Format != "" {
				fmtVal, err := parseFormat(tbl.Format, refAtLine(f, line))
				if err != nil {
					return directives{}, err
				}
				inc.Format = fmtVal
			}
			d.Includes = append(d.Includes, inc)

		default:
			return directives{}, errorAt("parse", refAtLine(f, line),
				fmt.Errorf("%w: include entry must be a string or a mapping with a path key", ErrParse), nil)
		}
	}
	return d, nil
}

// parseFormat validates an explicitly declared format.
//
// An unrecognised value is an error rather than a fallback, for the same
// reason an unknown extension is: guessing here would send the content to the
// wrong parser.
func parseFormat(s string, ref ir.SourceRef) (ir.Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "toml":
		return ir.FormatTOML, nil
	case "yaml", "yml":
		return ir.FormatYAML, nil
	default:
		return ir.FormatUnknown, errorAt("parse", ref,
			fmt.Errorf("%w: unknown format %q, expected \"toml\" or \"yaml\"", ErrFormatUnknown, s), nil)
	}
}

// refAtLine copies a file's reference and sets the line, so that every error
// carries a complete position rather than a bare path.
func refAtLine(f *ir.File, line int) ir.SourceRef {
	ref := f.Ref
	ref.Line = line
	return ref
}

// errLine extracts a line number from a TOML decode error, returning zero
// when the error carries no position.
//
// BurntSushi/toml exposes positions through ParseError. Anything else (for
// example a type mismatch detected after a successful parse) is reported
// against the file rather than a specific line.
func errLine(err error) int {
	var perr toml.ParseError
	if errors.As(err, &perr) {
		return perr.Position.Line
	}
	return 0
}

// yamlErrLine extracts a line number from a YAML decode error.
//
// yaml.v3 reports syntax errors as *yaml.TypeError with per-item messages
// that do not carry structured positions, so this returns zero and the error
// is attributed to the file. yaml.Node.Line is used directly where a position
// is genuinely available, which covers the include directives themselves.
func yamlErrLine(err error) int {
	var terr *yaml.TypeError
	if errors.As(err, &terr) {
		return 0
	}
	return 0
}
