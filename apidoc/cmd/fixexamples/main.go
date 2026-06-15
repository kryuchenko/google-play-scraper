// Command fixexamples post-processes swag's generated spec, converting the
// deprecated schema-level `example` keyword to the OpenAPI 3.1 `examples` array.
// It is run by gen.sh after `swag init`. See package specfix for the rationale.
//
// Usage: fixexamples -dir docs
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kryuchenko/google-play-scraper/apidoc/internal/specfix"
)

func main() {
	dir := flag.String("dir", "docs", "directory containing swagger.yaml, swagger.json and docs.go")
	flag.Parse()

	if err := specfix.Transform(*dir); err != nil {
		fmt.Fprintf(os.Stderr, "fixexamples: %v\n", err)
		os.Exit(1)
	}
}
