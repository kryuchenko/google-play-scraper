package apidoc

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kryuchenko/google-play-scraper/apidoc/internal/specfix"

	// Imported so the swag toolchain stays in go.mod/go.sum even though the
	// annotations it parses are plain comments. This keeps the generator
	// reproducible and lets `go test` fail loudly if the dependency drifts.
	_ "github.com/swaggo/swag/v2"
)

// knownRPCIDs is the set of batchexecute rpcid literals the scraper is expected
// to use. It mirrors the @x-rpcid extensions in google_endpoints.go and is the
// contract the drift test enforces against the real source.
var knownRPCIDs = []string{"IJ4APc", "oCPfdb", "qnKhOb", "vyAe2", "xdSrCf"}

// rpcidUsage finds batchexecute rpcids at their real call sites — generically,
// so a NEWLY added rpcid is detected too (that is the whole point of drift test
// A). The three alternatives cover the only places an rpcid appears as the
// leading token: a `rpcids=` / `rpcids": {"` query or map value, and the first
// element of a raw `[[["<rpcid>"` f.req payload array. Anchoring to these
// positions excludes trailing payload literals like "generic" or "null".
var rpcidUsage = regexp.MustCompile(`(?:rpcids[=":}{ ]+"?|\[\[\[\\?")([A-Za-z0-9]{4,9})`)

// TestRPCIDCoverage (drift test A) compares the rpcids actually used in the root
// scraper source against the ones documented here. A mismatch means either a new
// endpoint was added without docs, or an annotation outlived its endpoint.
func TestRPCIDCoverage(t *testing.T) {
	used := rpcidsInRootSource(t)
	documented := toSet(knownRPCIDs)

	for id := range used {
		if !documented[id] {
			t.Errorf("rpcid %q is used in the scraper but not documented in apidoc (add a stub in google_endpoints.go)", id)
		}
	}
	for id := range documented {
		if !used[id] {
			t.Errorf("rpcid %q is documented in apidoc but no longer used in the scraper (remove the dead stub)", id)
		}
	}
}

// TestDocumentedRPCIDsInSpec (drift test A, spec half) verifies every known
// rpcid is present in the committed swagger.json, catching a spec that was never
// regenerated after the stubs changed.
func TestDocumentedRPCIDsInSpec(t *testing.T) {
	spec := readSpec(t)
	blob, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("re-marshal spec: %v", err)
	}
	text := string(blob)
	for _, id := range knownRPCIDs {
		if !strings.Contains(text, id) {
			t.Errorf("rpcid %q is missing from docs/swagger.json (run ./gen.sh)", id)
		}
	}
}

// TestPathCoverage (drift test A, paths) asserts the canonical GET endpoints the
// scraper hits are all present as paths in the committed spec.
func TestPathCoverage(t *testing.T) {
	spec := readSpec(t)
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatal("spec has no paths object")
	}
	want := []string{
		"/store/apps/details",
		"/store/search",
		"/store/apps/top",
		"/store/apps/category/{category}",
		"/store/apps/dev",
		"/store/apps/developer",
		"/store/apps/datasafety",
		"/_/PlayStoreUi/data/batchexecute",
		"/robots.txt",
		"/sitemaps/sitemaps-index-{n}.xml",
		"/sitemaps/{shard}.xml.gz",
	}
	for _, p := range want {
		if !hasPathPrefix(paths, p) {
			t.Errorf("path %q is used by the scraper but missing from docs/swagger.json (run ./gen.sh)", p)
		}
	}
}

// rpcidsInRootSource scans the root scraper's non-test .go files for rpcid
// literals appearing in batchexecute contexts.
func rpcidsInRootSource(t *testing.T) map[string]bool {
	t.Helper()
	root := filepath.Join("..")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root dir: %v", err)
	}
	found := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, m := range rpcidUsage.FindAllStringSubmatch(string(src), -1) {
			found[m[1]] = true
		}
	}
	if len(found) == 0 {
		t.Fatal("found no rpcids in root source; the scanner regex likely broke")
	}
	return found
}

