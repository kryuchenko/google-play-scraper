package apidoc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"
)

// TestSpecIsValidOpenAPI validates the generated spec against the OpenAPI 3.1
// rules with a Go-native validator (no python/npx needed), so the generator
// can never commit a structurally invalid document. It runs on both the YAML
// and JSON outputs since downstream tools consume either.
func TestSpecIsValidOpenAPI(t *testing.T) {
	for _, name := range []string{"swagger.yaml", "swagger.json"} {
		name := name
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("docs", name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}

			doc, err := libopenapi.NewDocument(data)
			if err != nil {
				t.Fatalf("%s: parse failed: %v", name, err)
			}
			if v := doc.GetSpecInfo().Version; v != "3.1.0" {
				t.Errorf("%s: OpenAPI version = %q, want 3.1.0", name, v)
			}

			// Building the v3 model surfaces structural errors (bad refs,
			// malformed objects, etc.).
			if _, mErr := doc.BuildV3Model(); mErr != nil {
				t.Errorf("%s: model error: %v", name, mErr)
			}

			v, errs := validator.NewValidator(doc)
			if len(errs) > 0 {
				t.Fatalf("%s: validator build failed: %v", name, errs)
			}
			ok, vErrs := v.ValidateDocument()
			if !ok {
				for _, e := range vErrs {
					t.Errorf("%s: %s: %s", name, e.ValidationType, e.Message)
				}
			}
		})
	}
}
