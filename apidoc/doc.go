// Package apidoc holds an auto-generated OpenAPI/Swagger description of the
// PRIVATE, undocumented Google Play HTTP endpoints that the
// github.com/kryuchenko/google-play-scraper library calls.
//
// This is a documentation-only, nested Go module: it depends on swaggo so that
// the root scraper module can stay zero-dependency. Nothing here is imported by
// the scraper itself; the dependency direction is apidoc -> root.
//
// Regenerate the spec with `go generate ./...` from this directory (see the
// directive below) or `./gen.sh`.
//
// @title           Google Play Store private HTTP API (reverse-engineered)
// @version         1.0.0
// @description     Reverse-engineered description of the PRIVATE, undocumented
// @description     HTTP endpoints of the Google Play Store web frontend that the
// @description     google-play-scraper Go library calls.
// @description
// @description     DISCLAIMER: These endpoints are NOT a public, supported Google
// @description     API. This project is NOT affiliated with, endorsed by, or
// @description     sponsored by Google. The endpoints are undocumented, may change
// @description     or disappear without notice, and accessing them may be subject
// @description     to the Google Play / Google Terms of Service. This spec is
// @description     provided for interoperability and educational purposes only.
// @description
// @description     Two response shapes appear:
// @description       * AF_initDataCallback: GET endpoints return text/html whose
// @description         body embeds `AF_initDataCallback({key:'ds:N', data:[...]})`
// @description         script blocks; the scraper extracts the logical model from
// @description         a numbered data block (e.g. ds:5).
// @description       * batchexecute envelope: POST /_/PlayStoreUi/data/batchexecute
// @description         returns a `)]}'`-prefixed, `wrb.fr`-framed envelope; the
// @description         scraper decodes the inner URL-encoded JSON for the RPC.
//
// @host      play.google.com
// @BasePath  /
// @schemes   https
//
// @license.name  MIT
// @license.url   https://github.com/kryuchenko/google-play-scraper/blob/main/LICENSE
//
// @externalDocs.description  google-play-scraper source
// @externalDocs.url          https://github.com/kryuchenko/google-play-scraper
//
//go:generate go run github.com/swaggo/swag/cmd/swag@v1.16.4 init -g doc.go -o docs --parseDependency --parseInternal
package apidoc
