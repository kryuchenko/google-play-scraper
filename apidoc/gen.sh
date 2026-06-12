#!/usr/bin/env bash
# Regenerate the OpenAPI/Swagger spec for the private Google Play endpoints.
# Run from the apidoc/ directory. Outputs docs/docs.go, docs/swagger.json and
# docs/swagger.yaml. swag is fetched on demand so it never touches the root
# zero-dependency module.
set -euo pipefail
cd "$(dirname "$0")"
exec go run github.com/swaggo/swag/cmd/swag@v1.16.4 init \
	-g doc.go -o docs --parseDependency --parseInternal
