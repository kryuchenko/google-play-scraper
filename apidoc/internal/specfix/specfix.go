// Package specfix post-processes the swagger/OpenAPI files that swag generates,
// converting the deprecated schema-level `example` keyword to the OpenAPI 3.1 /
// JSON Schema 2020-12 `examples` array form.
//
// Why this exists: swag v2 (pinned at v2.0.0-rc5, the latest release) only emits
// the singular `example` for schema objects, which 3.1 deprecates. Strict
// validators warn on it. swag has no `examples` mechanism and no newer version,
// so the spec is corrected here, deterministically, as a generation step.
//
// Scope is deliberately narrow: ONLY Schema Objects under `components.schemas`
// are rewritten. The Parameter Object's own `example` field (under `paths`) is
// NOT deprecated in 3.1 and is left untouched — the transform never descends
// into `paths`.
//
// Transform reads swagger.yaml as the single source structure, then re-emits all
// three generated artifacts (swagger.yaml, swagger.json, docs.go) from it so
// they stay consistent. It is invoked by gen.sh after `swag init`, and by the
// freshness test, so both the committed files and any regeneration pass through
// the identical (deterministic) transform.
package specfix

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// schemesPlaceholder is the Go-template directive swag embeds in docs.go's JSON
// string. It is not valid JSON, so it is swapped for a sentinel during parsing
// and restored afterwards.
const (
	schemesPlaceholder = "{{ marshal .Schemes }}"
	schemesSentinel    = "@@SPECFIX_SCHEMES@@"
)

// Transform rewrites swagger.yaml, swagger.json and docs.go in dir so that every
// schema-level `example` becomes an `examples` array. It is idempotent: running
// it on already-transformed files is a no-op (no `example` keys remain under the
// schemas subtree).
func Transform(dir string) error {
	if err := transformYAMLAndJSON(dir); err != nil {
		return err
	}
	return transformDocsGo(dir)
}

// transformYAMLAndJSON reads swagger.yaml, applies the conversion, and writes
// both swagger.yaml (YAML) and swagger.json (JSON) from the same structure.
func transformYAMLAndJSON(dir string) error {
	yamlPath := filepath.Join(dir, "swagger.yaml")
	raw, err := os.ReadFile(yamlPath)
	if err != nil {
		return fmt.Errorf("read swagger.yaml: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse swagger.yaml: %w", err)
	}

	convertSchemasExamples(rootMapping(&doc))

	var yb bytes.Buffer
	enc := yaml.NewEncoder(&yb)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode swagger.yaml: %w", err)
	}
	enc.Close()
	if err := os.WriteFile(yamlPath, yb.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write swagger.yaml: %w", err)
	}

	jsonBytes, err := nodeToJSON(rootMapping(&doc))
	if err != nil {
		return fmt.Errorf("render swagger.json: %w", err)
	}
	jsonBytes = append(jsonBytes, '\n')
	if err := os.WriteFile(filepath.Join(dir, "swagger.json"), jsonBytes, 0o644); err != nil {
		return fmt.Errorf("write swagger.json: %w", err)
	}
	return nil
}

// docsGoJoiner is how a literal backtick is embedded inside the docTemplate
// raw-string const: the raw string is split and a quoted backtick concatenated,
// i.e. ...` + "`" + `... It is used when RE-embedding (gen.sh runs gofmt on
// docs.go afterwards, so the exact spacing here is normalized regardless).
const docsGoJoiner = "` + \"`\" + `"

// docsGoJoinerRe matches that same joiner when PARSING, tolerant of any
// whitespace around the `+` operators (gofmt's spacing can vary), so the
// collapse back to a single backtick is robust.
var docsGoJoinerRe = regexp.MustCompile("`\\s*\\+\\s*\"`\"\\s*\\+\\s*`")

// transformDocsGo rewrites the JSON embedded in docs.go's docTemplate const.
// Two encodings must be undone first: swag splits the raw string around literal
// backticks (docsGoJoiner), and it embeds a `{{ marshal .Schemes }}` template
// directive that is not valid JSON. Both are reversed before parsing and
// re-applied after rendering, so everything outside the JSON payload is
// preserved verbatim.
func transformDocsGo(dir string) error {
	path := filepath.Join(dir, "docs.go")
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read docs.go: %w", err)
	}
	text := string(src)

	const open = "const docTemplate = `"
	openIdx := strings.Index(text, open)
	if openIdx < 0 {
		return fmt.Errorf("docs.go: docTemplate opening not found")
	}
	exprStart := openIdx + len(open) - 1 // include the opening backtick

	// The docTemplate expression ends at the backtick just before the blank line
	// preceding the SwaggerInfo declaration.
	tailIdx := strings.Index(text[exprStart:], "\n\n// SwaggerInfo")
	if tailIdx < 0 {
		return fmt.Errorf("docs.go: docTemplate end marker not found")
	}
	exprEnd := exprStart + tailIdx // exclusive; text[exprEnd-1] is the closing backtick

	expr := text[exprStart:exprEnd]
	// Collapse the segment joiners to literal backticks, then strip the outer
	// delimiting backticks to recover the raw JSON payload.
	jsonText := docsGoJoinerRe.ReplaceAllString(expr, "`")
	jsonText = strings.TrimPrefix(jsonText, "`")
	jsonText = strings.TrimSuffix(jsonText, "`")
	jsonText = strings.Replace(jsonText, schemesPlaceholder, `"`+schemesSentinel+`"`, 1)

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(jsonText), &doc); err != nil {
		return fmt.Errorf("parse docs.go docTemplate: %w", err)
	}
	convertSchemasExamples(rootMapping(&doc))

	jsonBytes, err := nodeToJSON(rootMapping(&doc))
	if err != nil {
		return fmt.Errorf("render docs.go docTemplate: %w", err)
	}
	out := strings.Replace(string(jsonBytes), `"`+schemesSentinel+`"`, schemesPlaceholder, 1)
	// Re-embed: restore the segment joiners around any literal backticks, then
	// wrap in the outer delimiting backticks.
	out = "`" + strings.ReplaceAll(out, "`", docsGoJoiner) + "`"

	var buf bytes.Buffer
	buf.WriteString(text[:exprStart])
	buf.WriteString(out)
	buf.WriteString(text[exprEnd:])
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write docs.go: %w", err)
	}
	return nil
}

