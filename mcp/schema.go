package gpsmcp

import (
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// inputSchema infers a tool's input schema from In and tightens it with the
// `schema` struct tag, so bounds and allowed values sit in the schema where a
// client can check them, not only in the description text:
//
//	schema:"min=1,max=50"           integer bounds
//	schema:"enum=a|b|c"             allowed strings
//	schema:"minItems=1,maxItems=20" array length
//
// Every argument is a value or absent, never null, so the "null" the
// inference adds for slices and pointers is dropped.
func inputSchema[In any]() *jsonschema.Schema {
	s, err := jsonschema.For[In](nil)
	if err != nil {
		panic(err)
	}
	tighten(reflect.TypeFor[In](), s)
	return s
}

func tighten(t reflect.Type, s *jsonschema.Schema) {
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			tighten(f.Type, s) // embedded fields are inlined, as in JSON
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		prop := s.Properties[name]
		if prop == nil {
			continue
		}
		if i := slices.Index(prop.Types, "null"); i >= 0 {
			prop.Types = slices.Delete(prop.Types, i, i+1)
			if len(prop.Types) == 1 {
				prop.Type, prop.Types = prop.Types[0], nil
			}
		}
		for kv := range strings.SplitSeq(f.Tag.Get("schema"), ",") {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			switch k {
			case "min":
				prop.Minimum = ptr(mustFloat(v))
			case "max":
				prop.Maximum = ptr(mustFloat(v))
			case "minItems":
				prop.MinItems = ptr(int(mustFloat(v)))
			case "maxItems":
				prop.MaxItems = ptr(int(mustFloat(v)))
			case "enum":
				for e := range strings.SplitSeq(v, "|") {
					prop.Enum = append(prop.Enum, e)
				}
			default:
				panic("unknown schema tag key " + k)
			}
		}
	}
}

func mustFloat(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		panic(err)
	}
	return f
}
