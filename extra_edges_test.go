package googleplayscraper

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// listPageWithSections builds a top-charts ds:4 page with three collection
// sections (free/paid/grossing), each carrying the given app IDs at
// section[21][0]. parseListApp reads each app's AppID at [0][0].
func listPageWithSections(t *testing.T, free, paid, grossing []string) []byte {
	t.Helper()
	mkApp := func(id string) any {
		return []any{[]any{id}, nil, nil, "Title " + id}
	}
	section := func(ids []string) any {
		s := make([]any, 22)
		apps := make([]any, 0, len(ids))
		for _, id := range ids {
			apps = append(apps, mkApp(id))
		}
		s[21] = []any{apps}
		return s
	}
	ds4 := []any{
		[]any{nil, []any{section(free), section(paid), section(grossing)}},
	}
	raw, err := json.Marshal(ds4)
	if err != nil {
		t.Fatalf("marshal ds4: %v", err)
	}
	return htmlWithDataBlocks(map[string]string{"ds:4": string(raw)})
}

func TestParseListPageSectionSelection(t *testing.T) {
	page := listPageWithSections(t,
		[]string{"com.free1", "com.free2"},
		[]string{"com.paid1"},
		[]string{"com.gross1"},
	)
	tests := []struct {
		col  Collection
		want string
	}{
		{CollectionTopFree, "com.free1"},
		{CollectionTopPaid, "com.paid1"},
		{CollectionGrossing, "com.gross1"},
	}
	for _, tt := range tests {
		results, err := parseListPage(page, ListOptions{Collection: tt.col, Num: 10})
		if err != nil {
			t.Fatalf("%s: parseListPage: %v", tt.col, err)
		}
		if len(results) == 0 || results[0].AppID != tt.want {
			t.Errorf("%s: first app = %v, want %s", tt.col, results, tt.want)
		}
	}
}

func TestParseListPageNumLimit(t *testing.T) {
	page := listPageWithSections(t,
		[]string{"com.a", "com.b", "com.c", "com.d"}, nil, nil,
	)
	results, err := parseListPage(page, ListOptions{Collection: CollectionTopFree, Num: 2})
	if err != nil {
		t.Fatalf("parseListPage: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("got %d apps, want 2 (capped by Num)", len(results))
	}
}

func TestParseListPageFallbackToOtherSection(t *testing.T) {
	// The requested TOP_PAID section is empty, so parseListPage scans the other
	// sections and returns whatever apps exist.
	page := listPageWithSections(t,
		[]string{"com.free1"}, nil, nil,
	)
	results, err := parseListPage(page, ListOptions{Collection: CollectionTopPaid, Num: 10})
	if err != nil {
		t.Fatalf("parseListPage: %v", err)
	}
	if len(results) != 1 || results[0].AppID != "com.free1" {
		t.Errorf("fallback results = %v, want [com.free1]", results)
	}
}

// TestSearchPaginates drives Search across the page-1 HTML plus a qnKhOb
// continuation page, exercising the pagination loop in Search.
func TestSearchPaginates(t *testing.T) {
	// The page shows two apps; asking for more than a page fetches the whole
	// list with one lGYRle call, whose payload has the page's ds:4 shape.
	page1 := searchHTMLPage(t, []string{"com.s1", "com.s2"}, "")

	var mu sync.Mutex
	var calls int
	c := newMockClient(t,
		routePath("/store/search", page1),
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch || req.URL.Query().Get("rpcids") != "lGYRle" {
				return mockResponse{}, false
			}
			mu.Lock()
			defer mu.Unlock()
			calls++
			return mockResponse{Body: batchEnvelope("lGYRle",
				searchDS4(t, []string{"com.s1", "com.s2", "com.s3", "com.s4"}))}, true
		},
	)

	results, err := c.Search(context.Background(), SearchOptions{Term: "x", Num: 30})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("got %d results, want the 4 the RPC returned", len(results))
	}
	if calls != 1 {
		t.Errorf("lGYRle calls = %d, want 1", calls)
	}
}

