package googleplayscraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
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

// Only the observed shape is the "no PC build" answer: the PC listing of a
// package redirecting to the phone listing of the same package, same host.
func TestIsPCExit(t *testing.T) {
	const pc = "https://play.google.com/pc-store/games/details?id=x&hl=en&gl=us"
	cases := []struct {
		from, to string
		want     bool
	}{
		{pc, "https://play.google.com/store/apps/details?id=x&hl=en&gl=us", true},
		{pc, "/store/apps/details?id=x", true}, // resolved against from below
		{pc, "https://play.google.com/store/apps/details?id=y", false},
		{pc, "https://play.google.com/store/apps/details", false},
		{pc, "https://play.google.com/store/", false},
		{pc, "https://play.google.com/store/apps/error?id=x", false},
		{pc, "https://play.google.com/pc-store/games/details?id=x&hl=de", false},
		{pc, "https://www.google.com/store/apps/details?id=x", false},
		{pc, "https://www.google.com/sorry/index", false},
		{"https://play.google.com/pc-store/games/other?id=x", "https://play.google.com/store/apps/details?id=x", false},
		{"https://play.google.com/pc-store/games/details", "https://play.google.com/store/apps/details", false},
	}
	for _, tc := range cases {
		from := mustRequest(t, tc.from).URL
		to := from.ResolveReference(mustRequest(t, tc.to).URL)
		if got := isPCExit(from, to); got != tc.want {
			t.Errorf("%s -> %s: %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestPCExitPolicy(t *testing.T) {
	pcHop := func(hl string) *http.Request {
		return mustRequest(t, "https://play.google.com/pc-store/games/details?id=x&hl="+hl)
	}
	exit := mustRequest(t, "https://play.google.com/store/apps/details?id=x")
	chain := func(n int) []*http.Request {
		via := make([]*http.Request, n)
		for i := range via {
			via[i] = pcHop(fmt.Sprint(i))
		}
		return via
	}

	t.Run("judges the hop being redirected, not the first request", func(t *testing.T) {
		p := &pcExitPolicy{}
		via := []*http.Request{pcHop("en"), mustRequest(t, "https://play.google.com/pc-store/games/other?id=x")}
		if err := p.checkRedirect(exit, via); err != nil || p.stopped.Load() {
			t.Errorf("err = %v, stopped = %v: a redirect from another page is not the answer", err, p.stopped.Load())
		}
		via = []*http.Request{mustRequest(t, "https://play.google.com/pc-store/games/other?id=x"), pcHop("de")}
		if err := p.checkRedirect(exit, via); !errors.Is(err, http.ErrUseLastResponse) || !p.stopped.Load() {
			t.Errorf("err = %v, stopped = %v: a locale hop then the exit is the answer", err, p.stopped.Load())
		}
	})

	t.Run("net/http's limit applies to the exit hop too", func(t *testing.T) {
		p := &pcExitPolicy{}
		if err := p.checkRedirect(exit, chain(9)); !errors.Is(err, http.ErrUseLastResponse) || !p.stopped.Load() {
			t.Errorf("ninth redirect: err = %v, stopped = %v, want the exit", err, p.stopped.Load())
		}
		p = &pcExitPolicy{}
		if err := p.checkRedirect(exit, chain(10)); err == nil || errors.Is(err, http.ErrUseLastResponse) || p.stopped.Load() {
			t.Errorf("tenth redirect: err = %v, stopped = %v, want the limit", err, p.stopped.Load())
		}
	})

	t.Run("the caller's policy has the first word", func(t *testing.T) {
		strict := errors.New("one redirect is enough")
		var consulted int
		p := &pcExitPolicy{next: func(_ *http.Request, via []*http.Request) error {
			consulted++
			switch {
			case len(via) >= 2:
				return strict
			case via[0].URL.Query().Get("hl") == "stop":
				return http.ErrUseLastResponse
			}
			return nil
		}}
		if err := p.checkRedirect(exit, chain(2)); !errors.Is(err, strict) || p.stopped.Load() {
			t.Errorf("err = %v, stopped = %v, want the caller's limit", err, p.stopped.Load())
		}
		if err := p.checkRedirect(exit, []*http.Request{pcHop("stop")}); !errors.Is(err, http.ErrUseLastResponse) || p.stopped.Load() {
			t.Errorf("err = %v, stopped = %v: a stop the caller asked for is not the PC answer", err, p.stopped.Load())
		}
		if err := p.checkRedirect(exit, []*http.Request{pcHop("en")}); !errors.Is(err, http.ErrUseLastResponse) || !p.stopped.Load() {
			t.Errorf("err = %v, stopped = %v, want the exit once the caller allows it", err, p.stopped.Load())
		}
		if consulted != 3 {
			t.Errorf("caller's policy consulted %d times for 3 redirects", consulted)
		}
	})

	t.Run("the caller's client is left alone", func(t *testing.T) {
		policy := func(*http.Request, []*http.Request) error { return nil }
		callers := &http.Client{CheckRedirect: policy}
		c := NewClient(WithHTTPClient(callers))
		hc, _ := c.pcStoreClient()
		if hc == callers || c.httpClient != callers || callers.CheckRedirect == nil {
			t.Error("pcStoreClient changed the caller's client")
		}
	})
}

// pcRedirects answers the PC listing with a 302 to location(hop), where hop
// counts the requests so far, and fails the test if the phone listing is
// fetched.
func pcRedirects(t *testing.T, location func(hop int) string) routeFunc {
	return func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pcPath {
			t.Errorf("followed a redirect to %s", req.URL)
			return mockResponse{Status: http.StatusNotFound}, true
		}
		hop, _ := strconv.Atoi(req.URL.Query().Get("hop"))
		return mockResponse{Status: http.StatusFound, Header: http.Header{"Location": {location(hop)}}}, true
	}
}

// A 3xx that is not the PC store's exit is the error it is, not "not on PC".
func TestPCAppOtherRedirectsStayErrors(t *testing.T) {
	const mobile = "https://play.google.com/store/apps/details?id=" + pcFixtureAppID
	const locale = "https://play.google.com/pc-store/games/details?id=" + pcFixtureAppID + "&hl=de"
	stopLocale := func(req *http.Request, via []*http.Request) error {
		switch {
		case req.URL.Query().Get("hl") == "de":
			return http.ErrUseLastResponse
		case len(via) >= 10: // a policy of its own replaces net/http's limit
			return errors.New("redirect loop")
		}
		return nil
	}
	stopAll := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	cases := []struct {
		name     string
		route    routeFunc
		redirect func(*http.Request, []*http.Request) error
		code     int
	}{
		{"302 without a Location", routePathStatus(pcPath, http.StatusFound), nil, http.StatusFound},
		{"304", routePathStatus(pcPath, http.StatusNotModified), nil, http.StatusNotModified},
		{"caller stops a locale redirect", pcRedirects(t, func(int) string { return locale }), stopLocale, http.StatusFound},
		{"caller stops the exit itself", pcRedirects(t, func(int) string { return mobile }), stopAll, http.StatusFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := &routingTransport{t: t, routes: []routeFunc{tc.route}}
			c := NewClient(WithHTTPClient(&http.Client{Transport: rt, CheckRedirect: tc.redirect}))
			pc, err := c.PCApp(context.Background(), pcFixtureAppID, AppOptions{})
			var se *StatusError
			if !errors.As(err, &se) || se.Code != tc.code {
				t.Errorf("PCApp = %+v, %v; want a StatusError %d", pc, err, tc.code)
			}
		})
	}
}

// net/http stops a chain at ten requests, so it follows nine redirects, and
// the hop that leaves the PC store counts towards that like any other.
func TestPCAppExitWithinTheRedirectLimit(t *testing.T) {
	const mobile = "https://play.google.com/store/apps/details?id=" + pcFixtureAppID
	for _, tc := range []struct {
		localeHops int
		onPCAnswer bool
	}{
		{0, true},
		{8, true},  // the exit is the ninth redirect: allowed
		{9, false}, // the tenth: refused, whatever it points at
	} {
		c := newMockClient(t, pcRedirects(t, func(hop int) string {
			if hop < tc.localeHops {
				return fmt.Sprintf("%s&hop=%d", pcFixtureURL, hop+1)
			}
			return mobile
		}))
		pc, err := c.PCApp(context.Background(), pcFixtureAppID, AppOptions{})
		if got := err == nil && pc != nil && !pc.OnPC; got != tc.onPCAnswer {
			t.Errorf("%d locale hops then the exit: PCApp = %+v, %v; want the not-on-PC answer %v",
				tc.localeHops, pc, err, tc.onPCAnswer)
		}
	}
}

// A cancel that lands after the exit was recognised is the caller leaving: the
// pipeline wraps it together with the 302, and PCApp must not unwrap that into
// a successful not-on-PC answer.
func TestPCAppCancelAfterExitIsAnError(t *testing.T) {
	const mobile = "https://play.google.com/store/apps/details?id=" + pcFixtureAppID
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := &routingTransport{t: t, routes: []routeFunc{pcRedirects(t, func(int) string { return mobile })}}
	c := NewClient(
		WithHTTPClient(&http.Client{Transport: rt}),
		WithHooks(Hooks{OnResponse: func(string, string, int, int, time.Duration, error) { cancel() }}),
	)
	pc, err := c.PCApp(ctx, pcFixtureAppID, AppOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PCApp = %+v, %v; want an error wrapping context.Canceled", pc, err)
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
