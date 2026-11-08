// Package records is the one output contract shared by the gpscrape command
// and the gpscrape-mcp server: the records both emit for an app, a list row,
// a review, permissions, a data safety declaration and an availability check,
// built from the library's types, and the error codes both report.
//
// A filter written against one interface works on the other because both
// emit these types: `gpscrape reviews` writes one Review per line, and the
// MCP play_get_reviews tool returns the same Review objects in a page. Field
// names are snake_case and stable. Text written by third parties -- review
// text, reviewer names, developer replies, app summaries, descriptions and
// release notes -- sits in fields named untrusted_*, so whatever reads them
// knows not to follow instructions found there.
//
// The library's own types (googleplayscraper.App and the rest) stay what they
// are; these records are a projection of them for agents and pipelines, and
// `gpscrape -raw` still emits the library types in full.
package records

import (
	"html"
	"regexp"
	"slices"
	"strings"
	"time"

	gps "github.com/kryuchenko/google-play-scraper"
)

// App is an app's store page. The concise form carries what most questions
// need; detailed adds the rest that is worth reading.
type App struct {
	AppID       string  `json:"app_id"`
	Title       string  `json:"title"`
	Developer   string  `json:"developer"`
	DeveloperID string  `json:"developer_id"`
	Score       float64 `json:"score"`
	Ratings     int     `json:"ratings"`
	Installs    string  `json:"installs"`
	Free        bool    `json:"free"`
	Price       float64 `json:"price,omitempty"`
	Currency    string  `json:"currency,omitempty"`
	Genre       string  `json:"genre"`
	GenreID     string  `json:"genre_id"`
	Available   bool    `json:"installable_here"`
	Preregister bool    `json:"preregister,omitempty"`
	Version     string  `json:"version,omitempty"`
	Updated     string  `json:"updated,omitempty"`
	Summary     string  `json:"untrusted_summary,omitempty"`
	URL         string  `json:"url"`

	// detailed only
	Description      string `json:"untrusted_description,omitempty"`
	RecentChanges    string `json:"untrusted_recent_changes,omitempty"`
	Histogram        []int  `json:"histogram_1_to_5,omitempty"`
	Reviews          int    `json:"reviews,omitempty"`
	Released         string `json:"released,omitempty"`
	ContentRating    string `json:"content_rating,omitempty"`
	AndroidVersion   string `json:"android_version,omitempty"`
	AdSupported      *bool  `json:"ad_supported,omitempty"`
	OffersIAP        *bool  `json:"offers_iap,omitempty"`
	IAPRange         string `json:"iap_range,omitempty"`
	DeveloperEmail   string `json:"developer_email,omitempty"`
	DeveloperWebsite string `json:"developer_website,omitempty"`
	PrivacyPolicy    string `json:"privacy_policy,omitempty"`
}

// FromApp projects a library App. Price and currency are dropped for free
// apps, where the store's zero is not a price.
func FromApp(a *gps.App, detailed bool) App {
	d := App{
		AppID:       a.AppID,
		Title:       a.Title,
		Developer:   a.Developer,
		DeveloperID: a.DeveloperID,
		Score:       round2(a.Score),
		Ratings:     a.Ratings,
		Installs:    a.Installs,
		Free:        a.Free,
		Price:       a.Price,
		Currency:    a.Currency,
		Genre:       a.Genre,
		GenreID:     a.GenreID,
		Available:   a.Available,
		Preregister: a.Preregister,
		Version:     a.Version,
		Updated:     unixDay(a.Updated),
		Summary:     html.UnescapeString(a.Summary),
		URL:         a.URL,
	}
	if a.Free {
		d.Price, d.Currency = 0, ""
	}
	if !detailed {
		return d
	}
	d.Description = a.Description
	d.RecentChanges = a.RecentChanges
	d.Histogram = slices.Clone(a.Histogram[:])
	d.Reviews = a.Reviews
	d.Released = unixDay(a.Released)
	d.ContentRating = a.ContentRating
	d.AndroidVersion = a.AndroidVersion
	adSupported, offersIAP := a.AdSupported, a.OffersIAP
	d.AdSupported, d.OffersIAP = &adSupported, &offersIAP
	d.IAPRange = a.IAPRange
	d.DeveloperEmail = a.DeveloperEmail
	d.DeveloperWebsite = a.DeveloperWebsite
	d.PrivacyPolicy = a.PrivacyPolicy
	return d
}

