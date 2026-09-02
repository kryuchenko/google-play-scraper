// Command fixturegen downloads live Google Play responses into testdata/ so the
// offline parser tests have stable inputs. It is a development tool, not part of
// the library; run it manually to refresh fixtures:
//
//	go run ./internal/fixturegen
//
// It mirrors the exact URLs and request bodies the library's own methods issue,
// so the captured bytes parse identically to a live call.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	baseURL   = "https://play.google.com"
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	appID     = "com.google.android.apps.maps"
	// gameAppID is a free-to-play game whose listing exercises the media,
	// monetization and changelog fields that the Maps fixture lacks
	// (trailer video, in-app-purchase range, "Contains ads", recent changes).
	gameAppID = "com.king.candycrushsaga"
	// regionLockedAppID is a US-only carrier app. Fetched with gl=de it returns
	// a 200 page whose availability node [18] is empty, the signal Availability
	// classifies as StatusNotInRegion. Captured for the offline classifier test.
	regionLockedAppID = "com.vzw.hss.myverizon"
	regionLockedGL    = "de"
)

// scriptDataRegex mirrors the library's block matcher; used here only to pull a
// cluster URL out of the app page for the similar/cluster fixtures.
var scriptDataRegex = regexp.MustCompile(`AF_initDataCallback\(\{key:\s*'(ds:\d+)'.*?data:(.*?), sideChannel:`)

func main() {
	outDir := "testdata"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fail(err)
	}
	c := &http.Client{Timeout: 60 * time.Second}

	// 1. App detail page.
	appPage := get(c, fmt.Sprintf("%s/store/apps/details?id=%s&hl=en&gl=us", baseURL, appID))
	save(outDir, "app_page.html", appPage)

	// 1b. Game detail page: exercises media/monetization/changelog fields absent
	// from the Maps fixture.
	save(outDir, "app_page_game.html",
		get(c, fmt.Sprintf("%s/store/apps/details?id=%s&hl=en&gl=us", baseURL, gameAppID)))

	// 1c. Region-locked detail page: a US-only app fetched from Germany, whose
	// [18] availability node is empty. Backs the offline availability classifier
	// test and the App.Available=false case.
	save(outDir, "app_unavailable_region.html",
		get(c, fmt.Sprintf("%s/store/apps/details?id=%s&hl=en&gl=%s", baseURL, regionLockedAppID, regionLockedGL)))

	// 2. Search page.
	save(outDir, "search_page.html",
		get(c, fmt.Sprintf("%s/store/search?q=%s&hl=en&gl=us&price=0&c=apps", baseURL, url.QueryEscape("minecraft"))))

	// 3. List via vyAe2 batchexecute (GAME / TOP_FREE), matching listViaBatch.
	save(outDir, "list_vyae2.bin", listBatch(c))

	// 4. Category page (parseClusterURLs source).
	save(outDir, "category_page.html",
		get(c, fmt.Sprintf("%s/store/apps/category/GAME_ACTION?hl=en&gl=us", baseURL)))

	// 5. Cluster page: discover a cluster URL from the category page, fetch it.
	clusterURL := firstClusterURL(get(c, fmt.Sprintf("%s/store/apps/category/GAME?hl=en&gl=us", baseURL)))
	if clusterURL == "" {
		fail(fmt.Errorf("no cluster URL found on category page"))
	}
	if !strings.HasPrefix(clusterURL, "http") {
		clusterURL = baseURL + clusterURL
	}
	sep := "?"
	if strings.Contains(clusterURL, "?") {
		sep = "&"
	}
	save(outDir, "cluster_page.html", get(c, clusterURL+sep+"hl=en&gl=us"))

	// 6. Reviews batchexecute, matching buildReviewsBody (initial request).
	save(outDir, "reviews_batch.bin", reviewsBatch(c))

	// 7. Developer page (numeric dev ID; Google -> /store/apps/dev). Maps' dev.
	save(outDir, "developer_page.html",
		get(c, fmt.Sprintf("%s/store/apps/dev?id=%s&hl=en&gl=us", baseURL, url.QueryEscape("5700313618786177705"))))

	// 8. Similar page: find the "Similar" cluster on the app page, fetch it.
	simURL := similarClusterURL(appPage)
	if simURL == "" {
		fail(fmt.Errorf("no similar cluster URL found on app page"))
	}
	save(outDir, "similar_page.html", get(c, baseURL+simURL+"&gl=us&hl=en"))

	// 9. Data safety page.
	save(outDir, "datasafety_page.html",
		get(c, fmt.Sprintf("%s/store/apps/datasafety?id=%s&hl=en&gl=us", baseURL, appID)))

	// 10. Top-charts page (parseListPage / listViaHTML fallback source). Google
	// redirects /store/apps/top to /store/apps; the default client follows it,
	// matching the library's own get.
	save(outDir, "top_charts_page.html",
		get(c, fmt.Sprintf("%s/store/apps/top?hl=en&gl=us", baseURL)))

	fmt.Println("fixtures written to", outDir)
}

