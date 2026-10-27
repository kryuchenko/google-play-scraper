package googleplayscraper

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSearchValidation(t *testing.T) {
	c := NewClient()

	_, err := c.Search(context.Background(), SearchOptions{})
	if err == nil {
		t.Error("expected error for empty term")
	}
}

func TestGetPriceValue(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"free", 1},
		{"FREE", 1},
		{"paid", 2},
		{"PAID", 2},
		{"all", 0},
		{"", 0},
	}

	for _, tt := range tests {
		got := getPriceValue(tt.input)
		if got != tt.want {
			t.Errorf("getPriceValue(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
func TestSearchIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	results, err := c.Search(context.Background(), SearchOptions{
		Term:    "maps",
		Lang:    "en",
		Country: "us",
		Num:     5,
	})

	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one result")
	}

	// Validate first result
	if len(results) > 0 {
		r := results[0]
		if r.AppID == "" {
			t.Error("AppID is empty")
		}
		if r.Title == "" {
			t.Error("Title is empty")
		}
		t.Logf("First result: %s (%s)", r.Title, r.AppID)
	}

	t.Logf("Got %d results", len(results))
}

func TestSearchFreeApps(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	results, err := c.Search(context.Background(), SearchOptions{
		Term:  "game",
		Num:   5,
		Price: "free",
	})

	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	for _, r := range results {
		assertValidApp(t, r)
		if !r.Free {
			t.Errorf("Expected free app, got paid: %s", r.Title)
		}
	}
}

// TestSearchPagination tests fetching multiple pages
func TestSearchPagination(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	results, err := c.Search(context.Background(), SearchOptions{
		Term: "social",
		Num:  60, // More than one page
	})

	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// One page is about 20; more than that proves the RPC path works. Google
	// returns at most about 50 for a query, so 60 is clamped.
	t.Logf("Got %d results (requested 60)", len(results))
	if len(results) <= 20 {
		t.Errorf("got %d results; more than one page should come back", len(results))
	}

	// Validate all results
	for _, r := range results {
		assertValidApp(t, r)
	}
}

// TestSearchLocalization tests different languages
func TestSearchLocalization(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()

	// Test Spanish
	results, err := c.Search(context.Background(), SearchOptions{
		Term:    "juegos",
		Lang:    "es",
		Country: "es",
		Num:     5,
	})

	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	t.Logf("Spanish search got %d results", len(results))
	for _, r := range results {
		assertValidApp(t, r)
	}
}

// TestSearchEmptyResults tests search with nonsense query
func TestSearchEmptyResults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	results, err := c.Search(context.Background(), SearchOptions{
		Term: "xyznonexistentappquery12345",
		Num:  5,
	})

	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	t.Logf("Nonsense query returned %d results", len(results))
}

// TestSearchSpecificApp tests searching for a specific app
func TestSearchSpecificApp(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	results, err := c.Search(context.Background(), SearchOptions{
		Term: "Netflix",
		Num:  10,
	})

	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// Netflix should be in results
	found := false
	for _, r := range results {
		if r.AppID == "com.netflix.mediaclient" {
			found = true
			break
		}
	}

	if !found {
		t.Log("Note: Netflix not found in top 10 results")
	}

	t.Logf("Search for 'Netflix' returned %d results", len(results))
}

// TestSearchGames tests searching in games category
func TestSearchGames(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	results, err := c.Search(context.Background(), SearchOptions{
		Term: "puzzle games",
		Num:  10,
	})

	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	for _, r := range results {
		assertValidApp(t, r)
	}

	t.Logf("Got %d puzzle game results", len(results))
}

// TestSearchFullDetail tests search with full app details
func TestSearchFullDetail(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	results, err := c.Search(context.Background(), SearchOptions{
		Term:       "calculator",
		Num:        3,
		FullDetail: true,
	})

	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("Expected at least one result")
	}

	// With FullDetail, results should have more fields populated
	for _, r := range results {
		assertValidApp(t, r)
	}

	t.Logf("Got %d results with full details", len(results))
}

