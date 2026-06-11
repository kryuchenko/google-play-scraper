package googleplayscraper

import (
	"context"
	"io"
	"net/http"
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

func TestParseSearchResult(t *testing.T) {
	// Mock search result data structure
	data := []interface{}{
		nil,        // [0]
		nil,        // [1]
		"Test App", // [2] Title
		nil,        // [3]
		[]interface{}{[]interface{}{[]interface{}{ // [4] Developer info
			"Test Developer", // [0][0][0]
		}}},
		nil,                           // [5]
		nil,                           // [6]
		nil,                           // [7]
		nil,                           // [8]
		nil,                           // [9]
		nil,                           // [10]
		nil,                           // [11]
		[]interface{}{"com.test.app"}, // [12] AppID
	}

	result := parseSearchResult(data)

	if result.Title != "Test App" {
		t.Errorf("Title: got %q, want %q", result.Title, "Test App")
	}
	if result.AppID != "com.test.app" {
		t.Errorf("AppID: got %q, want %q", result.AppID, "com.test.app")
	}
}

// TestSearchIntegration is a real integration test
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

	// Should have fetched multiple pages
	t.Logf("Got %d results (requested 60)", len(results))

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

	if len(enriched) != len(plain) {
		t.Fatalf("enriched length %d != plain length %d", len(enriched), len(plain))
	}
	for i := range plain {
		if enriched[i].AppID != plain[i].AppID {
			t.Errorf("order diverged at %d: enriched %q, plain %q", i, enriched[i].AppID, plain[i].AppID)
		}
		assertValidApp(t, enriched[i])
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
		input []interface{}
		want  bool
	}{
		{[]interface{}{[]interface{}{"com.example"}}, true},
		{[]interface{}{[]interface{}{"net.example"}}, true},
		{[]interface{}{[]interface{}{"io.example"}}, true},
		{[]interface{}{[]interface{}{"not.package"}}, false},
		{[]interface{}{[]interface{}{123}}, false},
		{[]interface{}{}, false},
	}

	for _, tt := range tests {
		if got := hasAppIdPattern(tt.input); got != tt.want {
			t.Errorf("hasAppIdPattern(%v) = %v, want %v", tt.input, got, tt.want)
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

func TestFetchMoreApps(t *testing.T) {
	// Mock response for batchexecute
	mockResponseBody := `)]}'
[["wrb.fr","[[null,[[10,[10,50]],true,null,[96,27,4,8,57,30,110,79,11,16,49,1,3,9,12,104,55,56,51,10,34,77]],[null,\"token\"]]",null,"generic"]]`

	// Create client with mock transport
	mockTransport := &MockTransport{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(mockResponseBody)),
				Header:     make(http.Header),
			}, nil
		},
	}

	// Create a new client and inject the transport
	// Note: Client struct in request.go uses *http.Client.
	// We need to ensure we can modify it. NewClient() creates a standard http.Client.
	// Since we are in the same package, and if Client.client is unexported, we can access it.
	c := NewClient()

	// Overwrite the transport of the internal http client
	if c.httpClient == nil {
		c.httpClient = &http.Client{}
	}
	c.httpClient.Transport = mockTransport

	results, token, err := c.fetchMoreApps(context.Background(), "token", "en", "us")

	// We expect NO error: a null payload is gracefully handled as "no more data".
	if err != nil {
		t.Fatalf("fetchMoreApps failed: %v", err)
	}

	// In this mock, we don't have real app data, so results should be empty
	if len(results) != 0 {
		t.Errorf("expected 0 results from mock garbage, got %d", len(results))
	}

	// Token might be empty or not depending on parser internals on garbage
	_ = token
}
