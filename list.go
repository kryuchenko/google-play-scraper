package googleplayscraper

import (
	"context"
	_ "embed"
	"fmt"
	"net/url"
	"strings"
)

// listPayloadTemplate is the URL-encoded "f.req" body for the vyAe2 RPC,
// ported verbatim from the reference node implementation. It contains the
// __NUM__, __COLLECTION__ and __CATEGORY__ placeholders that List substitutes
// per request.
//
//go:embed list_payload.txt
var listPayloadTemplate string

// Age represents age rating filter for app lists
type Age string

const (
	AgeAll  Age = ""           // All ages (default)
	AgeFive Age = "AGE_RANGE1" // Ages 5 and under
	AgeSix  Age = "AGE_RANGE2" // Ages 6-8
	AgeNine Age = "AGE_RANGE3" // Ages 9-12
)

// listMaxNum is the largest number of apps the vyAe2 RPC will return for a
// single collection. Google honours smaller values but caps the result set
// here regardless of the requested count.
const listMaxNum = 660

// ListOptions configures the app list request
type ListOptions struct {
	Collection Collection // TOP_FREE, TOP_PAID, GROSSING
	Category   Category   // APPLICATION, GAME, etc.
	// Age filters the list by age rating. It is currently a no-op on the
	// primary (vyAe2 batchexecute) path: the endpoint reads filters from the
	// request body, not the URL, and ignores the "age" query parameter. The
	// parameter is still sent (matching the reference node implementation) and
	// is honoured only by the legacy HTML fallback. Verified empirically:
	// FAMILY and GAME lists are identical with and without Age set.
	Age     Age
	Lang    string
	Country string
	// Num caps the number of apps returned. Default 500. Values above 660 are
	// clamped to 660 (listMaxNum), the most the vyAe2 RPC will accept; in
	// practice Google returns at most ~200 apps per collection regardless.
	Num        int
	FullDetail bool // Fetch full details for each app
}

// List fetches a ranked list of apps for a collection within a category.
//
// It uses the vyAe2 batchexecute RPC, matching the reference implementation.
// If that request fails or returns no apps, it falls back to scraping the
// rendered top-charts HTML page.
func (c *Client) List(ctx context.Context, opts ListOptions) ([]SearchResult, error) {
	ctx, endTask := startTask(ctx, traceTaskList)
	defer endTask()

	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}
	if opts.Num <= 0 {
		opts.Num = 500
	}
	if opts.Num > listMaxNum {
		opts.Num = listMaxNum
	}
	if opts.Collection == "" {
		opts.Collection = CollectionTopFree
	}
	if opts.Category == "" {
		opts.Category = CategoryApplication
	}

	cluster, ok := clusterNames[opts.Collection]
	if !ok {
		return nil, fmt.Errorf("unknown collection: %s", opts.Collection)
	}

	results, err := c.listViaBatch(ctx, cluster, opts)
	if err != nil || len(results) == 0 {
		// Fall back to the legacy HTML path if the RPC is unavailable.
		fallback, fbErr := c.listViaHTML(ctx, opts)
		switch {
		case fbErr == nil && len(fallback) > 0:
			results = fallback
		case err != nil:
			return nil, err
		case fbErr != nil:
			// The RPC answered with nothing and the fallback cannot serve this
			// collection at all. Returning (nil, nil) made "the newer clusters
			// have no HTML section" indistinguishable from "the store listed
			// nothing", silently and with exit 0.
			return nil, fmt.Errorf("%s: the batch listing was empty and the HTML fallback could not serve it: %w",
				opts.Collection, fbErr)
		}
	}

	if len(results) > opts.Num {
		results = results[:opts.Num]
	}

	if opts.FullDetail {
		return c.enrichSearchResults(ctx, results, opts.Lang, opts.Country)
	}
	return results, nil
}

// listViaBatch performs the vyAe2 batchexecute request and parses its apps.
func (c *Client) listViaBatch(ctx context.Context, cluster string, opts ListOptions) ([]SearchResult, error) {
	body := strings.NewReplacer(
		"__NUM__", fmt.Sprintf("%d", opts.Num),
		"__COLLECTION__", cluster,
		"__CATEGORY__", string(opts.Category),
	).Replace(listPayloadTemplate)

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
		"hl":           {opts.Lang},
		"gl":           {opts.Country},
	}
	if opts.Age != "" {
		query.Set("age", string(opts.Age))
	}

	reqURL := fmt.Sprintf("%s/_/PlayStoreUi/data/batchexecute?%s", BaseURL, query.Encode())
	respBody, err := c.post(ctx, reqURL, "application/x-www-form-urlencoded;charset=UTF-8", body)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	data, err := decodeBatchEnvelope(respBody)
	if err != nil {
		return nil, err
	}

	apps := getPath(data, 0, 1, 0, 28, 0)
	appsArr, ok := apps.([]any)
	if !ok {
		return nil, nil
	}

	results := make([]SearchResult, 0, len(appsArr))
	for _, app := range appsArr {
		if r := parseClusterListApp(app); r.AppID != "" {
			results = append(results, r)
		}
	}
	return results, nil
}