// TestSearchFullDetailConcurrentPreservesOrder drives the FullDetail enrichment
// path through the shared worker pool at concurrency>1 and asserts the enriched
// output stays in the same AppID order as the plain search. Run under -race it
// proves the rewritten enrichSearchResults has no data race (each worker writes
// its own slot) and that completion-order nondeterminism never reorders results.
func TestSearchFullDetailConcurrentPreservesOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	opts := SearchOptions{Term: "weather", Num: 8, Country: "us", Lang: "en"}

	plain, err := NewClient().Search(context.Background(), opts)
	if err != nil {
		t.Fatalf("plain search failed: %v", err)
	}
	if len(plain) == 0 {
		t.Fatal("expected at least one result")
	}

	detailOpts := opts
	detailOpts.FullDetail = true
	enriched, err := NewClient(WithConcurrency(4)).Search(context.Background(), detailOpts)
	if err != nil {
		t.Fatalf("full-detail search failed: %v", err)
	}

	// The two searches are independent live requests and Google does not
	// return a stable list: ranking churn between them is normal, and a test
	// that demands identical slices fails for reasons that have nothing to do
	// with this library. It failed exactly that way once during a full-suite
	// run and passed five times in isolation.
	//
	// What the concurrent enrichment must actually preserve is *relative
	// order*: whichever apps both searches happened to return must appear in
	// the same sequence. That is the property under test, and it does not
	// require Google to be deterministic.
	plainPos := make(map[string]int, len(plain))
	for i, a := range plain {
		plainPos[a.AppID] = i
	}

	last := -1
	var common int
	for i, a := range enriched {
		assertValidApp(t, a)
		pos, ok := plainPos[a.AppID]
		if !ok {
			continue // Google returned it to one search and not the other
		}
		common++
		if pos < last {
			t.Errorf("order diverged: %q is at %d in the plain search but follows an app at %d (enriched index %d)",
				a.AppID, pos, last, i)
		}
		last = pos
	}

	// If the two searches shared almost nothing, the ordering check above had
	// nothing to check. That is not a defect in this library -- it is Google
	// returning a different list to the second query -- so skip rather than
	// fail. Under a full-suite run, where the store is being queried hard, the
	// overlap does sometimes drop that far.
	if common < len(plain)/2 {
		t.Skipf("only %d of %d results were common to both searches; nothing to check",
			common, len(plain))
	}
}

// TestDecodeBatchEnvelope tests the shared batchexecute envelope decoder.
func TestDecodeBatchEnvelope(t *testing.T) {
	// Empty response
	if _, err := decodeBatchEnvelope([]byte{}); err == nil {
		t.Error("expected error for empty response")
	}

	// Invalid JSON
	if _, err := decodeBatchEnvelope([]byte("\n{invalid")); err == nil {
		t.Error("expected error for invalid JSON")
	}

	// Null payload signals "no more data": returns nil data, no error.
	data, err := decodeBatchEnvelope([]byte(")]}'\n[[\"wrb.fr\",\"qnKhOb\",null,null,null,[3],\"generic\"]]"))
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if data != nil {
		t.Errorf("expected nil data for null payload, got %v", data)
	}

	// Valid payload: data string is parsed into a slice.
	data, err = decodeBatchEnvelope([]byte(")]}'\n[[\"wrb.fr\",\"qnKhOb\",\"[1,2,3]\",null,null,[3],\"generic\"]]"))
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(data) != 3 {
		t.Errorf("expected 3 elements, got %d", len(data))
	}
}

// TestSearchLargePagination tests search with many results (triggers pagination)
func TestSearchLargePagination(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	results, err := c.Search(context.Background(), SearchOptions{
		Term: "game",
		Num:  100, // Request more than first page
	})

	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	t.Logf("Got %d results for large search", len(results))
	for _, r := range results {
		assertValidApp(t, r)
	}
}

func TestEnrichSearchResults_Error(t *testing.T) {
	// Mock a client with a transport that fails for specific URLs
	// Since we can't easily mock the internal client's transport here without exposing it,
	// we'll rely on the existing integration test structure but pointing to invalid URLs effectively.
	// Actually, enrichSearchResults makes calls to c.App(). We can test that if c.App fails, the original result is kept.

	// Create a client that will fail requests
	c := NewClient()

	results := []SearchResult{
		{AppID: "invalid.id.1"},
		{AppID: "invalid.id.2"},
	}

	// This is an integration test essentially, as it will make real network calls that return 404
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	enriched, err := c.enrichSearchResults(context.Background(), results, "en", "us")
	if err != nil {
		t.Fatalf("enrichSearchResults failed: %v", err)
	}

	if len(enriched) != 2 {
		t.Errorf("expected 2 results, got %d", len(enriched))
	}

	// Should still be the original results if enrichment failed (404s)
	if enriched[0].AppID != "invalid.id.1" {
		t.Errorf("expected appID invalid.id.1, got %s", enriched[0].AppID)
	}
}

