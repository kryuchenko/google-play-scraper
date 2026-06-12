package googleplayscraper

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
)

// clusterMorePayload renders the qnKhOb pagination response that fetchMoreApps
// consumes. Apps live at data[0][0][0]; each app is a parseSearchResult row with
// AppID at [12][0] and Title at [2]. The next token sits at data[0][0][7][1].
func clusterMorePayload(t *testing.T, appIDs []string, nextToken string) []byte {
	t.Helper()
	apps := make([]interface{}, 0, len(appIDs))
	for _, id := range appIDs {
		row := make([]interface{}, 13)
		row[2] = "Title " + id
		row[12] = []interface{}{id}
		apps = append(apps, row)
	}
	inner0 := make([]interface{}, 8)
	inner0[0] = apps
	if nextToken != "" {
		inner0[7] = []interface{}{nil, nextToken}
	}
	data := []interface{}{[]interface{}{inner0}}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal cluster-more: %v", err)
	}
	return batchEnvelope("qnKhOb", string(raw))
}

// TestClusterPaginates drives Cluster across two pages: the initial HTML page
// carries a token, the first qnKhOb call returns more apps + a second token, and
// the second call returns an empty (tokenless) page that stops the loop.
func TestClusterPaginates(t *testing.T) {
	initialPage := clusterHTMLPage(t, []string{"com.a", "com.b"}, "tok1")

	var mu sync.Mutex
	var batchCalls int
	c := newMockClient(t,
		routePath(pathCluster, initialPage),
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch {
				return mockResponse{}, false
			}
			mu.Lock()
			defer mu.Unlock()
			batchCalls++
			if batchCalls == 1 {
				return mockResponse{Body: clusterMorePayload(t, []string{"com.c", "com.d"}, "tok2")}, true
			}
			return mockResponse{Body: clusterMorePayload(t, nil, "")}, true
		},
	)

	results, err := c.Cluster(context.Background(), ClusterOptions{Path: "/store/apps/collection/cluster?x=1"})
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	// 2 initial + 2 from page 1; page 2 is empty and stops the loop.
	if len(results) != 4 {
		t.Fatalf("got %d apps across pages, want 4", len(results))
	}
	if batchCalls != 2 {
		t.Errorf("batch calls = %d, want 2 (second page empty stops)", batchCalls)
	}
}

// clusterHTMLPage builds a cluster HTML page with apps at [0,1,0,21,0] and the
// pagination token at [0,1,0,3,0], matching parseClusterPage.
func clusterHTMLPage(t *testing.T, appIDs []string, token string) []byte {
	t.Helper()
	apps := make([]interface{}, 0, len(appIDs))
	for _, id := range appIDs {
		// parseSearchResultNew reads AppID at [0][0] and Title at [3].
		apps = append(apps, []interface{}{[]interface{}{id}, nil, nil, "Title " + id})
	}
	lvl := make([]interface{}, 22)
	lvl[3] = []interface{}{token} // [0,1,0,3,0] token
	lvl[21] = []interface{}{apps} // [0,1,0,21,0] apps
	node := []interface{}{
		[]interface{}{nil, []interface{}{lvl}}, // [0][1][0] = lvl
	}
	raw, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("marshal cluster page: %v", err)
	}
	return htmlWithDataBlocks(map[string]string{"ds:4": string(raw)})
}

// TestParseSearchResultRichRow covers parseSearchResult (the qnKhOb-row parser)
// for fields the fixture path rarely populates: URL, developer id, currency,
// price, summary, score.
func TestParseSearchResultRichRow(t *testing.T) {
	row := make([]interface{}, 13)
	row[2] = "Cool App"
	row[12] = []interface{}{"com.cool.app"}
	row[9] = []interface{}{nil, nil, nil, nil, []interface{}{nil, nil, "/store/apps/details?id=com.cool.app"}}
	row[1] = []interface{}{nil, []interface{}{[]interface{}{nil, nil, nil, []interface{}{nil, nil, "https://icon.png"}}}}
	row[4] = []interface{}{[]interface{}{[]interface{}{
		"Cool Dev",
		[]interface{}{nil, nil, nil, nil, []interface{}{nil, nil, "https://play.google.com/store/apps/dev?id=DEV123"}},
	}}, []interface{}{nil, []interface{}{nil, []interface{}{nil, "Summary here"}}}}
	// Price/currency at [7][0][3][2][1][0][0] and [...][1].
	priceTuple := []interface{}{float64(2990000), "USD"}
	row[7] = []interface{}{[]interface{}{nil, nil, nil, []interface{}{nil, nil, []interface{}{nil, []interface{}{priceTuple}}}}}
	row[6] = []interface{}{[]interface{}{nil, nil, []interface{}{nil, []interface{}{"4.5", float64(4.5)}}}}

	got := parseSearchResult(row)
	if got.AppID != "com.cool.app" {
		t.Errorf("AppID = %q", got.AppID)
	}
	if got.Title != "Cool App" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.URL != BaseURL+"/store/apps/details?id=com.cool.app" {
		t.Errorf("URL = %q", got.URL)
	}
	if got.DeveloperID != "DEV123" {
		t.Errorf("DeveloperID = %q, want DEV123", got.DeveloperID)
	}
	if got.Currency != "USD" || got.Price != 2.99 || got.Free {
		t.Errorf("price fields wrong: currency=%q price=%v free=%v", got.Currency, got.Price, got.Free)
	}
	if got.ScoreText != "4.5" || got.Score != 4.5 {
		t.Errorf("score fields wrong: %q %v", got.ScoreText, got.Score)
	}
}