// readSpec loads the committed swagger.json.
func readSpec(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("docs", "swagger.json"))
	if err != nil {
		t.Fatalf("read docs/swagger.json (run ./gen.sh): %v", err)
	}
	var spec map[string]any
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse swagger.json: %v", err)
	}
	return spec
}

// hasPathPrefix reports whether any documented path equals base or starts with
// base plus a synthetic `(disambiguator)` segment, which we append to separate
// operations that share a real path+method (the batchexecute RPCs and the
// availability probe).
func hasPathPrefix(paths map[string]any, base string) bool {
	for p := range paths {
		if p == base || strings.HasPrefix(p, base+"(") {
			return true
		}
	}
	return false
}

func toSet(xs []string) map[string]bool {
	s := make(map[string]bool, len(xs))
	for _, x := range xs {
		s[x] = true
	}
	return s
}

// TestNoDeprecatedSchemaExample (drift test C) asserts that no Schema Object
// under components.schemas still carries the singular, 3.1-deprecated `example`
// keyword — they must all be the `examples` array form produced by specfix /
// gen.sh. It catches both a forgotten regeneration and a future swag change that
// reintroduces `example`. Parameter-level `example` under paths is NOT checked:
// it is the Parameter Object's own field and is valid in 3.1.
func TestNoDeprecatedSchemaExample(t *testing.T) {
	spec := readSpec(t)
	components, _ := spec["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	if len(schemas) == 0 {
		t.Fatal("components.schemas missing or empty in swagger.json")
	}
	for name, sch := range schemas {
		if hits := findExampleKeys(sch); len(hits) > 0 {
			t.Errorf("schema %q still uses deprecated `example` (%d occurrence(s)); run ./gen.sh", name, len(hits))
		}
	}
}

// findExampleKeys recursively collects every `example` key found in a decoded
// JSON value (maps and slices), used to assert none survive under the schemas
// subtree.
func findExampleKeys(v any) []string {
	var hits []string
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if k == "example" {
				hits = append(hits, k)
			}
			hits = append(hits, findExampleKeys(child)...)
		}
	case []any:
		for _, child := range t {
			hits = append(hits, findExampleKeys(child)...)
		}
	}
	return hits
}

// TestSpecIsFresh (drift test B) regenerates the spec into a temp directory and
// compares it against the committed docs/swagger.yaml, catching annotations that
// were edited without re-running ./gen.sh. It is skipped under -short and when
// swag cannot run (e.g. offline CI without the module cache), so it never causes
// a spurious failure; in those environments ./gen.sh + a git-diff check in CI is
// the equivalent guard.
func TestSpecIsFresh(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping spec-freshness regeneration in -short mode")
	}

	tmp := t.TempDir()
	cmd := exec.Command("go", "run",
		"github.com/swaggo/swag/v2/cmd/swag@v2.0.0-rc5",
		"init", "-g", "doc.go", "-o", tmp,
		"--parseDependency", "--parseInternal", "--v3.1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot run swag to verify freshness (treat ./gen.sh as the source of truth): %v\n%s", err, out)
	}

	// Apply the same post-processing gen.sh runs (example -> examples), so the
	// regenerated output is compared on equal footing with the committed files.
	if err := specfix.Transform(tmp); err != nil {
		t.Fatalf("specfix.Transform(regenerated): %v", err)
	}

	got, err := os.ReadFile(filepath.Join(tmp, "swagger.yaml"))
	if err != nil {
		t.Fatalf("read regenerated swagger.yaml: %v", err)
	}
	want, err := os.ReadFile(filepath.Join("docs", "swagger.yaml"))
	if err != nil {
		t.Fatalf("read committed docs/swagger.yaml: %v", err)
	}
	if string(got) != string(want) {
		t.Error("docs/swagger.yaml is stale: annotations changed without regenerating. Run ./gen.sh and commit docs/.")
	}
}