// AppSummary is one row of a search, chart, similar or developer list.
type AppSummary struct {
	AppID       string  `json:"app_id"`
	Title       string  `json:"title"`
	Developer   string  `json:"developer"`
	DeveloperID string  `json:"developer_id,omitempty"`
	Score       float64 `json:"score"`
	// Free is absent when the row carries no price -- the search grid,
	// similar apps and developer pages read without full details. A false
	// "free": true there was the library's default, not the store's answer,
	// and paid apps read as free.
	Free     *bool   `json:"free,omitempty"`
	Price    float64 `json:"price,omitempty"`
	Currency string  `json:"currency,omitempty"`
}

// FromSearchResult projects one list row.
func FromSearchResult(r gps.SearchResult) AppSummary {
	s := AppSummary{
		AppID:       r.AppID,
		Title:       r.Title,
		Developer:   r.Developer,
		DeveloperID: r.DeveloperID,
		Score:       round2(r.Score),
	}
	if r.PriceKnown {
		free := r.Free
		s.Free = &free
		if !free {
			s.Price, s.Currency = r.Price, r.Currency
		}
	}
	return s
}

// Review is one user review. Lang is the review corpus it was read from:
// reviews are partitioned by language, not filtered by it, so a merged
// multi-language read needs it to tell its parts apart. Truncated is set
// when a concise view cut the text.
type Review struct {
	ID        string `json:"id"`
	Score     int    `json:"score"`
	Date      string `json:"date"`
	ThumbsUp  int    `json:"thumbs_up"`
	Version   string `json:"version,omitempty"`
	Text      string `json:"untrusted_text"`
	Truncated bool   `json:"truncated,omitempty"`
	UserName  string `json:"untrusted_user_name,omitempty"`
	ReplyDate string `json:"reply_date,omitempty"`
	ReplyText string `json:"untrusted_reply_text,omitempty"`
	Lang      string `json:"lang,omitempty"`
}

// FromReview projects a review in full. Concise views cut it with Concise.
func FromReview(r gps.Review, lang string) Review {
	o := Review{
		ID: r.ID, Score: r.Score, Date: Day(r.Date), ThumbsUp: r.ThumbsUp,
		Version: r.Version, Text: r.Text, UserName: r.UserName,
		ReplyText: r.ReplyText, Lang: lang,
	}
	if r.ReplyText != "" {
		o.ReplyDate = Day(r.ReplyDate)
	}
	return o
}

// Concise cuts the text to n characters on a word boundary, and drops the
// author and the developer reply: the shape a page of reviews takes when an
// agent asked for the gist.
func (r Review) Concise(n int) Review {
	r.UserName, r.ReplyText, r.ReplyDate = "", "", ""
	r.Text, r.Truncated = truncate(r.Text, n)
	return r
}

// Permissions maps each permission group to its permissions, which says the
// group once instead of on every row.
type Permissions struct {
	AppID       string              `json:"app_id,omitempty"`
	Permissions map[string][]string `json:"permissions"`
}

// FromPermissions groups the library's rows for app id, dropping duplicates.
func FromPermissions(id string, ps []gps.Permission) Permissions {
	out := Permissions{AppID: id, Permissions: map[string][]string{}}
	for _, p := range ps {
		if !slices.Contains(out.Permissions[p.Type], p.Permission) {
			out.Permissions[p.Type] = append(out.Permissions[p.Type], p.Permission)
		}
	}
	return out
}

// DataSafety is an app's data safety declaration, as the developer made it.
type DataSafety struct {
	AppID             string             `json:"app_id,omitempty"`
	SharedData        []DataEntry        `json:"shared_data"`
	CollectedData     []DataEntry        `json:"collected_data"`
	SecurityPractices []SecurityPractice `json:"security_practices"`
	PrivacyPolicyURL  string             `json:"privacy_policy_url,omitempty"`
}

// DataEntry is one kind of data shared or collected.
type DataEntry struct {
	Data     string `json:"data"`
	Type     string `json:"type"`
	Purpose  string `json:"purpose"`
	Optional bool   `json:"optional"`
}

