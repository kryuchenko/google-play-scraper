//go:build canary

// Package canary tests are LIVE contract tests against the real Google Play.
//
// Unlike parser_fixtures_test.go (frozen testdata) and the *_test.go integration
// suite (mostly len>0 asserts), every test here makes STRICT field-level
// assertions on freshly fetched data. Their purpose is drift detection: when
// Google changes its page layout or RPC response shape, the corresponding parse
// path stops populating a field and exactly one subtest turns red, naming the
// METHOD, the FIELD, and the suspected data path.
//
// They are gated behind the `canary` build tag so they neither compile nor run
// in the normal `go test` / `go test -short` flow — only `go test -tags canary`
// pulls them in. See .github/workflows/canary.yml for the scheduled run.
//
// All subtests share one throttled, sequential client to stay well under
// Google's anonymous rate limits (~30-50 requests total across the suite).

package googleplayscraper_test

import (
	"context"
	"strings"
	"testing"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper/v2"
)

// Stable target apps. These are picked for longevity: large, first-party or
// flagship titles unlikely to be delisted, so a failure points at Google's
// layout rather than the app vanishing.
const (
	canaryStableApp = "com.google.android.apps.maps" // free, non-game, well-formed
	canaryGameApp   = "com.king.candycrushsaga"      // game with IAP, video, GAME genre
	canaryReviewApp = "com.instagram.android"        // high review volume, stable
)

// newCanaryClient builds the shared client: a 1s throttle between request
// starts keeps the whole suite gentle on Google's rate limiter.
func newCanaryClient() *googleplayscraper.Client {
	return googleplayscraper.NewClient(
		googleplayscraper.WithThrottle(1*time.Second),
		// Retries, because this suite's job is to detect drift in Google's
		// payloads and a transient 5xx is not drift. Without them a weekly run
		// goes red for reasons nobody can reproduce, and a drift detector that
		// cries wolf is worse than none: it gets ignored on the week it is
		// right. Observed once here -- a missing-id probe took a retry's worth
		// of extra time and surfaced a fetch error instead of its status.
		googleplayscraper.WithRetry(googleplayscraper.RetryPolicy{
			MaxAttempts:       3,
			BaseDelay:         time.Second,
			RespectRetryAfter: true,
		}),
	)
}

// canaryCtx returns a per-subtest context with a generous timeout. Multi-request
// methods (CategoryApps, ReviewsSeq) get more headroom via their own contexts.
func canaryCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 45*time.Second)
}

// TestCanary is the single entry point. Each public method is a named subtest so
// a failure line reads e.g. "TestCanary/App/game_fields" — the method and the
// broken contract are visible without reading the body.
func TestCanary(t *testing.T) {
	client := newCanaryClient()

	t.Run("App", func(t *testing.T) { canaryApp(t, client) })
	t.Run("Search", func(t *testing.T) { canarySearch(t, client) })
	t.Run("List", func(t *testing.T) { canaryList(t, client) })
	t.Run("ClusterURLs+Cluster", func(t *testing.T) { canaryCluster(t, client) })
	t.Run("Reviews", func(t *testing.T) { canaryReviews(t, client) })
	t.Run("ReviewsSeq", func(t *testing.T) { canaryReviewsSeq(t, client) })
	t.Run("Developer", func(t *testing.T) { canaryDeveloper(t, client) })
	t.Run("Similar", func(t *testing.T) { canarySimilar(t, client) })
	t.Run("Permissions", func(t *testing.T) { canaryPermissions(t, client) })
	t.Run("DataSafety", func(t *testing.T) { canaryDataSafety(t, client) })
	t.Run("Suggest", func(t *testing.T) { canarySuggest(t, client) })
	t.Run("Categories", func(t *testing.T) { canaryCategories(t, client) })
	t.Run("CategoryApps", func(t *testing.T) { canaryCategoryApps(t, client) })
	t.Run("Availability", func(t *testing.T) { canaryAvailability(t, client) })
	t.Run("Sitemap", func(t *testing.T) { canarySitemap(t, client) })
	t.Run("Batched", func(t *testing.T) { canaryBatched(t, client) })
	t.Run("AvailabilityClasses", func(t *testing.T) { canaryAvailabilityClasses(t, client) })
	t.Run("Collections", func(t *testing.T) { canaryCollections(t, client) })
}

// ---------------------------------------------------------------------------
// App
// ---------------------------------------------------------------------------