// searchHTMLPage builds a search page (ds:4) with apps at [0][1][0][0][0] and a
// pagination token at [0][1][0][0][3][0], matching extractSearchResults.
func searchHTMLPage(t *testing.T, appIDs []string, token string) []byte {
	t.Helper()
	return htmlWithDataBlocks(map[string]string{"ds:4": searchDS4WithToken(t, appIDs, token)})
}

// searchDS4 is the ds:4 block of a search page, which is also the payload
// shape of the lGYRle RPC.
func searchDS4(t *testing.T, appIDs []string) string {
	t.Helper()
	return searchDS4WithToken(t, appIDs, "")
}

func searchDS4WithToken(t *testing.T, appIDs []string, token string) string {
	t.Helper()
	apps := make([]any, 0, len(appIDs))
	for _, id := range appIDs {
		// parseSearchResultNew reads AppID at [0][0] and Title at [3].
		apps = append(apps, []any{[]any{id}, nil, nil, "Title " + id})
	}
	inner := make([]any, 4)
	inner[0] = apps
	inner[3] = []any{token}
	ds4 := []any{[]any{nil, []any{[]any{inner}}}}
	raw, err := json.Marshal(ds4)
	if err != nil {
		t.Fatalf("marshal search page: %v", err)
	}
	return string(raw)
}

func TestSearchNumClampedToMax(t *testing.T) {
	// A Num above searchMaxNum is clamped to it: the RPC is asked for 50, and
	// when it has nothing more the page's results stand.
	page := searchHTMLPage(t, []string{"com.only"}, "")
	var asked string
	c := newMockClient(t, routePath("/store/search", page),
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch {
				return mockResponse{}, false
			}
			body, _ := io.ReadAll(req.Body)
			asked, _ = url.QueryUnescape(string(body))
			return mockResponse{Body: emptySearchRPC}, true
		})

	results, err := c.Search(context.Background(), SearchOptions{Term: "x", Num: 99999})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("got %d results, want 1", len(results))
	}
	if !strings.Contains(asked, "[[8,[50,50]]") {
		t.Errorf("the RPC was not asked for searchMaxNum results: %.120s", asked)
	}
}

func TestListRequestErrorPropagates(t *testing.T) {
	// Both the batch RPC and the HTML fallback fail, so List surfaces the error.
	c := newMockClient(t,
		routePathStatus(pathBatch, http.StatusInternalServerError),
		routePathStatus(pathTop, http.StatusInternalServerError),
	)
	if _, err := c.List(context.Background(), ListOptions{Collection: CollectionTopFree}); err == nil {
		t.Fatal("expected error when both batch and HTML fallback fail")
	}
}

// --- batchexecute parser error branches ---

func TestParseReviewsResponseErrors(t *testing.T) {
	if _, err := parseReviewsResponse([]byte(""), "com.x"); err == nil {
		t.Error("empty body should error")
	}
	if _, err := parseReviewsResponse([]byte(")]}'\nnot json"), "com.x"); err == nil {
		t.Error("malformed outer JSON should error")
	}
}

// TestParseDataEntries covers the optional flag and purpose mapping plus the
// skipping of malformed entries.
func TestParseDataEntries(t *testing.T) {
	in := []any{
		[]any{ // category "Personal info"
			[]any{nil, "Personal info"},
			nil, nil, nil,
			[]any{
				[]any{"Name", float64(0), "App functionality"},   // required
				[]any{"Email", float64(1), "Account management"}, // optional
				[]any{""}, // skipped: empty data name
			},
		},
		"not-an-array", // skipped
	}
	got := parseDataEntries(in)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Data != "Name" || got[0].Optional || got[0].Type != "Personal info" {
		t.Errorf("entry 0 = %+v", got[0])
	}
	if got[1].Data != "Email" || !got[1].Optional || got[1].Purpose != "Account management" {
		t.Errorf("entry 1 = %+v", got[1])
	}

	if got := parseDataEntries("not-an-array"); got != nil {
		t.Errorf("parseDataEntries(non-array) = %v, want nil", got)
	}
}
