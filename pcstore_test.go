package googleplayscraper

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

const (
	pcFixtureAppID = "com.mytona.cookingdiary.android"
	pcFixtureURL   = "https://play.google.com/pc-store/games/details?id=" + pcFixtureAppID + "&hl=en&gl=us"
	pcPath         = "/pc-store/games/details"
)

func TestParsePCAppPageFixture(t *testing.T) {
	pc, err := parsePCAppPage(readFixture(t, "pc_page_game.html"), pcFixtureAppID, pcFixtureURL)
	if err != nil {
		t.Fatalf("parsePCAppPage: %v", err)
	}
	if !pc.OnPC {
		t.Error("OnPC is false for a page that is a PC listing")
	}
	if pc.Title != "Cooking Diary® Restaurant Game" {
		t.Errorf("Title = %q", pc.Title)
	}
	if pc.Developer != "Mytona" {
		t.Errorf("Developer = %q", pc.Developer)
	}
	if !strings.HasPrefix(pc.Icon, "https://") {
		t.Errorf("Icon = %q, want a URL", pc.Icon)
	}
	if len(pc.Screenshots) == 0 {
		t.Error("no screenshots")
	}
	if pc.YouTubeID == "" || !strings.Contains(pc.Video, pc.YouTubeID) {
		t.Errorf("YouTubeID = %q, Video = %q: want the id the trailer URL names", pc.YouTubeID, pc.Video)
	}
	if pc.Ratings <= 0 || pc.Score <= 0 || pc.ScoreText == "" {
		t.Errorf("rating = %v/%q from %d ratings, want the PC players' rating", pc.Score, pc.ScoreText, pc.Ratings)
	}
	if !slices.ContainsFunc(pc.Requirements, func(r PCRequirement) bool { return r.Label == "OS" && r.Value != "" }) {
		t.Errorf("Requirements = %v, want an OS line", pc.Requirements)
	}
	if !slices.ContainsFunc(pc.Requirements, func(r PCRequirement) bool { return r.Label == "" }) {
		t.Errorf("Requirements = %v, want the unlabelled lines kept", pc.Requirements)
	}
}

// The redirect Google answers for an app without a PC build is the whole
// answer. Following it would fetch the mobile listing, a megabyte that says
// nothing about PC, so the mobile route here fails the test if it is reached.
func TestPCAppNotOnPCStopsAtTheRedirect(t *testing.T) {
	var fixture struct {
		Status   int    `json:"status"`
		Location string `json:"location"`
	}
	if err := json.Unmarshal(readFixture(t, "pc_page_not_on_pc.json"), &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	c := newMockClient(t,
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pcPath {
				return mockResponse{}, false
			}
			return mockResponse{Status: fixture.Status, Header: http.Header{"Location": {fixture.Location}}}, true
		},
		func(req *http.Request) (mockResponse, bool) {
			t.Errorf("followed the redirect to %s", req.URL)
			return mockResponse{Body: readFixture(t, "app_page.html")}, true
		},
	)

	pc, err := c.PCApp(context.Background(), "com.whatsapp", AppOptions{})
	if err != nil {
		t.Fatalf("PCApp: %v; an app without a PC build is an answer, not an error", err)
	}
	// The record a pipeline sees: the id, the URL asked for, and the answer.
	raw, err := json.Marshal(pc)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"appId": "com.whatsapp",
		"url":   "https://play.google.com/pc-store/games/details?id=com.whatsapp&hl=en&gl=us",
		"onPC":  false,
	}
	if !maps.Equal(got, want) {
		t.Errorf("record = %v, want %v", got, want)
	}
}

func TestPCAppReadsTheListing(t *testing.T) {
	var asked *url.URL
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		asked = req.URL
		return mockResponse{Body: readFixture(t, "pc_page_game.html")}, req.URL.Path == pcPath
	})

	pc, err := c.PCApp(context.Background(), pcFixtureAppID, AppOptions{Lang: "de", Country: "at"})
	if err != nil {
		t.Fatalf("PCApp: %v", err)
	}
	if !pc.OnPC || pc.Title == "" {
		t.Errorf("OnPC = %v, Title = %q", pc.OnPC, pc.Title)
	}
	if q := asked.Query(); q.Get("id") != pcFixtureAppID || q.Get("hl") != "de" || q.Get("gl") != "at" {
		t.Errorf("asked for %s", asked)
	}
}

// A package Google does not know is a 404, as it is for App.
func TestPCAppNotFoundIsAnError(t *testing.T) {
	c := newMockClient(t, routePathStatus(pcPath, http.StatusNotFound))
	_, err := c.PCApp(context.Background(), "com.invalid.nothing", AppOptions{})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusNotFound {
		t.Errorf("err = %v, want a StatusError 404", err)
	}
}