// clusterListAppPaths is the vyAe2 list-RPC row layout. Every field hangs off an
// extra [0] wrapper compared with the top-charts HTML layout (listAppPaths).
var clusterListAppPaths = rowPaths{
	appID:     [][]int{{0, 0, 0}},
	title:     [][]int{{0, 3}},
	icon:      [][]int{{0, 1, 3, 2}},
	developer: [][]int{{0, 14}},
	summary:   [][]int{{0, 13, 1}},
	score:     [][]int{{0, 4, 1}},
	scoreText: [][]int{{0, 4, 0}},
	currency:  [][]int{{0, 8, 1, 0, 1}},
	price:     [][]int{{0, 8, 1, 0, 0}},
	urlPath:   [][]int{{0, 10, 4, 2}},
}

// parseClusterListApp maps a single app entry from the vyAe2 response.
// Index paths mirror the reference appsMappings.
func parseClusterListApp(item any) SearchResult {
	return decodeResultRow(item, clusterListAppPaths)
}

// listViaHTML is the legacy fallback that scrapes the rendered top-charts page.
func (c *Client) listViaHTML(ctx context.Context, opts ListOptions) ([]SearchResult, error) {
	// Refuse before fetching. The page lays out the three original charts in a
	// fixed order and says nothing about the others, so this path cannot serve
	// them at all -- and spending a request to discover that would be worse
	// than useless, because the caller only reaches here when the RPC already
	// failed.
	//
	// A switch with an implicit default answered every unknown collection with
	// section 0, the top-free chart. Since List falls back here whenever the
	// RPC merely returns nothing, one transient failure on "what is new"
	// returned the most popular apps with a nil error.
	if _, ok := htmlSections[opts.Collection]; !ok && opts.Collection != "" {
		return nil, fmt.Errorf("collection %s has no section in the legacy HTML page", opts.Collection)
	}

	var reqURL string
	if opts.Category == CategoryApplication || opts.Category == CategoryGame {
		reqURL = fmt.Sprintf("%s/store/apps/top?hl=%s&gl=%s", BaseURL, opts.Lang, opts.Country)
	} else {
		reqURL = fmt.Sprintf("%s/store/apps/category/%s?hl=%s&gl=%s", BaseURL, opts.Category, opts.Lang, opts.Country)
	}
	if opts.Age != "" {
		reqURL += "&age=" + string(opts.Age)
	}

	body, err := c.get(ctx, reqURL)
	if err != nil {
		return nil, err
	}
	return parseListPage(body, opts)
}

func parseListPage(body []byte, opts ListOptions) ([]SearchResult, error) {
	// The zero Collection means "unspecified", and List's documented default
	// is the top-free chart -- so honour that here rather than refusing a
	// caller who simply did not set the field.
	collection := opts.Collection
	if collection == "" {
		collection = CollectionTopFree
	}

	// A collection that was named but has no section is a different matter,
	// and must not silently read section 0. That fallthrough is how one
	// transient RPC failure on "what is new" came back as the most popular
	// apps with a nil error.
	sectionIndex, known := htmlSections[collection]
	if !known {
		return nil, fmt.Errorf("collection %s has no section in the legacy HTML page", collection)
	}

	// Apps are in ds:4[0][1][x][21][0]
	ds4, ok := dataBlock(body, "ds:4")
	if !ok {
		return nil, nil
	}

	sections := getPath(ds4, 0, 1)
	if sections == nil {
		return nil, nil
	}

	sectionsArr, ok := sections.([]any)
	if !ok {
		return nil, nil
	}

	var results []SearchResult

	// Try to get apps from the target section
	if sectionIndex < len(sectionsArr) {
		apps := getPath(sectionsArr[sectionIndex], 21, 0)
		if apps != nil {
			appsArr, ok := apps.([]any)
			if ok {
				for _, app := range appsArr {
					result := parseListApp(app)
					if result.AppID != "" {
						results = append(results, result)
					}
					if len(results) >= opts.Num {
						break
					}
				}
			}
		}
	}

	// If no results from target section, try all sections
	if len(results) == 0 {
		for _, section := range sectionsArr {
			apps := getPath(section, 21, 0)
			if apps == nil {
				continue
			}
			appsArr, ok := apps.([]any)
			if !ok {
				continue
			}
			for _, app := range appsArr {
				result := parseListApp(app)
				if result.AppID != "" {
					results = append(results, result)
				}
				if len(results) >= opts.Num {
					break
				}
			}
			if len(results) >= opts.Num {
				break
			}
		}
	}

	return results, nil
}

// listAppPaths is the top-charts HTML row layout (the listViaHTML fallback). It
// is the same shape as clusterListAppPaths without the leading [0] wrapper.
//
// Price lives under [8] as a tuple at [8][1][0][{0,1}]. getPath returns nil for
// out-of-range indices, so the candidate paths reproduce the old len(priceArr)>1
// guard exactly: a short or absent [8] node resolves to nil, leaving the row
// free with no currency — identical to the previous explicit branching.
var listAppPaths = rowPaths{
	appID:     [][]int{{0, 0}},
	title:     [][]int{{3}},
	icon:      [][]int{{1, 3, 2}},
	developer: [][]int{{14}},
	score:     [][]int{{4, 1}},
	scoreText: [][]int{{4, 0}},
	currency:  [][]int{{8, 1, 0, 1}},
	price:     [][]int{{8, 1, 0, 0}},
}

func parseListApp(item any) SearchResult {
	return decodeResultRow(item, listAppPaths)
}
