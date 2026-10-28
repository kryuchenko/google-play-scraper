//go:build canary

package googleplayscraper_test

import (
	"html"
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

// pcRegionNames maps each name the help page lists to the code PCRegions
// holds for it. Most are plain ISO 3166-1 names; the ones that are not are
// marked. A name missing here fails the canary, so a new region cannot pass
// unnoticed.
var pcRegionNames = map[string]string{
	"Albania":                "al",
	"Algeria":                "dz",
	"Angola":                 "ao",
	"Antigua and Barbuda":    "ag",
	"Argentina":              "ar",
	"Armenia":                "am",
	"Aruba":                  "aw",
	"Australia":              "au",
	"Austria":                "at",
	"Azerbaijan":             "az",
	"Bahamas":                "bs",
	"Bahrain":                "bh",
	"Bangladesh":             "bd",
	"Belgium":                "be",
	"Belize":                 "bz",
	"Benin":                  "bj",
	"Bolivia":                "bo",
	"Bosnia and Herzegovina": "ba",
	"Botswana":               "bw",
	"Brazil":                 "br",
	"Bulgaria":               "bg",
	"Burkina Faso":           "bf",
	"Cambodia":               "kh",
	"Cameroon":               "cm",
	"Canada":                 "ca",
	"Cape Verde":             "cv",
	"Chile":                  "cl",
	"Colombia":               "co",
	"Costa Rica":             "cr",
	// sic: Côte d'Ivoire
	"Côte d'Ivore":       "ci",
	"Croatia":            "hr",
	"Cyprus":             "cy",
	"Czech Republic":     "cz",
	"Denmark":            "dk",
	"Dominican Republic": "do",
	"Ecuador":            "ec",
	"Egypt":              "eg",
	"El Salvador":        "sv",
	"Estonia":            "ee",
	"Fiji":               "fj",
	"Finland":            "fi",
	"France":             "fr",
	"Gabon":              "ga",
	"Georgia":            "ge",
	"Germany":            "de",
	"Ghana":              "gh",
	"Gibraltar":          "gi",
	"Greece":             "gr",
	"Guatemala":          "gt",
	"Guinea-Bissau":      "gw",
	"Haiti":              "ht",
	"Honduras":           "hn",
	"Hong Kong":          "hk",
	"Hungary":            "hu",
	"Iceland":            "is",
	"India":              "in",
	"Indonesia":          "id",
	"Iraq":               "iq",
	"Ireland":            "ie",
	"Israel":             "il",
	"Italy":              "it",
	"Jamaica":            "jm",
	"Japan":              "jp",
	"Jordan":             "jo",
	"Kazakhstan":         "kz",
	"Kenya":              "ke",
	// South Korea
	"Korea":         "kr",
	"Kuwait":        "kw",
	"Kyrgyzstan":    "kg",
	"Laos":          "la",
	"Latvia":        "lv",
	"Lebanon":       "lb",
	"Liechtenstein": "li",
	"Lithuania":     "lt",
	"Luxembourg":    "lu",
	"Macau":         "mo",
	// North Macedonia
	"Macedonia":       "mk",
	"Malaysia":        "my",
	"Mali":            "ml",
	"Malta":           "mt",
	"Mauritius":       "mu",
	"Mexico":          "mx",
	"Moldova":         "md",
	"Monaco":          "mc",
	"Morocco":         "ma",
	"Mozambique":      "mz",
	"Myanmar (Burma)": "mm",
	"Namibia":         "na",
	"Nepal":           "np",
	// The Antilles were dissolved in 2010 and the page does not say which
	// successor (bq, cw, sx) it means, so the entry stands for nl alone.
	"Netherlands + Netherlands Antilles": "nl",
	"New Zealand":                        "nz",
	"Nicaragua":                          "ni",
	"Niger":                              "ne",
	"Nigeria":                            "ng",
	"Norway":                             "no",
	"Oman":                               "om",
	"Pakistan":                           "pk",
	"Panama":                             "pa",
	"Papua New Guinea":                   "pg",
	"Paraguay":                           "py",
	"Peru":                               "pe",
	"Philippines":                        "ph",
	"Poland":                             "pl",
	"Portugal":                           "pt",
	"Qatar":                              "qa",
	"Romania":                            "ro",
	"Rwanda":                             "rw",
	"San Marino":                         "sm",
	"Saudi Arabia":                       "sa",
	"Senegal":                            "sn",
	"Serbia":                             "rs",
	"Sierra Leone":                       "sl",
	"Singapore":                          "sg",
	"Slovakia":                           "sk",
	"Slovenia":                           "si",
	"South Africa":                       "za",
	"Spain":                              "es",
	"Sri Lanka":                          "lk",
	"Sweden":                             "se",
	"Switzerland":                        "ch",
	"Taiwan":                             "tw",
	"Tajikistan":                         "tj",
	"Tanzania":                           "tz",
	"Thailand":                           "th",
	"Togo":                               "tg",
	"Trinidad and Tobago":                "tt",
	"Tunisia":                            "tn",
	"Turkey":                             "tr",
	"Turkmenistan":                       "tm",
	"Uganda":                             "ug",
	"Ukraine":                            "ua",
	"United Arab Emirates":               "ae",
	"United Kingdom":                     "gb",
	"United States":                      "us",
	"Uruguay":                            "uy",
	"Uzbekistan":                         "uz",
	"Venezuela":                          "ve",
	"Vietnam":                            "vn",
	"Yemen":                              "ye",
	"Zambia":                             "zm",
	"Zimbabwe":                           "zw",
}

// canaryPCRegions compares the regions Google's help page lists with the
// snapshot, by code rather than by count: a region dropped and another added
// leaves the count where it was. It does not reach Play, so it does not use
// the shared client.
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
	live := map[string]bool{}
	for _, item := range strings.Split(list, "<li>")[1:] {
		name, _, _ := strings.Cut(item, "</li>")
		name = strings.Join(strings.Fields(html.UnescapeString(name)), " ")
		code, ok := pcRegionNames[name]
		if !ok {
			t.Errorf("the help page lists %q, which pcRegionNames does not map — a new region, or a renamed one", name)
			continue
		}
		live[code] = true
	}

	snapshot := googleplayscraper.PCRegions()
	for _, code := range snapshot {
		if !live[code] {
			t.Errorf("%q is in PCRegions but no longer on the help page — remove it from the snapshot", code)
		}
	}
	for code := range live {
		if !slices.Contains(snapshot, code) {
			t.Errorf("the help page lists %q and PCRegions does not — add it to the snapshot", code)
		}
	}
}