// Only a redirect off the PC store is an answer. One to google.com/sorry is
// still followed, so it is still a refusal rather than "not on PC".
func TestPCAppSorryRedirectIsStillARefusal(t *testing.T) {
	c := newMockClient(t,
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pcPath {
				return mockResponse{}, false
			}
			return mockResponse{Status: http.StatusFound,
				Header: http.Header{"Location": {"https://www.google.com/sorry/index"}}}, true
		},
		routePath("/sorry/index", []byte("<html>captcha</html>")),
	)
	_, err := c.PCApp(context.Background(), pcFixtureAppID, AppOptions{})
	var sb *SoftBlockError
	if !errors.As(err, &sb) || sb.Verdict != VerdictBlocked {
		t.Errorf("err = %v, want a blocked SoftBlockError", err)
	}
}

func TestPCStoreClientRedirectPolicy(t *testing.T) {
	var consulted []string
	callers := &http.Client{CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		consulted = append(consulted, req.URL.String())
		return nil
	}}
	c := NewClient(WithHTTPClient(callers))
	check := c.pcStoreClient().CheckRedirect

	from := mustRequest(t, "https://play.google.com/pc-store/games/details?id=x")
	cases := []struct {
		to      string
		want    error
		callers bool // whether the caller's own policy is consulted
	}{
		{"https://play.google.com/store/apps/details?id=x", http.ErrUseLastResponse, false},
		{"https://play.google.com/pc-store/games/details?id=x&hl=de", nil, true},
		{"https://www.google.com/sorry/index", nil, true},
	}
	for _, tc := range cases {
		consulted = nil
		if err := check(mustRequest(t, tc.to), []*http.Request{from}); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.to, err, tc.want)
		}
		if got := len(consulted) == 1; got != tc.callers {
			t.Errorf("%s: caller's policy consulted = %v, want %v", tc.to, got, tc.callers)
		}
	}
	if c.httpClient != callers || callers.CheckRedirect == nil {
		t.Error("pcStoreClient changed the caller's client")
	}

	// Without a policy of its own, the copy keeps net/http's limit.
	plain := NewClient().pcStoreClient().CheckRedirect
	if err := plain(mustRequest(t, "https://www.google.com/x"), slices.Repeat([]*http.Request{from}, 10)); err == nil {
		t.Error("an eleventh redirect was allowed")
	}
}

func mustRequest(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestPCAppValidation(t *testing.T) {
	if _, err := NewClient().PCApp(context.Background(), "", AppOptions{}); err == nil {
		t.Error("expected an error for an empty appID")
	}
}

func TestYouTubeID(t *testing.T) {
	cases := map[string]string{
		"https://play.google.com/video/lava/web/player/yt:movie:RJ1ranFTqDQ?autoplay=1&embed=play": "RJ1ranFTqDQ",
		"https://www.youtube.com/watch?v=UVS3Cb2U-Xw":                                              "UVS3Cb2U-Xw",
		"https://youtube.com/watch?v=UVS3Cb2U-Xw&t=3":                                              "UVS3Cb2U-Xw",
		"https://example.com/watch?v=UVS3Cb2U-Xw":                                                  "",
		"https://play-games.googleusercontent.com/vp/mp4/1280x720/RJ1ranFTqDQ.mp4":                 "",
		"":          "",
		"://broken": "",
	}
	for in, want := range cases {
		if got := youTubeID(in); got != want {
			t.Errorf("youTubeID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePCRequirements(t *testing.T) {
	ds3 := []any{nil, nil, []any{[]any{
		[]any{"8 GB of RAM", "Memory"},
		[]any{"Windows admin account"},
		[]any{nil, "Label without a value"},
		"not a line",
	}}}
	want := []PCRequirement{{Label: "Memory", Value: "8 GB of RAM"}, {Value: "Windows admin account"}}
	if got := parsePCRequirements(ds3); !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := parsePCRequirements([]any{}); got != nil {
		t.Errorf("an empty block gave %v", got)
	}
}

func TestPCRegions(t *testing.T) {
	regions := PCRegions()
	if len(regions) != 141 {
		t.Errorf("%d regions, want the 141 the snapshot was taken with", len(regions))
	}
	if !slices.IsSorted(regions) || len(slices.Compact(slices.Clone(regions))) != len(regions) {
		t.Error("regions are not sorted and unique")
	}
	for _, r := range regions {
		if len(r) != 2 || strings.ToLower(r) != r {
			t.Errorf("%q is not a lowercase alpha-2 code", r)
		}
	}
	regions[0] = "zz"
	if PCRegions()[0] == "zz" {
		t.Error("PCRegions hands out its own slice")
	}
}
