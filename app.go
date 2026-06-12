package googleplayscraper

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
)

// AppOptions configures the app details request
type AppOptions struct {
	Lang    string
	Country string
}

// App fetches application details
func (c *Client) App(ctx context.Context, appID string, opts AppOptions) (*App, error) {
	if appID == "" {
		return nil, fmt.Errorf("appID is required")
	}

	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}

	url := fmt.Sprintf("%s/store/apps/details?id=%s&hl=%s&gl=%s", BaseURL, appID, opts.Lang, opts.Country)

	body, err := c.get(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return parseAppPage(body, appID, url)
}

func parseAppPage(body []byte, appID, pageURL string) (*App, error) {
	dataBlocks := parseDataBlocks(body)
	return extractAppData(dataBlocks, appID, pageURL)
}

// appDataNode navigates a parsed Google Play app page to the core app-info node
// (ds:5 → [1][2]). It is the single point every availability-aware caller shares,
// so the lightweight Availability probe and the full App parser agree on where
// the app data lives. ok is false when the page has no ds:5 block or the [1][2]
// node is absent (e.g. a non-app page or a malformed response).
func appDataNode(body []byte) (appData interface{}, ok bool) {
	ds5, found := parseDataBlocks(body)["ds:5"]
	if !found {
		return nil, false
	}
	appData = getPath(ds5, 1, 2)
	return appData, appData != nil
}

// classifyAvailability interprets the multiplexed availability node at
// [18][0] of the app data into a region-level Status:
//
//   - 2   → StatusAvailable      (installable in this region)
//   - 1   → StatusNotInRegion    (pre-registration: an unreleased app, not yet
//     installable — treated as not available in the region)
//   - nil → StatusNotInRegion    (the page exists but the app is not offered in
//     this region; [18] is an empty array)
//
// It never returns StatusNotFound or StatusError: those are HTTP/transport
// outcomes decided before the body is parsed.
func classifyAvailability(appData interface{}) Status {
	if toInt(getPath(appData, 18, 0)) == 2 {
		return StatusAvailable
	}
	return StatusNotInRegion
}

func extractAppData(data map[string]interface{}, appID, url string) (*App, error) {
	app := &App{
		AppID: appID,
		URL:   url,
	}

	// Main data is in ds:5
	ds5, ok := data["ds:5"]
	if !ok {
		return nil, fmt.Errorf("main data block not found")
	}

	// Navigate: [1][2] contains app info
	appData := getPath(ds5, 1, 2)
	if appData == nil {
		return nil, fmt.Errorf("app data not found")
	}

	// Available reflects region availability, derived from the multiplexed
	// [18][0] node (==2 means installable here). It is not a hardcoded true:
	// a region-locked listing or a pre-registration entry is not available.
	app.Available = classifyAvailability(appData) == StatusAvailable

	// Title: [0][0]
	if v := getPath(appData, 0, 0); v != nil {
		app.Title = toString(v)
	}

	// Description HTML: [72][0][1]
	if v := getPath(appData, 72, 0, 1); v != nil {
		app.DescriptionHTML = toString(v)
		app.Description = stripHTML(app.DescriptionHTML)
	}

	// Summary: [73][0][1]
	if v := getPath(appData, 73, 0, 1); v != nil {
		app.Summary = toString(v)
	}

	// Installs: [13][0]
	if v := getPath(appData, 13, 0); v != nil {
		app.Installs = toString(v)
	}

	// MinInstalls: [13][1]
	if v := getPath(appData, 13, 1); v != nil {
		app.MinInstalls = toInt64(v)
	}

	// MaxInstalls: [13][2]
	if v := getPath(appData, 13, 2); v != nil {
		app.MaxInstalls = toInt64(v)
	}

	// Score: [51][0][1]
	if v := getPath(appData, 51, 0, 1); v != nil {
		app.Score = toFloat64(v)
	}

	// ScoreText: [51][0][0]
	if v := getPath(appData, 51, 0, 0); v != nil {
		app.ScoreText = toString(v)
	}

	// Ratings: [51][2][1]
	if v := getPath(appData, 51, 2, 1); v != nil {
		app.Ratings = toInt(v)
	}

	// Reviews count: [51][3][1]
	if v := getPath(appData, 51, 3, 1); v != nil {
		app.Reviews = toInt(v)
	}

	// Histogram: [51][1]
	if hist := getPath(appData, 51, 1); hist != nil {
		app.Histogram = extractHistogram(hist)
	}

	// Price: [57][0][0][0][0][1][0][0]
	if v := getPath(appData, 57, 0, 0, 0, 0, 1, 0, 0); v != nil {
		price := toFloat64(v)
		app.Price = price / 1000000
		app.Free = price == 0
	} else {
		app.Free = true
	}

	// Currency: [57][0][0][0][0][1][0][1]
	if v := getPath(appData, 57, 0, 0, 0, 0, 1, 0, 1); v != nil {
		app.Currency = toString(v)
	}

	// PriceText: [57][0][0][0][0][1][0][2]
	if v := getPath(appData, 57, 0, 0, 0, 0, 1, 0, 2); v != nil {
		app.PriceText = toString(v)
	}

	// Developer: [68][0]
	if v := getPath(appData, 68, 0); v != nil {
		app.Developer = toString(v)
	}

	// DeveloperID: [68][1][4][2]
	if v := getPath(appData, 68, 1, 4, 2); v != nil {
		app.DeveloperID = toString(v)
	}

	// DeveloperEmail: [69][1][0]
	if v := getPath(appData, 69, 1, 0); v != nil {
		app.DeveloperEmail = toString(v)
	}

	// DeveloperWebsite: [69][0][5][2]
	if v := getPath(appData, 69, 0, 5, 2); v != nil {
		app.DeveloperWebsite = toString(v)
	}

	// DeveloperAddress: [69][2][0]
	if v := getPath(appData, 69, 2, 0); v != nil {
		app.DeveloperAddress = toString(v)
	}

	// Genre: [79][0][0][0]
	if v := getPath(appData, 79, 0, 0, 0); v != nil {
		app.Genre = toString(v)
	}

	// GenreID: [79][0][0][2]
	if v := getPath(appData, 79, 0, 0, 2); v != nil {
		app.GenreID = toString(v)
	}

	// Icon: [95][0][3][2]
	if v := getPath(appData, 95, 0, 3, 2); v != nil {
		app.Icon = toString(v)
	}

	// Version: [140][0][0][0]
	if v := getPath(appData, 140, 0, 0, 0); v != nil {
		app.Version = toString(v)
	}

	// AndroidVersion: [140][1][1][0][0][1]
	if v := getPath(appData, 140, 1, 1, 0, 0, 1); v != nil {
		app.AndroidVersion = toString(v)
	}

	// ContentRating: [9][0]
	if v := getPath(appData, 9, 0); v != nil {
		app.ContentRating = toString(v)
	}

	// Released: [10][1][0] holds the epoch as a float64; mirror Updated as int64.
	if v := getPath(appData, 10, 1, 0); v != nil {
		app.Released = toInt64(v)
	}

	// Updated: [145][0][1][0]
	if v := getPath(appData, 145, 0, 1, 0); v != nil {
		app.Updated = toInt64(v)
	}

	// Screenshots
	if screenshots := getPath(appData, 78, 0); screenshots != nil {
		app.Screenshots = extractScreenshots(screenshots)
	}

	// PrivacyPolicy: [99][0][5][2]
	if v := getPath(appData, 99, 0, 5, 2); v != nil {
		app.PrivacyPolicy = toString(v)
	}

	extractMedia(app, appData)
	extractMonetization(app, appData)
	extractDistribution(app, appData)
	extractChangelog(app, appData)
	extractLegalInfo(app, appData)

	return app, nil
}