// SecurityPractice is one declared practice, its note as plain text.
type SecurityPractice struct {
	Practice    string `json:"practice"`
	Description string `json:"description,omitempty"`
}

// FromDataSafety projects app id's declaration; inline links in practice
// notes are reduced to their text.
func FromDataSafety(id string, ds *gps.DataSafety) DataSafety {
	conv := func(in []gps.DataSafetyEntry) []DataEntry {
		out := make([]DataEntry, 0, len(in))
		for _, e := range in {
			out = append(out, DataEntry{Data: e.Data, Type: e.Type, Purpose: e.Purpose, Optional: e.Optional})
		}
		return out
	}
	out := DataSafety{
		AppID:             id,
		SharedData:        conv(ds.SharedData),
		CollectedData:     conv(ds.CollectedData),
		SecurityPractices: make([]SecurityPractice, 0, len(ds.SecurityPractices)),
		PrivacyPolicyURL:  ds.PrivacyPolicyURL,
	}
	for _, p := range ds.SecurityPractices {
		out.SecurityPractices = append(out.SecurityPractices, SecurityPractice{Practice: p.Practice, Description: PlainText(p.Description)})
	}
	return out
}

// Empty reports a declaration with nothing in it. That is not "collects
// nothing": a declaration of that still lists security practices or a policy
// link. Empty means no declaration, a wrong id, or a page layout the library
// cannot read.
func (d DataSafety) Empty() bool {
	return len(d.SharedData) == 0 && len(d.CollectedData) == 0 && len(d.SecurityPractices) == 0 && d.PrivacyPolicyURL == ""
}

// Availability says, per checked country, whether an app can be installed
// there. Errors holds the countries whose probe failed, with a coded message.
type Availability struct {
	AppID        string            `json:"app_id"`
	Available    []string          `json:"available"`
	RegionLocked []string          `json:"region_locked"`
	NotListed    []string          `json:"not_listed"`
	Errors       map[string]string `json:"errors,omitempty"`
	Checked      int               `json:"checked"`
	Note         string            `json:"note,omitempty"`
}

// FromAvailability sorts the library's per-country statuses into lists.
// Failed probes go to Errors as "code: message".
func FromAvailability(id string, res gps.AvailabilityResult) Availability {
	out := Availability{
		AppID:        id,
		Available:    []string{},
		RegionLocked: []string{},
		NotListed:    []string{},
		Checked:      res.Checked,
	}
	for c, st := range res.Statuses {
		switch st {
		case gps.StatusAvailable:
			out.Available = append(out.Available, c)
		case gps.StatusNotInRegion:
			out.RegionLocked = append(out.RegionLocked, c)
		case gps.StatusNotFound:
			out.NotListed = append(out.NotListed, c)
		}
	}
	for c, e := range res.Errors {
		if out.Errors == nil {
			out.Errors = map[string]string{}
		}
		out.Errors[c] = string(Classify(e)) + ": " + e.Error()
	}
	slices.Sort(out.Available)
	slices.Sort(out.RegionLocked)
	slices.Sort(out.NotListed)
	return out
}

// AppError is one failed entry of a batch: the id and a coded error.
type AppError struct {
	AppID string `json:"app_id"`
	Code  Code   `json:"code"`
	Error string `json:"error"`
}

// NewAppError builds a batch error entry for id.
func NewAppError(id string, err error) AppError {
	return AppError{AppID: id, Code: Classify(err), Error: err.Error()}
}

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// PlainText drops inline tags and decodes entities: developer-declared notes
// arrive with <a href> links that only cost tokens.
func PlainText(s string) string {
	return strings.TrimSpace(html.UnescapeString(htmlTag.ReplaceAllString(s, "")))
}

// Day formats a time as YYYY-MM-DD in UTC, or "" for the zero time.
func Day(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.DateOnly)
}

func unixDay(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.DateOnly)
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

// truncate cuts s to at most n runes on a word boundary when it can, and
// reports whether it cut anything.
func truncate(s string, n int) (string, bool) {
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	cut := string(r[:n])
	// i is a byte offset, so compare it with the byte length: comparing with
	// the rune count would cut Cyrillic or CJK text far too early.
	if i := strings.LastIndexAny(cut, " \n\t"); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " \n\t") + "…", true
}