func canaryApp(t *testing.T, client *googleplayscraper.Client) {
	ctx, cancel := canaryCtx(t)
	defer cancel()

	app, err := client.App(ctx, canaryStableApp, googleplayscraper.AppOptions{})
	if err != nil {
		t.Fatalf("App(%s): %v", canaryStableApp, err)
	}

	if app.Title != "Google Maps" {
		t.Errorf("App: Title = %q, want %q — title path [1][2][0][0] may have changed", app.Title, "Google Maps")
	}
	if app.Developer == "" {
		t.Error("App: Developer empty — path [1][2][68][0] may have changed")
	}
	if app.GenreID == "" {
		t.Error("App: GenreID empty — path [1][2][79][0][0][2] may have changed")
	}
	if app.Score <= 0 {
		t.Errorf("App: Score = %v, want > 0 — path [1][2][51][0][1] may have changed", app.Score)
	}
	if app.Ratings <= 0 {
		t.Errorf("App: Ratings = %d, want > 0 — path [1][2][51][2][1] may have changed", app.Ratings)
	}
	if app.Installs == "" {
		t.Error("App: Installs empty — path [1][2][13][0] may have changed")
	}
	// Updated (last-update epoch, path [1][2][145][0][1][0]) is the reliable
	// date signal. Released ([1][2][10][1][0]) is intentionally NOT asserted:
	// Google currently omits it for some flagship listings (e.g. Google Maps)
	// even though the offline parser path is unchanged — asserting it would make
	// this canary permanently red for a non-regression. See report for QA.
	if app.Updated <= 0 {
		t.Error("App: Updated empty — path [1][2][145][0][1][0] may have changed")
	}
	if len(app.Screenshots) < 1 {
		t.Error("App: Screenshots empty — path [1][2][78][0] may have changed")
	}
	var histSum int
	for _, h := range app.Histogram {
		histSum += h
	}
	if histSum <= 0 {
		t.Errorf("App: Histogram sums to %d, want > 0 — path [1][2][51][1] may have changed", histSum)
	}
	if !strings.Contains(app.URL, canaryStableApp) {
		t.Errorf("App: URL = %q does not contain appID %q", app.URL, canaryStableApp)
	}

	// Game-specific fields exercise the monetization/media/changelog parse paths
	// that the non-game stable app does not populate.
	t.Run("game_fields", func(t *testing.T) {
		gctx, gcancel := canaryCtx(t)
		defer gcancel()

		game, err := client.App(gctx, canaryGameApp, googleplayscraper.AppOptions{})
		if err != nil {
			t.Fatalf("App(%s): %v", canaryGameApp, err)
		}

		if !strings.HasPrefix(game.GenreID, "GAME") {
			t.Errorf("App(game): GenreID = %q, want GAME* — path [1][2][79][0][0][2] may have changed", game.GenreID)
		}
		if !game.OffersIAP || game.IAPRange == "" {
			t.Errorf("App(game): OffersIAP=%v IAPRange=%q, want IAP present — path [1][2][19][0] may have changed", game.OffersIAP, game.IAPRange)
		}
		if game.Video == "" && game.HeaderImage == "" {
			t.Error("App(game): both Video ([1][2][100]...) and HeaderImage ([1][2][96]...) empty — media parse path may have changed")
		}
		if game.RecentChanges == "" {
			t.Error("App(game): RecentChanges empty — path [1][2][144][1][1] may have changed")
		}
		// Released is absent on a few flagship listings (e.g. Maps), so it is
		// asserted here on the game app, where its [1][2][10] node is present.
		if game.Released == 0 {
			t.Error("App(game): Released zero — path [1][2][10][1][0] may have changed")
		}
	})
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

func canarySearch(t *testing.T, client *googleplayscraper.Client) {
	ctx, cancel := canaryCtx(t)
	defer cancel()

	results, err := client.Search(ctx, googleplayscraper.SearchOptions{
		Term: "minecraft",
		Num:  20,
	})
	if err != nil {
		t.Fatalf("Search(minecraft): %v", err)
	}

	if len(results) < 10 {
		t.Fatalf("Search: got %d results, want >= 10 — apps array path or pagination may have changed", len(results))
	}

	// We assert on Mojang's app FAMILY (the "com.mojang." prefix), not a single
	// exact package: Google's relevance ranking shuffles which Minecraft title
	// leads (Minecraft Education vs. minecraftpe vs. minecraftedu), so pinning
	// one package would flake on ranking changes rather than on a parse break.
	// The contract being verified is "search returns the relevant publisher's
	// apps", which is what actually breaks if appId parsing regresses.
	const wantPrefix = "com.mojang."
	found := false
	for i, r := range results {
		if r.AppID == "" {
			t.Errorf("Search: result[%d] AppID empty — appId path in parseSearchResultNew may have changed", i)
		}
		if !strings.Contains(r.AppID, ".") {
			t.Errorf("Search: result[%d] AppID = %q does not look like a package name", i, r.AppID)
		}
		if r.Title == "" {
			t.Errorf("Search: result[%d] (%s) Title empty — title path [3] may have changed", i, r.AppID)
		}
		if strings.HasPrefix(r.AppID, wantPrefix) {
			found = true
		}
	}
	if !found {
		t.Errorf("Search(minecraft): no %q* app in results — relevance ranking or appId parsing may have changed", wantPrefix)
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func canaryList(t *testing.T, client *googleplayscraper.Client) {
	ctx, cancel := canaryCtx(t)
	defer cancel()

	const want = 50
	results, err := client.List(ctx, googleplayscraper.ListOptions{
		Category:   googleplayscraper.CategoryGame,
		Collection: googleplayscraper.CollectionTopFree,
		Num:        want,
	})
	if err != nil {
		t.Fatalf("List(GAME, TOP_FREE, 50): %v", err)
	}

	if len(results) != want {
		t.Errorf("List: got %d apps, want %d — vyAe2 apps path [0][1][0][28][0] may have changed", len(results), want)
	}
	if len(results) == 0 {
		return
	}

	seen := make(map[string]bool, len(results))
	for i, r := range results {
		if r.AppID == "" {
			t.Errorf("List: result[%d] AppID empty — path [0][0][0] may have changed", i)
			continue
		}
		if seen[r.AppID] {
			t.Errorf("List: duplicate AppID %q at index %d — list should be unique", r.AppID, i)
		}
		seen[r.AppID] = true
		if r.Title == "" {
			t.Errorf("List: result[%d] (%s) Title empty — path [0][3] may have changed", i, r.AppID)
		}
	}
	if results[0].Title == "" || results[0].AppID == "" {
		t.Error("List: first result is not meaningful (empty Title or AppID)")
	}
}

// ---------------------------------------------------------------------------
// ClusterURLs + Cluster
// ---------------------------------------------------------------------------

func canaryCluster(t *testing.T, client *googleplayscraper.Client) {
	ctx, cancel := canaryCtx(t)
	defer cancel()

	clusters, err := client.ClusterURLs(ctx, googleplayscraper.ClusterURLsOptions{
		Category: googleplayscraper.CategoryGameAction,
	})
	if err != nil {
		t.Fatalf("ClusterURLs(GAME_ACTION): %v", err)
	}
	if len(clusters) < 1 {
		t.Fatalf("ClusterURLs: got 0 clusters — section path [21][1][2][4][2] may have changed")
	}
	for i, cl := range clusters {
		if cl.Title == "" {
			t.Errorf("ClusterURLs: cluster[%d] Title empty — path [21][1][0] may have changed", i)
		}
		if cl.URL == "" {
			t.Errorf("ClusterURLs: cluster[%d] (%s) URL empty — path [21][1][2][4][2] may have changed", i, cl.Title)
		}
	}

	cctx, ccancel := canaryCtx(t)
	defer ccancel()

	apps, err := client.Cluster(cctx, googleplayscraper.ClusterOptions{
		Path: clusters[0].URL,
		Num:  60,
	})
	if err != nil {
		t.Fatalf("Cluster(%q): %v", clusters[0].Title, err)
	}
	if len(apps) < 10 {
		t.Errorf("Cluster: got %d apps, want >= 10 — apps path [0][1][0][21][0] may have changed", len(apps))
	}
	for i, a := range apps {
		if a.AppID == "" {
			t.Errorf("Cluster: app[%d] AppID empty — parseSearchResultNew appId path may have changed", i)
		}
	}

	t.Run("pagination", func(t *testing.T) { canaryClusterPagination(t, client) })
}

// canaryClusterPagination guards the qnKhOb recommendation-feed extension wired
// into Cluster. It runs against the GAME_ACTION *category* page, whose
// "recommended for you" sections expose the recs_topic feed tokens the extension
// follows.
//
// MECHANISM (reverse-engineered and re-confirmed live 2026-06-12): each
// recommendation section links to a cluster URL whose gsr query value is a
// base64url protobuf wrapping the topic's recs query. Re-wrapping that query
// from its field-9 gsr form into the field-12 form the qnKhOb RPC expects yields
// a stateless token that returns the whole topic in one request. extractFeedTokens
// harvests one such token per section. (The deeper "next topic" pointer in a
// response — [0][3][0] — is server-stateful and NULLs on replay, so it is NOT
// used.) Page-1 of the feed is therefore reachable statelessly; page-2+ of a
// single topic is not, but harvesting every topic on the page is the better win.
//
// This canary asserts the load-bearing contract: turning FollowFeed ON returns
// STRICTLY MORE apps than the initial grid alone. A green here means the token
// extraction + payload are alive; a red means one of them drifted.
func canaryClusterPagination(t *testing.T, client *googleplayscraper.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const feedPath = "/store/apps/category/GAME_ACTION"

	initial, err := client.Cluster(ctx, googleplayscraper.ClusterOptions{
		Path: feedPath,
		Num:  500,
	})
	if err != nil {
		t.Fatalf("Cluster(GAME_ACTION, initial): %v", err)
	}
	if len(initial) < 10 {
		t.Fatalf("Cluster(GAME_ACTION): initial grid got %d apps, want >= 10 — "+
			"cluster apps path [0][1][0][21][0] likely drifted", len(initial))
	}

	extended, err := client.Cluster(ctx, googleplayscraper.ClusterOptions{
		Path:       feedPath,
		Num:        500,
		FollowFeed: true,
	})
	if err != nil {
		t.Fatalf("Cluster(GAME_ACTION, FollowFeed): %v", err)
	}

	// The extension must add apps. Equality means the feed extension is dead.
	if len(extended) <= len(initial) {
		t.Errorf("Cluster(GAME_ACTION): FollowFeed added nothing (initial=%d, extended=%d) — "+
			"qnKhOb feed extension dead: recs_topic token extraction (extractFeedTokens) or the "+
			"qnKhOb payload likely drifted; re-capture f.req from the browser (see qnkhob_payload.txt)",
			len(initial), len(extended))
	}

	seen := make(map[string]bool, len(extended))
	for i, a := range extended {
		if a.AppID == "" {
			t.Errorf("Cluster(GAME_ACTION): app[%d] has empty AppID", i)
			continue
		}
		if seen[a.AppID] {
			t.Errorf("Cluster(GAME_ACTION): duplicate AppID %q — paginateQnKhOb dedup regressed", a.AppID)
		}
		seen[a.AppID] = true
	}
}

// ---------------------------------------------------------------------------
// Reviews
// ---------------------------------------------------------------------------

func canaryReviews(t *testing.T, client *googleplayscraper.Client) {
	ctx, cancel := canaryCtx(t)
	defer cancel()

	res, err := client.Reviews(ctx, canaryReviewApp, googleplayscraper.ReviewOptions{
		Sort:  googleplayscraper.SortNewest,
		Count: 50,
	})
	if err != nil {
		t.Fatalf("Reviews(%s): %v", canaryReviewApp, err)
	}
	if len(res.Reviews) < 1 {
		t.Fatalf("Reviews: got 0 reviews — oCPfdb response shape (data[0]) may have changed")
	}
	if res.NextToken == "" {
		t.Error("Reviews: NextToken empty — pagination token path data[1][1] may have changed")
	}

	r := res.Reviews[0]
	if strings.TrimSpace(r.Text) == "" {
		t.Error("Reviews: first review Text empty — path [4] may have changed")
	}
	if r.Score < 1 || r.Score > 5 {
		t.Errorf("Reviews: Score = %d, want 1..5 — path [2] may have changed", r.Score)
	}
	if r.Date.IsZero() {
		t.Error("Reviews: Date is zero — timestamp path [5] may have changed")
	}
	if r.UserName == "" {
		t.Error("Reviews: UserName (Author) empty — path [1][0] may have changed")
	}

	t.Run("filter_score_5", func(t *testing.T) {
		fctx, fcancel := canaryCtx(t)
		defer fcancel()

		filtered, err := client.Reviews(fctx, canaryReviewApp, googleplayscraper.ReviewOptions{
			Sort:        googleplayscraper.SortNewest,
			Count:       50,
			FilterScore: 5,
		})
		if err != nil {
			t.Fatalf("Reviews(FilterScore=5): %v", err)
		}
		if len(filtered.Reviews) < 1 {
			t.Fatal("Reviews(FilterScore=5): got 0 reviews")
		}
		for i, rv := range filtered.Reviews {
			if rv.Score != 5 {
				t.Errorf("Reviews(FilterScore=5): review[%d] Score = %d, want 5 — score filter in oCPfdb payload may be ignored", i, rv.Score)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// ReviewsSeq
// ---------------------------------------------------------------------------

func canaryReviewsSeq(t *testing.T, client *googleplayscraper.Client) {
	// Pagination is what is under test here; give it a wider budget.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const want = 200
	var reviews []googleplayscraper.Review
	for r, err := range client.ReviewsSeq(ctx, canaryReviewApp, googleplayscraper.ReviewOptions{
		Sort: googleplayscraper.SortNewest,
	}) {
		if err != nil {
			t.Fatalf("ReviewsSeq(%s): %v", canaryReviewApp, err)
		}
		reviews = append(reviews, r)
		if len(reviews) == want {
			break
		}
	}
	// A single page is 150; crossing that proves pagination is wired through.
	if len(reviews) <= 150 {
		t.Errorf("ReviewsSeq: got %d reviews, want > 150 (more than one page) — NextToken pagination may have broken", len(reviews))
	}
	for i, r := range reviews {
		if r.ID == "" {
			t.Errorf("ReviewsSeq: review[%d] ID empty — path [0] may have changed", i)
			break
		}
	}
}

// ---------------------------------------------------------------------------
// Developer
// ---------------------------------------------------------------------------

func canaryDeveloper(t *testing.T, client *googleplayscraper.Client) {
	ctx, cancel := canaryCtx(t)
	defer cancel()

	results, err := client.Developer(ctx, googleplayscraper.DeveloperOptions{
		DevID: "Google LLC",
	})
	if err != nil {
		t.Fatalf("Developer(Google LLC): %v", err)
	}
	if len(results) < 5 {
		t.Errorf("Developer: got %d apps, want >= 5 — apps path [0][1][0][22][0] may have changed", len(results))
	}
	for i, r := range results {
		if r.AppID == "" {
			t.Errorf("Developer: result[%d] AppID empty — path [0][0][0] may have changed", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Similar
// ---------------------------------------------------------------------------

func canarySimilar(t *testing.T, client *googleplayscraper.Client) {
	ctx, cancel := canaryCtx(t)
	defer cancel()

	results, err := client.Similar(ctx, googleplayscraper.SimilarOptions{
		AppID: canaryStableApp,
	})
	if err != nil {
		t.Fatalf("Similar(%s): %v", canaryStableApp, err)
	}
	if len(results) < 5 {
		t.Errorf("Similar: got %d apps, want >= 5 — 'Similar' cluster discovery or apps path may have changed", len(results))
	}
	for i, r := range results {
		if r.AppID == "" {
			t.Errorf("Similar: result[%d] AppID empty — path [0][0] may have changed", i)
		}
		if r.AppID == canaryStableApp {
			t.Errorf("Similar: results contain the seed app %q itself", canaryStableApp)
		}
	}
}

// ---------------------------------------------------------------------------
// Permissions
// ---------------------------------------------------------------------------

func canaryPermissions(t *testing.T, client *googleplayscraper.Client) {
	ctx, cancel := canaryCtx(t)
	defer cancel()

	perms, err := client.Permissions(ctx, googleplayscraper.PermissionsOptions{
		AppID: canaryStableApp,
	})
	if err != nil {
		t.Fatalf("Permissions(%s): %v", canaryStableApp, err)
	}
	if len(perms) < 1 {
		t.Fatalf("Permissions: got 0 — xdSrCf response shape or group path [2] may have changed")
	}
	hasNamed := false
	for _, p := range perms {
		if p.Permission != "" {
			hasNamed = true
			break
		}
	}
	if !hasNamed {
		t.Error("Permissions: every entry has empty Permission — perm name path [1] may have changed")
	}
}

// ---------------------------------------------------------------------------
// DataSafety
// ---------------------------------------------------------------------------

func canaryDataSafety(t *testing.T, client *googleplayscraper.Client) {
	ctx, cancel := canaryCtx(t)
	defer cancel()

	ds, err := client.DataSafety(ctx, googleplayscraper.DataSafetyOptions{
		AppID: canaryStableApp,
	})
	if err != nil {
		t.Fatalf("DataSafety(%s): %v", canaryStableApp, err)
	}
	if len(ds.CollectedData) == 0 && len(ds.SharedData) == 0 && ds.PrivacyPolicyURL == "" {
		t.Error("DataSafety: CollectedData, SharedData and PrivacyPolicyURL all empty — safety section path [1][2][1][138] may have changed")
	}
}

// ---------------------------------------------------------------------------
// Suggest
// ---------------------------------------------------------------------------

func canarySuggest(t *testing.T, client *googleplayscraper.Client) {
	ctx, cancel := canaryCtx(t)
	defer cancel()

	suggestions, err := client.Suggest(ctx, googleplayscraper.SuggestOptions{
		Term: "face",
	})
	if err != nil {
		t.Fatalf("Suggest(face): %v", err)
	}
	if len(suggestions) < 1 {
		t.Fatalf("Suggest: got 0 suggestions — IJ4APc response shape (data[0][0]) may have changed")
	}
	for i, s := range suggestions {
		if strings.TrimSpace(s) == "" {
			t.Errorf("Suggest: suggestion[%d] is empty — suggestion text path [0] may have changed", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Categories (no network)
// ---------------------------------------------------------------------------

func canaryCategories(t *testing.T, client *googleplayscraper.Client) {
	ctx, cancel := canaryCtx(t)
	defer cancel()

	cats, err := client.Categories(ctx, googleplayscraper.CategoriesOptions{})
	if err != nil {
		t.Fatalf("Categories(): %v", err)
	}
	if len(cats) < 50 {
		t.Errorf("Categories: got %d, want >= 50 — AllCategories list shrank unexpectedly", len(cats))
	}
	found := false
	for _, c := range cats {
		if c == googleplayscraper.CategoryGameAction {
			found = true
			break
		}
	}
	if !found {
		t.Error("Categories: GAME_ACTION not present in returned list")
	}
}

// ---------------------------------------------------------------------------
// CategoryApps
// ---------------------------------------------------------------------------

func canaryCategoryApps(t *testing.T, client *googleplayscraper.Client) {
	// A small but real run: one locale, no search dictionary, no graph walk.
	// MaxApps is set above the ~200 single-request ceiling to prove the union
	// of independent slices breaks past it. This issues a bounded number of
	// requests, so it gets a wider timeout than the single-request methods.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	res, err := client.CategoryApps(ctx, googleplayscraper.CoverageOptions{
		Category:    googleplayscraper.CategoryGameAction,
		Locales:     []googleplayscraper.Locale{{Country: "us", Lang: "en"}},
		SearchTerms: []string{},
		GraphDepth:  0,
		MaxApps:     300,
	})
	if err != nil {
		t.Fatalf("CategoryApps(GAME_ACTION): %v", err)
	}

	if res.RequestsMade <= 0 {
		t.Error("CategoryApps: RequestsMade = 0 — no source was attempted")
	}
	if len(res.Apps) <= 200 {
		t.Errorf("CategoryApps: got %d unique apps, want > 200 — coverage union no longer beats the single-request ceiling", len(res.Apps))
	}

	seen := make(map[string]bool, len(res.Apps))
	for i, a := range res.Apps {
		if a.AppID == "" {
			t.Errorf("CategoryApps: app[%d] has empty AppID", i)
			continue
		}
		if seen[a.AppID] {
			t.Errorf("CategoryApps: duplicate AppID %q — dedup in resultSet may have regressed", a.AppID)
		}
		seen[a.AppID] = true
	}
}

// canaryAppRegionLocked is a stable US-only app: its listing exists in the US
// ([18]=[2]) but is not offered in Germany ([18]=[]), so a de probe must return
// StatusNotInRegion. Verified live 2026-06-11. If Google ever opens it in the
// EU, swap in another US-only carrier app.
const canaryAppRegionLocked = "com.vzw.hss.myverizon"

// canaryAvailability asserts the three availability outcomes are genuinely
// distinguishable on live data, all keyed off the [18] node:
//   - an available app reports StatusAvailable in its home region;
//   - a US-only app reports StatusNotInRegion abroad;
//   - a nonexistent app reports StatusNotFound and GloballyRemoved.
func canaryAvailability(t *testing.T, client *googleplayscraper.Client) {
	t.Run("available", func(t *testing.T) {
		ctx, cancel := canaryCtx(t)
		defer cancel()

		res, err := client.Availability(ctx, canaryStableApp, googleplayscraper.AvailabilityOptions{
			Countries: []string{"us"},
		})
		if err != nil {
			t.Fatalf("Availability(%s, us): %v", canaryStableApp, err)
		}
		if got := res.Statuses["us"]; got != googleplayscraper.StatusAvailable {
			t.Errorf("Availability(%s).Statuses[us] = %v, want StatusAvailable — [18][0] is no longer 2 for an available app", canaryStableApp, got)
		}
	})

	t.Run("not_in_region", func(t *testing.T) {
		ctx, cancel := canaryCtx(t)
		defer cancel()

		res, err := client.Availability(ctx, canaryAppRegionLocked, googleplayscraper.AvailabilityOptions{
			Countries: []string{"de"},
		})
		if err != nil {
			t.Fatalf("Availability(%s, de): %v", canaryAppRegionLocked, err)
		}
		if got := res.Statuses["de"]; got != googleplayscraper.StatusNotInRegion {
			t.Errorf("Availability(%s).Statuses[de] = %v, want StatusNotInRegion — the [18]=[] region-lock signal changed (or the app opened in the EU)", canaryAppRegionLocked, got)
		}
	})

	t.Run("not_found_globally_removed", func(t *testing.T) {
		ctx, cancel := canaryCtx(t)
		defer cancel()

		const ghost = "com.invalid.nonexistent.app.xyz123"
		res, err := client.Availability(ctx, ghost, googleplayscraper.AvailabilityOptions{
			Countries: []string{"us"},
		})
		if err != nil {
			t.Fatalf("Availability(%s, us): %v", ghost, err)
		}
		if got := res.Statuses["us"]; got != googleplayscraper.StatusNotFound {
			t.Errorf("Availability(%s).Statuses[us] = %v, want StatusNotFound — a missing listing no longer 404s", ghost, got)
		}
		if !res.GloballyRemoved {
			t.Error("Availability(nonexistent): GloballyRemoved = false, want true — every checked country 404'd")
		}
	})
}

// ---------------------------------------------------------------------------
// Sitemap / full-catalog enumeration
// ---------------------------------------------------------------------------

func canarySitemap(t *testing.T, client *googleplayscraper.Client) {
	// robots.txt must still advertise at least one sitemap index, all under the
	// /sitemaps/ path. If Google moves or drops the directive, discovery breaks
	// at the root and this fails first.
	t.Run("index_discovery", func(t *testing.T) {
		ctx, cancel := canaryCtx(t)
		defer cancel()

		indexes, err := client.SitemapIndexURLs(ctx)
		if err != nil {
			t.Fatalf("SitemapIndexURLs: %v", err)
		}
		if len(indexes) == 0 {
			t.Fatal("SitemapIndexURLs: 0 indexes — robots.txt no longer advertises Sitemap directives")
		}
		for _, u := range indexes {
			if !strings.HasPrefix(u, "https://play.google.com/sitemaps/") {
				t.Errorf("SitemapIndexURLs: %q is not under https://play.google.com/sitemaps/ — index location moved", u)
			}
		}
	})

	// The first index must parse into a large shard list (Google ships tens of
	// thousands). A handful would mean the <sitemapindex>/<loc> shape changed.
	t.Run("shard_listing", func(t *testing.T) {
		ctx, cancel := canaryCtx(t)
		defer cancel()

		indexes, err := client.SitemapIndexURLs(ctx)
		if err != nil {
			t.Fatalf("SitemapIndexURLs: %v", err)
		}
		shards, err := client.SitemapShards(ctx, indexes[0])
		if err != nil {
			t.Fatalf("SitemapShards(%s): %v", indexes[0], err)
		}
		if len(shards) < 1000 {
			t.Errorf("SitemapShards(%s): %d shards, want >=1000 — the sitemapindex shape may have changed", indexes[0], len(shards))
		}
		if len(shards) > 0 && !strings.Contains(shards[0], ".xml.gz") {
			t.Errorf("SitemapShards: first shard %q is not a .xml.gz — shard URL shape changed", shards[0])
		}
	})

	// A single shard must decompress, parse, and yield real app package ids
	// mixed in among the books/movies/music URLs. Zero apps would mean either
	// the gzip/urlset handling broke or the /store/apps/details filter no longer
	// matches.
	t.Run("shard_packages", func(t *testing.T) {
		ctx, cancel := canaryCtx(t)
		defer cancel()

		indexes, err := client.SitemapIndexURLs(ctx)
		if err != nil {
			t.Fatalf("SitemapIndexURLs: %v", err)
		}
		shards, err := client.SitemapShards(ctx, indexes[0])
		if err != nil {
			t.Fatalf("SitemapShards: %v", err)
		}
		pkgs, err := client.SitemapShardPackages(ctx, shards[0])
		if err != nil {
			t.Fatalf("SitemapShardPackages(%s): %v", shards[0], err)
		}
		if len(pkgs) == 0 {
			t.Fatalf("SitemapShardPackages(%s): 0 app packages — gzip/urlset parse or /store/apps/details filter broke", shards[0])
		}
		for _, p := range pkgs {
			if !strings.Contains(p, ".") {
				t.Errorf("SitemapShardPackages: %q does not look like a package id — id extraction off", p)
			}
		}
	})

	// CatalogSeq over a tiny shard subset must wire discovery -> fetch ->
	// emit end to end and produce ids.
	t.Run("enumerate_subset", func(t *testing.T) {
		ctx, cancel := canaryCtx(t)
		defer cancel()

		var n int
		for _, err := range client.CatalogSeq(ctx, googleplayscraper.CatalogOptions{
			Shards: []int{0, 1},
		}) {
			if err != nil {
				t.Fatalf("CatalogSeq(shards 0,1): %v", err)
			}
			n++
		}
		if n == 0 {
			t.Error("CatalogSeq(shards 0,1): yielded 0 packages — orchestration produced nothing")
		}
	})
}

// canaryBatched checks the operations that pack several RPCs into one request
// against the one-at-a-time methods they mirror.
//
// This is drift detection of a particular kind. AppsMany does not scrape the
// details page: it calls Ws7gDc, the RPC that page names in its own
// AF_dataServiceRequests map as the source of the ds:5 block, using the request
// body captured from there. That body selects which fields Google returns. If
// Google renumbers those fields or changes the request, the RPC keeps answering
// -- with less in it -- and nothing fails loudly. Comparing against App, which
// reads the rendered page, is what turns a silent degradation into a red test.
//
// Rating and review counters move continuously on a popular app, so two
// requests seconds apart legitimately disagree on them; everything static must
// match exactly.
func canaryBatched(t *testing.T, client *googleplayscraper.Client) {
	t.Run("AppsMany_matches_App", func(t *testing.T) {
		ctx, cancel := canaryCtx(t)
		defer cancel()

		page, err := client.App(ctx, canaryStableApp, googleplayscraper.AppOptions{})
		if err != nil {
			t.Fatalf("App: %v", err)
		}
		results := client.AppsMany(ctx, []string{canaryStableApp}, googleplayscraper.AppOptions{})
		if len(results) != 1 {
			t.Fatalf("AppsMany returned %d results for one app", len(results))
		}
		if results[0].Err != nil {
			t.Fatalf("AppsMany: %v", results[0].Err)
		}
		rpc := results[0].App

		// Field-by-field, so a failure names the field that stopped arriving
		// rather than dumping two structs.
		for _, f := range []struct {
			name      string
			page, rpc any
		}{
			{"Title", page.Title, rpc.Title},
			{"AppID", page.AppID, rpc.AppID},
			{"URL", page.URL, rpc.URL},
			{"Developer", page.Developer, rpc.Developer},
			{"DeveloperID", page.DeveloperID, rpc.DeveloperID},
			{"Genre", page.Genre, rpc.Genre},
			{"GenreID", page.GenreID, rpc.GenreID},
			{"Icon", page.Icon, rpc.Icon},
			{"Free", page.Free, rpc.Free},
			{"Available", page.Available, rpc.Available},
			{"ContentRating", page.ContentRating, rpc.ContentRating},
			{"Summary", page.Summary, rpc.Summary},
		} {
			if f.page != f.rpc {
				t.Errorf("%s: page=%v rpc=%v -- the Ws7gDc request may have drifted; "+
					"re-capture it from AF_dataServiceRequests['ds:5'].request",
					f.name, f.page, f.rpc)
			}
		}
		if rpc.Description == "" {
			t.Error("Description empty over the RPC path (page path has it): field selection may have drifted")
		}
		if rpc.Ratings == 0 {
			t.Error("Ratings zero over the RPC path")
		}
	})

	t.Run("PermissionsMany_matches_Permissions", func(t *testing.T) {
		ctx, cancel := canaryCtx(t)
		defer cancel()

		one, err := client.Permissions(ctx, googleplayscraper.PermissionsOptions{AppID: canaryStableApp})
		if err != nil {
			t.Fatalf("Permissions: %v", err)
		}
		if len(one) == 0 {
			t.Fatal("Permissions returned nothing; the comparison would be vacuous")
		}

		many := client.PermissionsMany(ctx, []string{canaryStableApp}, googleplayscraper.PermissionsOptions{})
		if many[0].Err != nil {
			t.Fatalf("PermissionsMany: %v", many[0].Err)
		}
		if len(many[0].Permissions) != len(one) {
			t.Fatalf("batched returned %d permissions, one-at-a-time %d",
				len(many[0].Permissions), len(one))
		}
		for i := range one {
			if one[i] != many[0].Permissions[i] {
				t.Errorf("permission %d: one=%+v many=%+v", i, one[i], many[0].Permissions[i])
			}
		}
	})

	t.Run("SuggestMany_matches_Suggest", func(t *testing.T) {
		ctx, cancel := canaryCtx(t)
		defer cancel()

		const term = "maps"
		one, err := client.Suggest(ctx, googleplayscraper.SuggestOptions{Term: term})
		if err != nil {
			t.Fatalf("Suggest: %v", err)
		}
		if len(one) == 0 {
			t.Fatal("Suggest returned nothing; the comparison would be vacuous")
		}

		many := client.SuggestMany(ctx, []string{term}, googleplayscraper.SuggestOptions{})
		if many[0].Err != nil {
			t.Fatalf("SuggestMany: %v", many[0].Err)
		}
		if len(many[0].Suggestions) != len(one) {
			t.Fatalf("batched returned %d suggestions, one-at-a-time %d",
				len(many[0].Suggestions), len(one))
		}
		for i := range one {
			if one[i] != many[0].Suggestions[i] {
				t.Errorf("suggestion %d: one=%q many=%q", i, one[i], many[0].Suggestions[i])
			}
		}
	})

	// Packing must actually pack: several apps in one request must all come
	// back, each with its own data. A regression that made batchCall fall back
	// to answering only the first call would still pass every single-app check
	// above.
	t.Run("several_apps_in_one_request", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		ids := []string{canaryStableApp, canaryGameApp, canaryReviewApp}
		results := client.AppsMany(ctx, ids, googleplayscraper.AppOptions{})
		if len(results) != len(ids) {
			t.Fatalf("got %d results for %d apps", len(results), len(ids))
		}
		seen := map[string]bool{}
		for i, r := range results {
			if r.Err != nil {
				t.Errorf("%s: %v", r.AppID, r.Err)
				continue
			}
			if r.AppID != ids[i] {
				t.Errorf("result %d is for %s, want %s", i, r.AppID, ids[i])
			}
			if r.App.AppID != ids[i] {
				t.Errorf("%s carries data for %s -- answers paired by position, not by index",
					ids[i], r.App.AppID)
			}
			if r.App.Title == "" {
				t.Errorf("%s: empty title", ids[i])
			}
			if seen[r.App.Title] {
				t.Errorf("%s: duplicate title %q -- one answer served for several calls",
					ids[i], r.App.Title)
			}
			seen[r.App.Title] = true
		}
	})
}

// canaryAvailabilityClasses pins the three outcomes the availability probe has
// to tell apart, on live data.
//
// The probe reads them from the Ws7gDc RPC rather than from the rendered page,
// which is a 64x reduction in bytes over a 177-country sweep. That is only safe
// while the RPC's three signals keep mapping onto the page's:
//
//	empty payload   <- page 404              -> StatusNotFound
//	[18] == []      <- page 200, [18] == []  -> StatusNotInRegion
//	[18][0] == 2    <- page 200, [18][0] == 2-> StatusAvailable
//
// If Google collapses two of those -- say it starts answering a missing id with
// an empty [18] instead of no payload -- a sweep would silently reclassify
// every unknown app as "not in this region". This is the test that turns that
// into a red line rather than a wrong dataset.
func canaryAvailabilityClasses(t *testing.T, client *googleplayscraper.Client) {
	cases := []struct {
		name    string
		appID   string
		country string
		want    googleplayscraper.Status
	}{
		// Available: a first-party app in its home market.
		{"available", canaryStableApp, "us", googleplayscraper.StatusAvailable},
		// Not found: an id Google has never served.
		{"missing_id", "com.qa.definitely.not.a.real.pkg.zz", "us", googleplayscraper.StatusNotFound},
		// Not in region: a Japanese title that is listed but not offered in the US.
		{"region_locked", "jp.co.mixi.monsterstrike", "us", googleplayscraper.StatusNotInRegion},
		{"region_home", "jp.co.mixi.monsterstrike", "jp", googleplayscraper.StatusAvailable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := canaryCtx(t)
			defer cancel()

			res, err := client.Availability(ctx, tc.appID, googleplayscraper.AvailabilityOptions{
				Countries: []string{tc.country},
			})
			if err != nil {
				t.Fatalf("Availability: %v", err)
			}
			got, ok := res.Statuses[tc.country]
			if !ok {
				t.Fatalf("no status for %q in %v", tc.country, res.Statuses)
			}
			if got != tc.want {
				t.Errorf("%s in %q: status %v, want %v -- the RPC's availability signals may have drifted",
					tc.appID, tc.country, got, tc.want)
			}
		})
	}
}

// canaryCollections checks that every cluster name still answers.
//
// These are undocumented identifiers found by asking the endpoint: Google
// accepts topselling_free, topselling_paid, topgrossing, topselling_new_free,
// topselling_new_paid and movers_shakers, and returns nothing at all for
// plausible-looking alternatives like new_free or topselling_trending. A
// renamed cluster would not error -- List would simply return an empty list,
// and a pipeline built on "what is new" would quietly stop seeing new apps.
func canaryCollections(t *testing.T, client *googleplayscraper.Client) {
	seen := map[googleplayscraper.Collection][]googleplayscraper.SearchResult{}
	for _, col := range []googleplayscraper.Collection{
		googleplayscraper.CollectionTopFree,
		googleplayscraper.CollectionTopPaid,
		googleplayscraper.CollectionGrossing,
		googleplayscraper.CollectionNewFree,
		googleplayscraper.CollectionNewPaid,
		googleplayscraper.CollectionMoversShakers,
	} {
		t.Run(string(col), func(t *testing.T) {
			ctx, cancel := canaryCtx(t)
			defer cancel()

			results, err := client.List(ctx, googleplayscraper.ListOptions{
				Collection: col,
				Category:   googleplayscraper.CategoryGameAction,
				Num:        50,
			})
			if err != nil {
				t.Fatalf("List(%s): %v", col, err)
			}
			if len(results) == 0 {
				t.Fatalf("%s returned no apps; the cluster name may have been retired", col)
			}
			for i, r := range results {
				if r.AppID == "" {
					t.Errorf("%s result %d has no appId", col, i)
				}
			}
			seen[col] = results
		})
	}

	// Non-empty is not enough. The legacy HTML fallback used to answer any
	// collection it did not recognise with the top-free chart and a nil error,
	// so a broken cluster name would have passed the check above while
	// returning the most popular apps under the name of the newest. Distinct
	// collections must return distinct lists.
	top, ok := seen[googleplayscraper.CollectionTopFree]
	if !ok || len(top) == 0 {
		return
	}
	inTop := make(map[string]bool, len(top))
	for _, r := range top {
		inTop[r.AppID] = true
	}
	for _, col := range []googleplayscraper.Collection{
		googleplayscraper.CollectionNewFree,
		googleplayscraper.CollectionMoversShakers,
	} {
		other := seen[col]
		if len(other) == 0 {
			continue
		}
		var shared int
		for _, r := range other {
			if inTop[r.AppID] {
				shared++
			}
		}
		if shared == len(other) {
			t.Errorf("%s returned exactly the top-free chart (%d of %d shared); "+
				"the cluster name may be unrecognised and a fallback answering in its place",
				col, shared, len(other))
		}
	}
}
