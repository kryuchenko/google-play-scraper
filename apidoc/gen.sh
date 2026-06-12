#!/usr/bin/env bash
# Regenerate the OpenAPI/Swagger spec for the private Google Play endpoints.
# Run from the apidoc/ directory. Outputs docs/docs.go, docs/swagger.json and
# docs/swagger.yaml. swag is fetched on demand so it never touches the root
# zero-dependency module.
set -euo pipefail
cd "$(dirname "$0")"
exec go run github.com/swaggo/swag/v2/cmd/swag@v2.0.0-rc5 init \
	-g doc.go -o docs --parseDependency --parseInternal --v3.1