// rootMapping returns the top-level mapping node of a parsed YAML/JSON document.
func rootMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

// convertSchemasExamples finds components.schemas in the root mapping and
// converts every schema-level `example` beneath it. It is a no-op when the
// subtree is absent.
func convertSchemasExamples(root *yaml.Node) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	schemas := childValue(childValue(root, "components"), "schemas")
	convertExamplesRec(schemas)
}

// convertExamplesRec walks a node subtree and, for every mapping that carries an
// `example` key, renames it to `examples` and wraps the value in a one-element
// sequence (the 3.1 form). It recurses through all nested mappings and
// sequences, so properties, items, allOf/oneOf/anyOf members and
// additionalProperties are all covered. Because it is only ever called on the
// components.schemas subtree, every `example` it meets is schema-level.
func convertExamplesRec(node *yaml.Node) {
	if node == nil {
		return
	}
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			val := node.Content[i+1]
			if key.Value == "example" {
				key.Value = "examples"
				key.Tag = "!!str"
				seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{val}}
				node.Content[i+1] = seq
				// Recurse into the original value in case it is itself a
				// schema-bearing structure (it is not, for example values, but
				// the walk stays uniform and harmless).
				convertExamplesRec(val)
				continue
			}
			convertExamplesRec(val)
		}
	case yaml.SequenceNode:
		for _, c := range node.Content {
			convertExamplesRec(c)
		}
	}
}

// childValue returns the value node for key in a mapping node, or nil.
func childValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// nodeToJSON renders a YAML node tree as indented JSON (4-space, matching swag's
// style), preserving mapping key order. It handles the scalar tags that appear
// in an OpenAPI document; an unknown scalar tag is emitted as a JSON string.
func nodeToJSON(node *yaml.Node) ([]byte, error) {
	var b bytes.Buffer
	if err := writeJSON(&b, node, 0); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeJSON(b *bytes.Buffer, node *yaml.Node, depth int) error {
	const unit = "    "
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) == 0 {
			b.WriteString("null")
			return nil
		}
		return writeJSON(b, node.Content[0], depth)

	case yaml.MappingNode:
		if len(node.Content) == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteString("{\n")
		pad := strings.Repeat(unit, depth+1)
		for i := 0; i+1 < len(node.Content); i += 2 {
			b.WriteString(pad)
			b.WriteString(encodeJSONString(node.Content[i].Value))
			b.WriteString(": ")
			if err := writeJSON(b, node.Content[i+1], depth+1); err != nil {
				return err
			}
			if i+2 < len(node.Content) {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat(unit, depth))
		b.WriteByte('}')
		return nil

	case yaml.SequenceNode:
		if len(node.Content) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteString("[\n")
		pad := strings.Repeat(unit, depth+1)
		for i, c := range node.Content {
			b.WriteString(pad)
			if err := writeJSON(b, c, depth+1); err != nil {
				return err
			}
			if i+1 < len(node.Content) {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat(unit, depth))
		b.WriteByte(']')
		return nil

	case yaml.ScalarNode:
		return writeJSONScalar(b, node)

	default:
		return fmt.Errorf("unsupported YAML node kind %v", node.Kind)
	}
}

func writeJSONScalar(b *bytes.Buffer, node *yaml.Node) error {
	switch node.Tag {
	case "!!bool":
		if node.Value == "true" || node.Value == "false" {
			b.WriteString(node.Value)
			return nil
		}
		// Non-canonical bool literal (yes/no/on/off): normalize to JSON bool.
		switch strings.ToLower(node.Value) {
		case "yes", "on", "y", "true":
			b.WriteString("true")
		default:
			b.WriteString("false")
		}
		return nil
	case "!!int", "!!float":
		b.WriteString(node.Value)
		return nil
	case "!!null":
		b.WriteString("null")
		return nil
	default: // !!str and anything else → JSON string
		b.WriteString(encodeJSONString(node.Value))
		return nil
	}
}

// encodeJSONString returns s as a JSON string literal with HTML escaping
// disabled, so characters like < > & survive verbatim (URLs, HTML snippets in
// descriptions) the way swag emits them.
func encodeJSONString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(buf.String(), "\n")
}
