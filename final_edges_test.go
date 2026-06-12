package googleplayscraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestExtractMonetizationDiscount drives the discount branches of
// extractMonetization (OriginalPrice and DiscountEndDate) that the fixtures do
// not exercise, on a hand-built app-data node.
func TestExtractMonetizationDiscount(t *testing.T) {
	appData := make([]interface{}, 58)
	// AdSupported marker at [48].
	appData[48] = []interface{}{"Contains ads"}
	// IAPRange at [19][0].
	appData[19] = []interface{}{"$0.99 - $99.99 per item"}
	// Price block carrying OriginalPrice at [57][0][0][0][0][1][1][0] and
	// DiscountEndDate at [57][0][0][0][0][14][0][0].
	priceInner := make([]interface{}, 15)
	priceInner[1] = []interface{}{nil, []interface{}{float64(4990000)}} // [1][1][0] original price micros
	priceInner[14] = []interface{}{[]interface{}{float64(1700000000)}}  // [14][0][0] discount end unix
	// Nest priceInner at [57][0][0][0][0].
	appData[57] = []interface{}{[]interface{}{[]interface{}{[]interface{}{priceInner}}}}

	app := &App{}
	extractMonetization(app, appData)

	if !app.AdSupported {
		t.Error("AdSupported = false, want true")
	}
	if !app.OffersIAP || app.IAPRange == "" {
		t.Errorf("IAP fields wrong: offers=%v range=%q", app.OffersIAP, app.IAPRange)
	}
	if app.OriginalPrice != 4.99 {
		t.Errorf("OriginalPrice = %v, want 4.99", app.OriginalPrice)
	}
	if app.DiscountEndDate != 1700000000 {
		t.Errorf("DiscountEndDate = %d, want 1700000000", app.DiscountEndDate)
	}
}

func TestExtractMonetizationNoExtras(t *testing.T) {
	// An app with none of the optional monetization nodes leaves the fields zero.
	app := &App{}
	extractMonetization(app, []interface{}{})
	if app.AdSupported || app.OffersIAP || app.OriginalPrice != 0 || app.DiscountEndDate != 0 {
		t.Errorf("expected all monetization fields zero, got %+v", app)
	}
}

// TestAppDataNodeMissing covers appDataNode's negative branches: no ds:5 block,
// and a ds:5 block whose [1][2] node is absent.
func TestAppDataNodeMissing(t *testing.T) {
	if _, ok := appDataNode(htmlWithDataBlocks(map[string]string{"ds:4": `[]`})); ok {
		t.Error("appDataNode returned ok=true with no ds:5 block")
	}
	if _, ok := appDataNode(htmlWithDataBlocks(map[string]string{"ds:5": `[1,2]`})); ok {
		t.Error("appDataNode returned ok=true when [1][2] is absent")
	}
}

// TestPostBodyReadError exercises the io.ReadAll failure branch of post.
func TestPostBodyReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "2048")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("partial"))
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	}))
	defer server.Close()

	if _, err := NewClient().post(context.Background(), server.URL, "text/plain", "x"); err == nil {
		t.Fatal("expected a body read error from post, got nil")
	}
}

// TestSimilarClusterRequestError makes the second fetch (the cluster page) fail,
// covering Similar's "cluster request failed" branch.
func TestSimilarClusterRequestError(t *testing.T) {
	c := newMockClient(t,
		routePath(pathDetails, readFixture(t, "app_page.html")),
		routePathStatus(pathCluster, http.StatusInternalServerError),
	)
	if _, err := c.Similar(context.Background(), SimilarOptions{AppID: "com.x"}); err == nil {
		t.Fatal("expected cluster request error")
	}
}

func TestFindAppsInDataNoMatch(t *testing.T) {
	if got := findAppsInData([]interface{}{"no", "apps", "here"}); got != nil {
		t.Errorf("findAppsInData(no apps) = %v, want nil", got)
	}
	if got := findAppsInData("not an array"); got != nil {
		t.Errorf("findAppsInData(non-array) = %v, want nil", got)
	}
}

// TestCategoryAppsExpandSuggest exercises the ExpandSuggest branch of the search
// phase: each term is run through Suggest, whose results are enqueued.
func TestCategoryAppsExpandSuggest(t *testing.T) {
	emptyHTML := htmlWithDataBlocks(map[string]string{"ds:4": `[]`})

	var suggestHit bool
	routes := []routeFunc{
		// Suggest RPC.
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path == pathBatch && req.URL.Query().Get("rpcids") == "IJ4APc" {
				suggestHit = true
				return mockResponse{Body: readFixture(t, "suggest_batch.bin")}, true
			}
			return mockResponse{}, false
		},
		routeQuery(pathBatch, map[string]string{"rpcids": "qnKhOb"},
			[]byte(")]}'\n\n[[\"wrb.fr\",\"qnKhOb\",null,null,null,null,\"generic\"]]")),
		routeQuery(pathBatch, map[string]string{"rpcids": "vyAe2"},
			[]byte(")]}'\n\n[[\"wrb.fr\",\"vyAe2\",null,null,null,null,\"generic\"]]")),
		routePath(pathTop, emptyHTML),
		func(req *http.Request) (mockResponse, bool) {
			if len(req.URL.Path) >= len(pathCategory) && req.URL.Path[:len(pathCategory)] == pathCategory {
				return mockResponse{Body: emptyHTML}, true
			}
			return mockResponse{}, false
		},
		routePath("/store/search", readFixture(t, "search_page.html")),
	}
	c := newMockClient(t, routes...)

	res, err := c.CategoryApps(context.Background(), CoverageOptions{
		Category:      CategoryGameAction,
		SearchTerms:   []string{"shooter"},
		ExpandSuggest: true,
		// A tight cap keeps the suggest-expanded queue from running long.
		MaxApps: 5,
	})
	if err != nil {
		t.Fatalf("CategoryApps: %v", err)
	}
	if !suggestHit {
		t.Error("Suggest was never called despite ExpandSuggest=true")
	}
	if res.RequestsMade == 0 {
		t.Error("RequestsMade = 0")
	}
}
