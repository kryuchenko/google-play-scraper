package apidoc

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// stubs is every annotation carrier in google_endpoints.go. They are never
// called -- swag reads their doc comments -- so this list is what keeps them
// referenced, and the test below is what keeps the list honest: a stub added
// without an entry here, or one whose annotations drifted, fails.
var stubs = []func(){
	getAppDetails, getAppAvailability, getPCAppDetails, searchApps, topApps,
	categoryApps, developerAppsNumeric, developerAppsName, getDataSafety,
	getCluster, batchListVyAe2, batchPaginateQnKhOb, batchSearchLGYRle,
	batchReviewsOCPfdb, batchPermissionsXdSrCf, batchAppDetailsWs7gDc,
	batchSuggestIJ4APc, getRobotsTxt, getSitemapIndex, getSitemapShard,
}

// Every stub carries a @Router and an @Id equal to its name, and the list
// above names exactly the stubs the file declares.
func TestEndpointStubsAreAnnotated(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "google_endpoints.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var declared []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fn.Name.Name
		declared = append(declared, name)
		doc := fn.Doc.Text()
		if !strings.Contains(doc, "@Router") {
			t.Errorf("%s has no @Router", name)
		}
		if !idMatches(doc, name) {
			t.Errorf("%s: @Id does not match the function name", name)
		}
	}

	var listed []string
	for _, s := range stubs {
		full := runtime.FuncForPC(reflect.ValueOf(s).Pointer()).Name()
		listed = append(listed, full[strings.LastIndex(full, ".")+1:])
	}
	slices.Sort(declared)
	slices.Sort(listed)
	if !slices.Equal(declared, listed) {
		t.Errorf("stubs declared %v, listed %v", declared, listed)
	}
}

// idMatches reports whether doc has an "@Id <name>" line.
func idMatches(doc, name string) bool {
	for line := range strings.SplitSeq(doc, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "@Id" && f[1] == name {
			return true
		}
	}
	return false
}
