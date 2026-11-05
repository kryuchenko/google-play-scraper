package googleplayscraper

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"testing"
)

// The category and tag nodes as the store served them for Candy Crush Saga
// and Google Maps (trimmed to the first groups).
const (
	candyGenre = `[[["Casual",[null,null,null,null,[null,null,"/store/apps/category/GAME_CASUAL"]],"GAME_CASUAL"]]]`
	candyTags  = `[[[["Puzzle",[null,null,null,null,[null,null,"/store/apps/category/GAME_PUZZLE"]],"GAME_PUZZLE","/m/04q1x3q"],["Match 3",[null,null,null,null,[null,null,"/store/search?q=Match+3+games&c=apps"]],null,"/genome/00022"]]],[[["Casual",[null,null,null,null,[null,null,"/store/apps/category/GAME_CASUAL"]],"GAME_CASUAL","/m/0b1vjn"]]],null,[[[["Single player",[null,null,null,null,[null,null,"/store/search?q=Single-player+games&c=apps"]],null,"/m/02hjn4"]]]]]`
	mapsGenre  = `[[["Travel & Local",[null,null,null,null,[null,null,"/store/apps/category/TRAVEL_AND_LOCAL"]],"TRAVEL_AND_LOCAL"]]]`
)

func decodeJSON(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCategoryNames(t *testing.T) {
	got := categoryNames(decodeJSON(t, candyGenre), decodeJSON(t, candyTags))
	want := []string{"Casual", "Puzzle", "Match 3", "Single player"}
	if !slices.Equal(got, want) {
		t.Errorf("candy crush: %q, want %q", got, want)
	}
	got = categoryNames(decodeJSON(t, mapsGenre), decodeJSON(t, `[]`))
	if !slices.Equal(got, []string{"Travel & Local"}) {
		t.Errorf("maps: %q", got)
	}
	if got := categoryNames(nil, nil); got != nil {
		t.Errorf("missing nodes: %q", got)
	}
	// A string next to an array without a store link is not an entry.
	if got := categoryNames(decodeJSON(t, `[["Not a tag",[1,2,3]],["x",[null,null,null,null,[null,null,"https://elsewhere"]]]]`)); got != nil {
		t.Errorf("non-entries collected: %q", got)
	}
}

func TestPriceKnownMerge(t *testing.T) {
	grid := SearchResult{AppID: "a", Free: true}                                  // no price node
	full := SearchResult{AppID: "a", Price: 14.99, Free: false, PriceKnown: true} // FullDetail
	free := SearchResult{AppID: "a", Price: 0, Free: true, PriceKnown: true}

	if m := mergeResult(grid, full); !m.PriceKnown || m.Free || m.Price != 14.99 {
		t.Errorf("unknown+paid: %+v", m)
	}
	if m := mergeResult(grid, free); !m.PriceKnown || !m.Free {
		t.Errorf("unknown+free: %+v", m)
	}
	if m := mergeResult(full, grid); !m.PriceKnown || m.Free || m.Price != 14.99 {
		t.Errorf("a known price was overwritten by an unknown one: %+v", m)
	}
	if r := searchResultFromApp(&App{AppID: "a", Price: 2.99}); !r.PriceKnown {
		t.Error("full detail result not marked PriceKnown")
	}
}

func TestNoDataSafety(t *testing.T) {
	for name, body := range map[string]string{
		"block does not parse": `<script>AF_initDataCallback({key: 'ds:3', hash: '1', data:[[1,}, sideChannel: {}});</script>`,
		"no section":           `<script>AF_initDataCallback({key: 'ds:3', hash: '1', data:[null,[null,null,[null,[]]]], sideChannel: {}});</script>`,
	} {
		ds, err := parseDataSafetyPage([]byte(body))
		if ds != nil || !errors.Is(err, ErrNoDataSafety) {
			t.Errorf("%s: got %+v, %v; want ErrNoDataSafety", name, ds, err)
		}
	}
}

func TestNoReviewsPage(t *testing.T) {
	_, err := parseReviewsPayload("", "com.example")
	if !errors.Is(err, ErrNoReviewsPage) {
		t.Fatalf("got %v", err)
	}
	// Callers that matched the historical text keep working.
	if got := err.Error(); len(got) < 20 || got[:20] != "data is not a string" {
		t.Errorf("message lost its old prefix: %q", got)
	}
}

// notFoundPage is what /store/apps/datasafety serves, with status 200, for an
// id the store does not list: a page shell with no data blocks at all.
var notFoundPage = []byte(`<!doctype html><html><head><title>Not Found</title></head><body>Not Found</body></html>`)

func TestDataSafetyUnknownID(t *testing.T) {
	c := newMockClient(t,
		routePath("/store/apps/datasafety", notFoundPage),
		routePathStatus("/store/apps/details", 404),
	)
	ds, err := c.DataSafety(t.Context(), DataSafetyOptions{AppID: "com.qa.nonexistent"})
	if ds != nil || !errors.Is(err, ErrNoDataSafety) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %+v, %v; want ErrNoDataSafety wrapping ErrNotFound", ds, err)
	}
	var sb *SoftBlockError
	if errors.As(err, &sb) {
		t.Errorf("an unknown id is reported as a soft block: %v", err)
	}
}

func TestDataSafetyEmptyPageForListedApp(t *testing.T) {
	c := newMockClient(t,
		routePath("/store/apps/datasafety", notFoundPage),
		routePath("/store/apps/details", htmlWithDataBlocks(map[string]string{"ds:5": `[null,[null,null,[]]]`})),
	)
	_, err := c.DataSafety(t.Context(), DataSafetyOptions{AppID: "com.example.listed"})
	if !errors.Is(err, ErrNoDataSafety) || errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v; want ErrNoDataSafety alone", err)
	}
}

func TestDataSafetyPageVerdict(t *testing.T) {
	if v, _ := dataSafetyPage(notFoundPage); v != VerdictMalformed {
		t.Errorf("block-less page: %v, want malformed so an adaptive client does not pause", v)
	}
	// A page with other blocks but no ds:3 keeps requireBlock's verdict.
	if v, r := dataSafetyPage(htmlWithDataBlocks(map[string]string{"ds:1": `[]`})); v != VerdictMalformed || r != "no-ds:3" {
		t.Errorf("ds:3 missing: %v %s", v, r)
	}
}

func TestFirstParagraphAndRowSummary(t *testing.T) {
	desc := `Muse is your personal AI agent &amp; it&#39;s fast.<br><br>PERSONAL <b>PRODUCTIVITY</b>`
	if got := firstParagraph(desc); got != "Muse is your personal AI agent & it's fast." {
		t.Errorf("firstParagraph = %q", got)
	}
	row := decodeJSON(t, `[[["com.example.app",7],null,null,"Title",null,null,null,null,null,null,null,null,null,[null,`+
		strconv.Quote(desc)+`]]]`)
	if r := decodeResultRow(row, clusterListAppPaths); r.Summary != "Muse is your personal AI agent & it's fast." {
		t.Errorf("chart row summary = %q", r.Summary)
	}
	grid := decodeJSON(t, `[["com.example.app",7],null,null,"T",null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,[[null,"GPS &amp; maps"]]]`)
	if r := decodeResultRow(grid, featuredRowPaths); r.Summary != "GPS & maps" {
		t.Errorf("entities not decoded in row summary: %q", r.Summary)
	}
}