// extractMedia fills the promotional media URLs (header image, trailer video and
// its poster, and the autoplay preview clip). All are optional and absent on most
// non-game listings.
func extractMedia(app *App, appData interface{}) {
	// HeaderImage (feature graphic): [96][0][3][2]
	if v := getPath(appData, 96, 0, 3, 2); v != nil {
		app.HeaderImage = toString(v)
	}
	// Video (YouTube trailer player URL): [100][0][0][3][2]
	if v := getPath(appData, 100, 0, 0, 3, 2); v != nil {
		app.Video = toString(v)
	}
	// VideoImage (trailer poster): [100][1][0][3][2]
	if v := getPath(appData, 100, 1, 0, 3, 2); v != nil {
		app.VideoImage = toString(v)
	}
	// PreviewVideo (autoplay mp4 clip): [100][1][2][0][2]
	if v := getPath(appData, 100, 1, 2, 0, 2); v != nil {
		app.PreviewVideo = toString(v)
	}
}

// extractMonetization fills ad-support, in-app-purchase and discount fields.
func extractMonetization(app *App, appData interface{}) {
	// AdSupported: [48] is present (e.g. ["Contains ads"]) when the app shows ads.
	app.AdSupported = getPath(appData, 48) != nil

	// IAPRange: [19][0] is a human range like "$0.99 - $149.99 per item".
	// Its mere presence is what node uses to derive offersIAP.
	if v := getPath(appData, 19, 0); v != nil {
		app.IAPRange = toString(v)
		app.OffersIAP = true
	}

	// OriginalPrice (pre-discount price, micros): [57][0][0][0][0][1][1][0].
	// Present only while a promotional discount is active.
	if v := getPath(appData, 57, 0, 0, 0, 0, 1, 1, 0); v != nil {
		app.OriginalPrice = toFloat64(v) / 1000000
	}

	// DiscountEndDate (unix seconds): [57][0][0][0][0][14][0][0]. The [14][1]
	// sibling is the human-readable "Sale ends in N days" string, not the epoch.
	if v := getPath(appData, 57, 0, 0, 0, 0, 14, 0, 0); v != nil {
		app.DiscountEndDate = toInt64(v)
	}
}

