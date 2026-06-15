#!/usr/bin/env bash
# Regenerate the OpenAPI/Swagger spec for the private Google Play endpoints.
# Run from the apidoc/ directory. Outputs docs/docs.go, docs/swagger.json and
# docs/swagger.yaml. swag is fetched on demand so it never touches the root
# zero-dependency module.
set -euo pipefail
cd "$(dirname "$0")"
go run github.com/swaggo/swag/v2/cmd/swag@v2.0.0-rc5 init \
	-g doc.go -o docs --parseDependency --parseInternal --v3.1

# swag v2 only emits the singular, 3.1-deprecated `example` for schema objects.
# Convert schema-level `example` -> `examples` array (the JSON Schema 2020-12
# form) deterministically. The freshness test runs this same transform, so the
# committed files and any regeneration stay byte-identical. See internal/specfix.
go run ./cmd/fixexamples -dir docs

# Normalize docs.go formatting (the post-processor rewrites its embedded JSON).
gofmt -w docs/docs.go
