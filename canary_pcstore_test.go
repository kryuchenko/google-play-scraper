//go:build canary

package googleplayscraper_test

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

const (
	// canaryPCGame has a Google Play Games on PC build, and its PC screenshots
	// are a set of their own rather than a subset of the phone ones.
	canaryPCGame = "com.mytona.cookingdiary.android"
	// canaryNotOnPC is not a game and has no PC build.
	canaryNotOnPC = "com.whatsapp"
)

func canaryPCApp(t *testing.T, client *googleplayscraper.Client) {
	t.Run("on_pc", func(t *testing.T) {
		ctx, cancel := canaryCtx(t)
		defer cancel()

		pc, err := client.PCApp(ctx, canaryPCGame, googleplayscraper.AppOptions{})
		if err != nil {
			t.Fatalf("PCApp(%s): %v", canaryPCGame, err)
		}
		if !pc.OnPC {
			t.Fatal("OnPC = false — the PC listing now redirects, or the redirect policy stops too early")
		}
		if !strings.Contains(pc.Title, "Cooking Diary") {
			t.Errorf("Title = %q — ds:4 path [1][2][0][0] may have changed", pc.Title)
		}
		if pc.Developer != "Mytona" {
			t.Errorf("Developer = %q — ds:4 path [1][2][68][0] may have changed", pc.Developer)
		}
		if pc.Icon == "" {
			t.Error("Icon empty — ds:4 path [1][2][95][0][3][2] may have changed")
		}
		if len(pc.Screenshots) == 0 {
			t.Error("Screenshots empty — ds:4 path [1][2][78][0] may have changed")
		}
		if pc.YouTubeID == "" {
			t.Errorf("YouTubeID empty from Video %q — ds:4 path [1][2][100][0][0][3][2] or the player URL changed", pc.Video)
		}
		if pc.Ratings <= 0 {
			t.Errorf("Ratings = %d — ds:4 path [1][2][51][2][1] may have changed", pc.Ratings)
		}
		if !slices.ContainsFunc(pc.Requirements, func(r googleplayscraper.PCRequirement) bool { return r.Label == "OS" }) {
			t.Errorf("Requirements = %v, want an OS line — ds:3 [2][0] may have changed", pc.Requirements)
		}

		// The point of reading this page at all. If the PC screenshots ever
		// match the phone ones, either the developer merged the two sets or
		// the page stopped asking for the PC record; the second is drift.
		app, err := client.App(ctx, canaryPCGame, googleplayscraper.AppOptions{})
		if err != nil {
			t.Fatalf("App(%s): %v", canaryPCGame, err)
		}
		if slices.ContainsFunc(pc.Screenshots, func(s string) bool { return slices.Contains(app.Screenshots, s) }) {
			t.Errorf("PC screenshots overlap the phone listing's — ds:4 may no longer be the PC record")
		}
	})

	t.Run("not_on_pc", func(t *testing.T) {
		ctx, cancel := canaryCtx(t)
		defer cancel()

		pc, err := client.PCApp(ctx, canaryNotOnPC, googleplayscraper.AppOptions{})
		if err != nil {
			t.Fatalf("PCApp(%s): %v — an app without a PC build should be OnPC=false, not an error", canaryNotOnPC, err)
		}
		if pc.OnPC || pc.Title != "" {
			t.Errorf("OnPC = %v, Title = %q — the PC store no longer redirects apps it does not list", pc.OnPC, pc.Title)
		}
	})
}

// pcRegionsHelp is where PCRegions was copied from.
const pcRegionsHelp = "https://support.google.com/googleplay/answer/11358071?hl=en"

// canaryPCRegions counts the regions Google's help page lists against the
// snapshot. It does not reach Play, so it does not use the shared client.
func canaryPCRegions(t *testing.T) {
	ctx, cancel := canaryCtx(t)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pcRegionsHelp, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", pcRegionsHelp, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", pcRegionsHelp, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	// The list is the first <ul> after this sentence, one <li> per region.
	_, rest, ok := strings.Cut(string(body), "Google Play Games on PC is available in:")
	if !ok {
		t.Fatal("the help page no longer introduces the region list the way it did")
	}
	list, _, ok := strings.Cut(rest, "</ul>")
	if !ok {
		t.Fatal("no list follows the introduction")
	}
	if got, want := strings.Count(list, "<li>"), len(googleplayscraper.PCRegions()); got != want {
		t.Errorf("the help page lists %d regions and PCRegions has %d — refresh the snapshot", got, want)
	}
}
