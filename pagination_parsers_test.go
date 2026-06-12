package googleplayscraper

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// clusterMorePayload renders the qnKhOb feed response that fetchQnKhOb /
// parseQnKhObResponse consume. Apps live at data[0][21][0]; each app is a
// parseSearchResultNew row with AppID at [0][0] and Title at [3]. The token at
// data[0][3][0] is set for shape fidelity but is intentionally ignored by
// paginateQnKhOb (it is the dead echo token); continuation is driven by the
// per-topic feed tokens extracted from the HTML page instead.
func clusterMorePayload(t *testing.T, appIDs []string, nextToken string) []byte {
	t.Helper()
	apps := make([]interface{}, 0, len(appIDs))
	for _, id := range appIDs {
		apps = append(apps, []interface{}{[]interface{}{id}, nil, nil, "Title " + id})
	}
	lvl := make([]interface{}, 22)
	if nextToken != "" {
		lvl[3] = []interface{}{nextToken} // [0][3][0] echo token (ignored)
	}
	lvl[21] = []interface{}{apps} // [0][21][0] apps
	data := []interface{}{lvl}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal cluster-more: %v", err)
	}
	return batchEnvelope("qnKhOb", string(raw))
}

// TestClusterPaginates drives Cluster with FollowFeed: the initial HTML page
// carries the apps grid plus two recommendation-topic cluster URLs. Each topic
// is fetched once via qnKhOb and its apps merged in, de-duplicated against the
// grid and across topics.
func TestClusterPaginates(t *testing.T) {
	initialPage := clusterHTMLPage(t, []string{"com.a", "com.b"}, "GAME", "topicOne", "topicTwo")

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
				// First topic: two fresh apps plus one repeat of the grid.
				return mockResponse{Body: clusterMorePayload(t, []string{"com.a", "com.c", "com.d"}, "")}, true
			}
			// Second topic: one fresh app plus a cross-topic repeat.
			return mockResponse{Body: clusterMorePayload(t, []string{"com.d", "com.e"}, "")}, true
		},
	)

	results, err := c.Cluster(context.Background(), ClusterOptions{Path: "/store/apps/collection/cluster?x=1", FollowFeed: true})
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	// Grid {a,b} + topic1 {a,c,d} + topic2 {d,e} = {a,b,c,d,e} after dedup.
	if len(results) != 5 {
		t.Fatalf("got %d apps across topics, want 5 unique", len(results))
	}
	if batchCalls != 2 {
		t.Errorf("batch calls = %d, want 2 (one per recommendation topic)", batchCalls)
	}
}

// clusterHTMLPage builds a cluster/category HTML page with apps at [0,1,0,21,0]
// and, for each topic id, a recommendation cluster URL whose gsr blob carries a
// recs_topic token (the shape extractFeedTokens harvests). The echo token at
// [0,1,0,3,0] is included for realism but is not used for pagination.
func clusterHTMLPage(t *testing.T, appIDs []string, category string, topicIDs ...string) []byte {
	t.Helper()
	apps := make([]interface{}, 0, len(appIDs))
	for _, id := range appIDs {
		// parseSearchResultNew reads AppID at [0][0] and Title at [3].
		apps = append(apps, []interface{}{[]interface{}{id}, nil, nil, "Title " + id})
	}
	lvl := make([]interface{}, 22)
	lvl[3] = []interface{}{"echo-token"} // [0,1,0,3,0] echo token (ignored)
	lvl[21] = []interface{}{apps}        // [0,1,0,21,0] apps
	node := []interface{}{
		[]interface{}{nil, []interface{}{lvl}}, // [0][1][0] = lvl
	}
	raw, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("marshal cluster page: %v", err)
	}

	var sb strings.Builder
	sb.Write(htmlWithDataBlocks(map[string]string{"ds:4": string(raw)}))
	for _, topic := range topicIDs {
		sb.WriteString(`<a href="/store/apps/collection/cluster?gsr=`)
		sb.WriteString(makeGsrBlob(category, topic))
		sb.WriteString(`"></a>`)
	}
	return []byte(sb.String())
}

// makeGsrBlob builds a base64url field-9 (tag 0x4a) protobuf blob equivalent to
// the recommendation cluster gsr value Google embeds, so extractFeedTokens /
// gsrToFeedToken accept it. The inner query is field 2 (tag 0x12) carrying a
// "recs_topic_<topic>" string at field 4 (tag 0x22); the exact sub-fields do
// not matter to the parser, only that the inner bytes contain "recs_topic".
func makeGsrBlob(category, topic string) string {
	name := "recs_topic_" + topic
	// field 4 (tag 0x22, len-delimited) = the recs_topic name.
	field4 := append([]byte{0x22, byte(len(name))}, []byte(name)...)
	// field 3 (tag 0x1a) = category, just to mirror the real layout.
	field3 := append([]byte{0x1a, byte(len(category))}, []byte(category)...)
	inner := append(field3, field4...)
	// field 2 (tag 0x12) wraps the query.
	query := append([]byte{0x12, byte(len(inner))}, inner...)
	// field 9 (tag 0x4a) wraps the whole gsr payload.
	blob := append([]byte{0x4a, byte(len(query))}, query...)
	return base64.RawURLEncoding.EncodeToString(blob)
}

// TestGsrToFeedToken verifies the gsr→feed-token rewrap: a field-9 recs blob is
// re-wrapped as a field-12 token whose inner bytes still carry the recs_topic
// query, and non-recs / malformed blobs are rejected.
func TestGsrToFeedToken(t *testing.T) {
	gsr := makeGsrBlob("GAME_ACTION", "abc123")
	tok, ok := gsrToFeedToken(gsr)
	if !ok {
		t.Fatal("gsrToFeedToken rejected a valid recs blob")
	}
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		t.Fatalf("token is not base64url: %v", err)
	}
	if raw[0] != 0x62 {
		t.Errorf("token wrapper tag = %#x, want 0x62 (field 12)", raw[0])
	}
	if !strings.Contains(string(raw), "recs_topic_abc123") {
		t.Error("token lost the recs_topic query")
	}

	// A promotion-style cluster (field 9 wrapping a non-recs query) is rejected.
	promo := base64.RawURLEncoding.EncodeToString(
		append([]byte{0x4a, 0x05, 0x12, 0x03}, []byte("xyz")...),
	)
	if _, ok := gsrToFeedToken(promo); ok {
		t.Error("gsrToFeedToken accepted a non-recs blob")
	}

	// Garbage / non-base64 input is rejected without panicking.
	if _, ok := gsrToFeedToken("!!!not-base64!!!"); ok {
		t.Error("gsrToFeedToken accepted non-base64 input")
	}
}

// TestExtractFeedTokensDedup confirms the page scanner finds each recs cluster
// URL once, tolerates the JSON-escaped '=' separator, and de-duplicates repeats.
func TestExtractFeedTokensDedup(t *testing.T) {
	a := makeGsrBlob("GAME_ACTION", "alpha")
	b := makeGsrBlob("GAME_ACTION", "beta")
	esc := string([]byte{'\\', 'u', '0', '0', '3', 'd'}) // JSON-escaped '='
	html := `x cluster?gsr` + esc + a +                  // escaped separator form
		` y cluster?gsr=` + b + // plain separator form
		` z cluster?gsr=` + a // duplicate of the first

	tokens := extractFeedTokens([]byte(html))
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens, want 2 (deduped)", len(tokens))
	}
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
