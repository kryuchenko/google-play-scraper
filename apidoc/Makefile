# Documentation-only module for the private Google Play HTTP endpoints.
# These targets never run from the root module, so the root stays zero-dependency.

SWAG := go run github.com/swaggo/swag/v2/cmd/swag@v2.0.0-rc5

.PHONY: gen test verify fmt

# gen regenerates docs/{docs.go,swagger.json,swagger.yaml} from the annotations.
gen:
	$(SWAG) init -g doc.go -o docs --parseDependency --parseInternal --v3.1

# test runs the drift tests (rpcid/path coverage + spec freshness).
test:
	go test ./...

# verify is the CI entry point: regenerate, then fail if anything changed,
# proving the committed spec matches the annotations.
verify: gen
	git diff --exit-code -- docs

# fmt formats the hand-written sources (generated docs/ is left as swag emits it).
fmt:
	gofmt -w doc.go models.go google_endpoints.go drift_test.go
