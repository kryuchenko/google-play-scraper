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

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
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
		googleplayscraper.WithThrottle(1 * time.Second),
	)
}

// canaryCtx returns a per-subtest context with a generous timeout. Multi-request
// methods (CategoryApps, ReviewsAll) get more headroom via their own contexts.
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
	t.Run("ReviewsAll", func(t *testing.T) { canaryReviewsAll(t, client) })
	t.Run("Developer", func(t *testing.T) { canaryDeveloper(t, client) })
	t.Run("Similar", func(t *testing.T) { canarySimilar(t, client) })
	t.Run("Permissions", func(t *testing.T) { canaryPermissions(t, client) })
	t.Run("DataSafety", func(t *testing.T) { canaryDataSafety(t, client) })
	t.Run("Suggest", func(t *testing.T) { canarySuggest(t, client) })
	t.Run("Categories", func(t *testing.T) { canaryCategories(t, client) })
	t.Run("CategoryApps", func(t *testing.T) { canaryCategoryApps(t, client) })
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
		if game.Released == "" {
			t.Error("App(game): Released empty — path [1][2][10][1][0] may have changed")
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
// ReviewsAll
// ---------------------------------------------------------------------------

func canaryReviewsAll(t *testing.T, client *googleplayscraper.Client) {
	// ReviewsAll paginates internally; give it a wider budget.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const want = 200
	reviews, err := client.ReviewsAll(ctx, canaryReviewApp, googleplayscraper.ReviewOptions{
		Sort:  googleplayscraper.SortNewest,
		Count: want,
	})
	if err != nil {
		t.Fatalf("ReviewsAll(%s, %d): %v", canaryReviewApp, want, err)
	}
	// A single page is 150; crossing that proves pagination is wired through.
	if len(reviews) <= 150 {
		t.Errorf("ReviewsAll: got %d reviews, want > 150 (more than one page) — NextToken pagination may have broken", len(reviews))
	}
	for i, r := range reviews {
		if r.ID == "" {
			t.Errorf("ReviewsAll: review[%d] ID empty — path [0] may have changed", i)
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