func TestParseSearchResultNotArray(t *testing.T) {
	if got := parseSearchResult("not an array"); got.AppID != "" {
		t.Errorf("parseSearchResult(non-array) = %+v, want zero", got)
	}
}

func TestParseSearchResultFreeWhenNoPrice(t *testing.T) {
	row := make([]interface{}, 13)
	row[12] = []interface{}{"com.free.app"}
	got := parseSearchResult(row)
	if !got.Free {
		t.Error("Free = false, want true when no price node present")
	}
}

// TestExtractSearchResultsNoSection returns an empty result set when no ds block
// is present, covering the early-return branch.
func TestExtractSearchResultsNoSection(t *testing.T) {
	results, token, err := extractSearchResults(map[string]interface{}{"ds:99": "irrelevant"})
	if err != nil {
		t.Fatalf("extractSearchResults: %v", err)
	}
	if len(results) != 0 || token != "" {
		t.Errorf("got %d results / token %q, want empty", len(results), token)
	}
}

// TestParseListPageNoDs4 returns nil when the page lacks the ds:4 block.
func TestParseListPageNoDs4(t *testing.T) {
	page := htmlWithDataBlocks(map[string]string{"ds:5": `[]`})
	results, err := parseListPage(page, ListOptions{Collection: CollectionTopFree, Num: 10})
	if err != nil {
		t.Fatalf("parseListPage: %v", err)
	}
	if results != nil {
		t.Errorf("got %v, want nil when ds:4 absent", results)
	}
}

// TestFindSimilarClusterNoCluster returns an empty URL (no error) when no
// "Similar" cluster is present.
func TestFindSimilarClusterNoCluster(t *testing.T) {
	page := htmlWithDataBlocks(map[string]string{"ds:7": `[null,[null,[]]]`})
	url, err := findSimilarCluster(page)
	if err != nil {
		t.Fatalf("findSimilarCluster: %v", err)
	}
	if url != "" {
		t.Errorf("url = %q, want empty", url)
	}
}

// TestParseDataBlocksSkipsMalformed keeps valid blocks and silently drops the
// one whose data is not valid JSON.
func TestParseDataBlocksSkipsMalformed(t *testing.T) {
	body := []byte(
		`<script>AF_initDataCallback({key: 'ds:1', hash: '1', data:[1,2,3], sideChannel: {}});</script>` +
			`<script>AF_initDataCallback({key: 'ds:2', hash: '1', data:{not valid json, sideChannel: {}});</script>`,
	)
	blocks := parseDataBlocks(body)
	if _, ok := blocks["ds:1"]; !ok {
		t.Error("ds:1 (valid) was dropped")
	}
	if _, ok := blocks["ds:2"]; ok {
		t.Error("ds:2 (malformed) was kept")
	}
}

// TestDecodeBatchEnvelopeErrors covers the empty-body and no-frame branches.
func TestDecodeBatchEnvelopeErrors(t *testing.T) {
	if _, err := decodeBatchEnvelope(nil); err == nil {
		t.Error("decodeBatchEnvelope(nil) should error on empty body")
	}
	// A well-formed prefix-only body with no wrb.fr frame returns (nil, nil).
	data, err := decodeBatchEnvelope([]byte(")]}'\n[[\"di\",42]]"))
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if data != nil {
		t.Errorf("data = %v, want nil for frameless response", data)
	}
}