func listBatch(c *http.Client) []byte {
	tmpl, err := os.ReadFile("list_payload.txt")
	if err != nil {
		fail(err)
	}
	body := strings.NewReplacer(
		"__NUM__", "200",
		"__COLLECTION__", "topselling_free",
		"__CATEGORY__", "GAME",
	).Replace(string(tmpl))

	query := url.Values{
		"rpcids":       {"vyAe2"},
		"source-path":  {"/store/apps"},
		"f.sid":        {"-4178618388443751758"},
		"bl":           {"boq_playuiserver_20220612.08_p0"},
		"authuser":     {"0"},
		"soc-app":      {"121"},
		"soc-platform": {"1"},
		"soc-device":   {"1"},
		"_reqid":       {"82003"},
		"rt":           {"c"},
		"hl":           {"en"},
		"gl":           {"us"},
	}
	reqURL := fmt.Sprintf("%s/_/PlayStoreUi/data/batchexecute?%s", baseURL, query.Encode())
	return post(c, reqURL, "application/x-www-form-urlencoded;charset=UTF-8", body)
}

func reviewsBatch(c *http.Client) []byte {
	// Mirrors buildReviewsBody: initial request, SortNewest(2), count 150, no filter.
	payload := fmt.Sprintf(
		`[[["oCPfdb","[null,[2,%d,[%d],null,[null,%s]],[\"%s\",7]]",null,"generic"]]]`,
		150, 150, "null", appID,
	)
	body := "f.req=" + url.QueryEscape(payload)
	reqURL := fmt.Sprintf("%s/_/PlayStoreUi/data/batchexecute?hl=en&gl=us", baseURL)
	return post(c, reqURL, "application/x-www-form-urlencoded", body)
}

// firstClusterURL replicates parseClusterURLs: scan ds:* blocks for a section
// whose [21,1,2,4,2] is a cluster page link. JSON-decoding the block resolves
// the = escapes so the resulting URL is usable verbatim.
func firstClusterURL(body []byte) string {
	for _, data := range decodeBlocks(body) {
		sections, ok := getPath(data, 0, 1).([]any)
		if !ok {
			continue
		}
		for _, section := range sections {
			path := str(getPath(section, 21, 1, 2, 4, 2))
			if strings.Contains(path, "/store/apps/collection/cluster") {
				return path
			}
		}
	}
	return ""
}

// similarClusterURL replicates findSimilarCluster: look in ds:7/8/6 at [1,1] for
// a cluster titled "Similar" and return its [21,1,2,4,2] link.
func similarClusterURL(body []byte) string {
	blocks := decodeKeyedBlocks(body)
	for _, key := range []string{"ds:7", "ds:8", "ds:6"} {
		ds, ok := blocks[key]
		if !ok {
			continue
		}
		clusters, ok := getPath(ds, 1, 1).([]any)
		if !ok {
			continue
		}
		for _, cluster := range clusters {
			if strings.Contains(str(getPath(cluster, 21, 1, 0)), "Similar") {
				if u := str(getPath(cluster, 21, 1, 2, 4, 2)); u != "" {
					return u
				}
			}
		}
	}
	return ""
}

func decodeBlocks(body []byte) []any {
	var out []any
	for _, m := range scriptDataRegex.FindAllStringSubmatch(string(body), -1) {
		if len(m) < 3 {
			continue
		}
		var data any
		if json.Unmarshal([]byte(strings.TrimSpace(m[2])), &data) == nil {
			out = append(out, data)
		}
	}
	return out
}

func decodeKeyedBlocks(body []byte) map[string]any {
	out := make(map[string]any)
	for _, m := range scriptDataRegex.FindAllStringSubmatch(string(body), -1) {
		if len(m) < 3 {
			continue
		}
		var data any
		if json.Unmarshal([]byte(strings.TrimSpace(m[2])), &data) == nil {
			out[m[1]] = data
		}
	}
	return out
}

func getPath(data any, idx ...int) any {
	cur := data
	for _, i := range idx {
		arr, ok := cur.([]any)
		if !ok || i >= len(arr) {
			return nil
		}
		cur = arr[i]
	}
	return cur
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func get(c *http.Client, rawURL string) []byte {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		fail(err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	return do(c, req)
}

func post(c *http.Client, rawURL, contentType, body string) []byte {
	req, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(body))
	if err != nil {
		fail(err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "*/*")
	return do(c, req)
}

func do(c *http.Client, req *http.Request) []byte {
	resp, err := c.Do(req)
	if err != nil {
		fail(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		fail(fmt.Errorf("%s %s: status %d", req.Method, req.URL, resp.StatusCode))
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		fail(err)
	}
	return b
}

func save(dir, name string, data []byte) {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("  %-24s %8d bytes\n", name, len(data))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "fixturegen:", err)
	os.Exit(1)
}
