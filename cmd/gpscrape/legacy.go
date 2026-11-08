package main

import (
	"encoding/json"
	"strings"
	"unicode"
)

// Catalog files written before v1.7 -- manifests, the resumable sweep state
// and the `catalog new` log -- used camelCase keys (completedAt, samplePct,
// appId). The records are snake_case now, like the rest of the CLI's output
// and the MCP server's; these decoders read both, so a catalog directory
// built by an older version keeps working. New files are written snake_case.

func (m *manifest) UnmarshalJSON(b []byte) error {
	type plain manifest
	return unmarshalLegacy(b, (*plain)(m))
}

func (s *syncState) UnmarshalJSON(b []byte) error {
	type plain syncState
	return unmarshalLegacy(b, (*plain)(s))
}

func (r *newRecord) UnmarshalJSON(b []byte) error {
	type plain newRecord
	return unmarshalLegacy(b, (*plain)(r))
}

// unmarshalLegacy decodes the object in b into v after renaming its
// top-level camelCase keys to snake_case. A key already present in snake_case
// wins over its camelCase twin.
func unmarshalLegacy(b []byte, v any) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return json.Unmarshal(b, v) // not an object: let v report it
	}
	changed := false
	for k, val := range m {
		s := snakeCase(k)
		if s == k {
			continue
		}
		if _, ok := m[s]; !ok {
			m[s] = val
		}
		delete(m, k)
		changed = true
	}
	if changed {
		var err error
		if b, err = json.Marshal(m); err != nil {
			return err
		}
	}
	return json.Unmarshal(b, v)
}

func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}