// extractDistribution fills availability flags: Play Pass inclusion, pre-registration
// and early-access state.
func extractDistribution(app *App, appData interface{}) {
	// IsAvailableInPlayPass: [62] is a non-null block when the app is in Play Pass.
	app.IsAvailableInPlayPass = getPath(appData, 62) != nil

	// Preregister: [18][0] == 1 marks an unreleased, pre-registerable app.
	if v := getPath(appData, 18, 0); v != nil {
		app.Preregister = toInt(v) == 1
	}

	// EarlyAccessEnabled: [18][2] is a string when early access is offered.
	if v := getPath(appData, 18, 2); v != nil {
		if _, ok := v.(string); ok {
			app.EarlyAccessEnabled = true
		}
	}
}

// extractChangelog fills the "what's new" text and the content-rating description.
func extractChangelog(app *App, appData interface{}) {
	// RecentChanges: [144][1][1]. The raw value carries <br> tags and HTML
	// entities, so run it through stripHTML like Description.
	if v := getPath(appData, 144, 1, 1); v != nil {
		app.RecentChanges = stripHTML(toString(v))
	}
	// ContentRatingDescription: [9][2][1] (e.g. "Fantasy Violence")
	if v := getPath(appData, 9, 2, 1); v != nil {
		app.ContentRatingDescription = toString(v)
	}
}

// extractLegalInfo fills the developer's internal store ID and the EU DSA trader
// contact details. The legal fields are absent for developers outside the EU
// trader regime, which is expected and left empty.
func extractLegalInfo(app *App, appData interface{}) {
	// DeveloperInternalID: the id= query param of the developer store URL
	// at [68][1][4][2] (numeric for /store/apps/dev, a name for /developer).
	if v := getPath(appData, 68, 1, 4, 2); v != nil {
		app.DeveloperInternalID = devIDFromURL(toString(v))
	}
	// DeveloperLegalName: [69][4][0]
	if v := getPath(appData, 69, 4, 0); v != nil {
		app.DeveloperLegalName = toString(v)
	}
	// DeveloperLegalEmail: [69][4][1][0]
	if v := getPath(appData, 69, 4, 1, 0); v != nil {
		app.DeveloperLegalEmail = toString(v)
	}
	// DeveloperLegalAddress: [69][4][2][0], newlines flattened to ", " like node.
	if v := getPath(appData, 69, 4, 2, 0); v != nil {
		app.DeveloperLegalAddress = strings.ReplaceAll(toString(v), "\n", ", ")
	}
	// DeveloperLegalPhoneNumber: [69][4][3]
	if v := getPath(appData, 69, 4, 3); v != nil {
		app.DeveloperLegalPhoneNumber = toString(v)
	}
}

// devIDFromURL extracts the id= query parameter from a developer store URL,
// matching node's `devUrl.split('id=')[1]`.
func devIDFromURL(devURL string) string {
	if _, after, ok := strings.Cut(devURL, "id="); ok {
		return after
	}
	return devURL
}

func getPath(data interface{}, indices ...int) interface{} {
	current := data
	for _, idx := range indices {
		switch v := current.(type) {
		case []interface{}:
			if idx >= len(v) {
				return nil
			}
			current = v[idx]
		case map[string]interface{}:
			// Handle maps with numeric string keys (e.g., "138", "100")
			key := fmt.Sprintf("%d", idx)
			val, ok := v[key]
			if !ok {
				return nil
			}
			current = val
		default:
			return nil
		}
	}
	return current
}

func toString(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func toInt(v interface{}) int {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

func toInt64(v interface{}) int64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

func toFloat64(v interface{}) float64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return 0
}

func extractHistogram(data interface{}) [5]int {
	var hist [5]int
	arr, ok := data.([]interface{})
	if !ok {
		return hist
	}
	// The node is a 6-element array [null, 1-star, 2-star, 3-star, 4-star,
	// 5-star]; index 0 is a null placeholder. Map arr[i] -> hist[i-1] so the
	// result is ordered hist[0]=1-star .. hist[4]=5-star. Each entry is itself
	// a 2-tuple ["formatted", count]; we take the integer count at index 1.
	for i := 1; i <= 5 && i < len(arr); i++ {
		if inner, ok := arr[i].([]interface{}); ok && len(inner) > 1 {
			hist[i-1] = toInt(inner[1])
		}
	}
	return hist
}

func extractScreenshots(data interface{}) []string {
	arr, ok := data.([]interface{})
	if !ok {
		return nil
	}
	var screenshots []string
	for _, item := range arr {
		if inner, ok := item.([]interface{}); ok && len(inner) > 3 {
			if imgData, ok := inner[3].([]interface{}); ok && len(imgData) > 2 {
				if url, ok := imgData[2].(string); ok {
					screenshots = append(screenshots, url)
				}
			}
		}
	}
	return screenshots
}

var htmlTagRegex = regexp.MustCompile(`<[^>]*>`)

func stripHTML(s string) string {
	// Replace <br> with newlines
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	// Remove all other tags
	s = htmlTagRegex.ReplaceAllString(s, "")
	// Decode HTML entities (&#39; -> ', &amp; -> &, ...)
	return html.UnescapeString(s)
}