func TestHasAppIdPattern(t *testing.T) {
	tests := []struct {
		input []any
		want  bool
	}{
		{[]any{[]any{"com.example"}}, true},
		{[]any{[]any{"net.example"}}, true},
		{[]any{[]any{"io.example"}}, true},
		{[]any{[]any{"not.package"}}, false},
		{[]any{[]any{123}}, false},
		{[]any{}, false},
	}

	for _, tt := range tests {
		if got := hasAppIDPattern(tt.input); got != tt.want {
			t.Errorf("hasAppIDPattern(%v) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// MockTransport allows mocking HTTP responses
type MockTransport struct {
	RoundTripFunc func(req *http.Request) (*http.Response, error)
}

func (m *MockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.RoundTripFunc(req)
}

// searchRPCResults decodes a captured lGYRle response the way searchRPC does.
func searchRPCResults(t *testing.T, fixture string) []SearchResult {
	t.Helper()
	data, err := decodeBatchEnvelope(readFixture(t, fixture))
	if err != nil {
		t.Fatalf("decode %s: %v", fixture, err)
	}
	results, _, err := extractSearchResults(map[string]any{"ds:4": data})
	if err != nil {
		t.Fatalf("parse %s: %v", fixture, err)
	}
	return results
}

// Issue #16: one lGYRle call returns the whole list, up to about 50. The two
// fixtures are the two section layouts seen so far: the grid is a [22] card
// after the featured app in the US, and a [21] card after a small cluster in
// Germany, where the featured card is the second section.
func TestSearchRPCFixtures(t *testing.T) {
	for _, fixture := range []string{"search_rpc_us.bin", "search_rpc_de.bin"} {
		results := searchRPCResults(t, fixture)
		if len(results) < 40 {
			t.Errorf("%s: %d results, want the 40-odd the RPC returns", fixture, len(results))
		}
		if results[0].AppID != "com.mojang.minecraftpe" {
			t.Errorf("%s: first result %s, want the featured Minecraft", fixture, results[0].AppID)
		}
		seen := map[string]bool{}
		for _, r := range results {
			if seen[r.AppID] {
				t.Errorf("%s: %s appears twice", fixture, r.AppID)
			}
			seen[r.AppID] = true
		}
	}
}

func TestSearchAboveOnePageUsesTheRPC(t *testing.T) {
	var rpcBodies []string
	c := newMockClient(t,
		routePath("/store/search", readFixture(t, "search_page.html")),
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch || req.URL.Query().Get("rpcids") != "lGYRle" {
				return mockResponse{}, false
			}
			body, _ := io.ReadAll(req.Body)
			decoded, _ := url.QueryUnescape(strings.TrimPrefix(string(body), "f.req="))
			rpcBodies = append(rpcBodies, decoded)
			return mockResponse{Body: readFixture(t, "search_rpc_us.bin")}, true
		},
	)

	results, err := c.Search(context.Background(), SearchOptions{Term: "minecraft", Num: 45})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 45 {
		t.Errorf("%d results, want 45", len(results))
	}
	if len(rpcBodies) != 1 {
		t.Fatalf("%d lGYRle calls, want 1", len(rpcBodies))
	}
	if !strings.Contains(rpcBodies[0], `\"minecraft\"`) || !strings.Contains(rpcBodies[0], "[[8,[45,50]]") {
		t.Errorf("request does not carry the query and size: %.200s", rpcBodies[0])
	}

	// A page's worth needs no RPC.
	rpcBodies = nil
	if _, err := c.Search(context.Background(), SearchOptions{Term: "minecraft", Num: 20}); err != nil {
		t.Fatal(err)
	}
	if len(rpcBodies) != 0 {
		t.Error("Num 20 should be served by the search page alone")
	}

	// Only the page applies a price filter, so a filtered search stays on it.
	if _, err := c.Search(context.Background(), SearchOptions{Term: "minecraft", Num: 45, Price: "paid"}); err != nil {
		t.Fatal(err)
	}
	if len(rpcBodies) != 0 {
		t.Error("a price-filtered search should not call the RPC")
	}

	// Asking for more than Google returns is clamped, not rejected.
	if _, err := c.Search(context.Background(), SearchOptions{Term: "minecraft", Num: 500}); err != nil {
		t.Fatal(err)
	}
	if n := len(rpcBodies); n != 1 || !strings.Contains(rpcBodies[0], "[[8,[50,50]]") {
		t.Errorf("Num 500 should ask for 50 in one call; calls %d", n)
	}
}
