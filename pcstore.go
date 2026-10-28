package googleplayscraper

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
)

// Google Play Games on PC.
//
// The PC store renders its own listing for a game with a Windows build, at
// /pc-store/games/details. It is built the way the details page is: its ds:4
// block is the Ws7gDc app record -- the same RPC, asked for one more field and
// with a PC device context -- so parseAppRecord reads it unchanged. What
// differs is the content. The screenshots and trailer are the ones the
// developer chose for PC, and the rating is the one PC players gave.
//
// An app without a PC build has no such page. Google answers 302 to the mobile
// listing with an empty body, and that redirect is the answer: followed, it
// costs a megabyte of a page nobody asked for, and the page at the end of it
// says nothing about PC.

// pcDetailsPath is the PC listing; mobileDetailsPath is the phone listing it
// redirects to for an app without a PC build.
const (
	pcDetailsPath     = "/pc-store/games/details"
	mobileDetailsPath = "/store/apps/details"
)

// PCApp is an app's Google Play Games on PC listing.
type PCApp struct {
	// AppID is the app package name.
	AppID string `json:"appId" example:"com.mytona.cookingdiary.android"`
	// URL is the PC listing that was asked for.
	URL string `json:"url" format:"uri" example:"https://play.google.com/pc-store/games/details?id=com.mytona.cookingdiary.android&hl=en&gl=us"`
	// OnPC is true when the PC store lists the app, which is to say it has a
	// Windows build. When it is false every field below is empty.
	OnPC bool `json:"onPC" example:"true"`
	// Title is the localized app name.
	Title string `json:"title,omitempty" example:"Cooking Diary® Restaurant Game"`
	// Developer is the developer's display name.
	Developer string `json:"developer,omitempty" example:"Mytona"`
	// Icon is the app icon URL.
	Icon string `json:"icon,omitempty" format:"uri"`
	// HeaderImage is the feature-graphic banner URL; absent on some listings.
	HeaderImage string `json:"headerImage,omitempty" format:"uri"`
	// Screenshots are the PC listing's own screenshots. Some developers upload
	// a separate set for PC; others reuse some of their phone screenshots.
	Screenshots []string `json:"screenshots,omitempty"`
	// Video is the trailer URL for this language: either Play's own player
	// (.../player/yt:movie:<id>?...) or a youtube.com watch URL. Absent when
	// the listing has no trailer.
	Video string `json:"video,omitempty" format:"uri"`
	// YouTubeID is the trailer's YouTube video id, taken from Video.
	YouTubeID string `json:"youTubeId,omitempty" example:"RJ1ranFTqDQ"`
	// PreviewVideo is the autoplay mp4 clip of the trailer; absent when the
	// trailer is a plain YouTube link.
	PreviewVideo string `json:"previewVideo,omitempty" format:"uri"`
	// Score is the average rating from PC players alone, at full precision.
	// Zero, like ScoreText and Ratings, until PC players have rated the game.
	Score float64 `json:"score,omitempty" minimum:"0" maximum:"5" example:"4.285714"`
	// ScoreText is Score rounded as displayed, e.g. "4.3".
	ScoreText string `json:"scoreText,omitempty" example:"4.3"`
	// Ratings is the number of ratings from PC players.
	Ratings int `json:"ratings,omitempty" minimum:"0" example:"7"`
	// Requirements is the listing's system requirements, localized. Every
	// listing checked on 2026-10-28 showed Google Play Games' own minimums
	// rather than anything specific to the game, but they come from a
	// per-app RPC (A6yuRe), so a game may differ.
	Requirements []PCRequirement `json:"requirements,omitempty"`
}

// PCRequirement is one line of a PC listing's system requirements, e.g.
// {Label: "Memory", Value: "8 GB of RAM"}. Lines that stand alone, such as
// "Windows admin account", have no label.
type PCRequirement struct {
	Label string `json:"label,omitempty" example:"Memory"`
	Value string `json:"value" example:"8 GB of RAM"`
}

// PCApp fetches an app's Google Play Games on PC listing.
//
// An app with no PC build is not an error: the result has OnPC false. A 404
// is, as it is for App -- Google does not know the package at all.
//
// Lang selects the language of the listing; trailers and requirement text are
// localized. Country does not say where the PC client is offered: the PC store
// renders listings for storefronts the client does not serve (ru, cu and ir
// all answered on 2026-10-28), redirects every app from cn, and answers 404
// from kp and sy. PCRegions holds Google's own list.
func (c *Client) PCApp(ctx context.Context, appID string, opts AppOptions) (*PCApp, error) {
	ctx, endTask := startTask(ctx, traceTaskPCApp)
	defer endTask()
	logTrace(ctx, "app.id", appID)

	if appID == "" {
		return nil, fmt.Errorf("appID is required")
	}
	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}

	pageURL := fmt.Sprintf("%s%s?id=%s&hl=%s&gl=%s",
		BaseURL, pcDetailsPath, appID, opts.Lang, opts.Country)

	hc, exit := c.pcStoreClient()
	body, err := c.do(ctx, hc, http.MethodGet, pageURL, func() (*http.Request, error) {
		exit.stopped.Store(false) // each attempt is judged by its own redirects
		return c.newPageRequest(ctx, pageURL)
	}, requireBlock("ds:4"))
	// The pipeline wraps a cancellation together with the status the last
	// attempt saw, so a cancel that lands after the exit must not be read as
	// "not on PC": the caller left, and the error has to say so.
	var status *StatusError
	if exit.stopped.Load() && ctx.Err() == nil && errors.As(err, &status) && status.Code/100 == 3 {
		return &PCApp{AppID: appID, URL: pageURL}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	return parsePCAppPage(body, appID, pageURL)
}

// pcStoreClient returns a copy of c.httpClient whose redirect policy is a
// pcExitPolicy wrapped around the client's own. The copy shares the
// transport, so it shares the connection pool too.
func (c *Client) pcStoreClient() (*http.Client, *pcExitPolicy) {
	hc := *c.httpClient
	policy := &pcExitPolicy{next: hc.CheckRedirect}
	hc.CheckRedirect = policy.checkRedirect
	return &hc, policy
}

// pcExitPolicy stops a PC listing request at one redirect only, the one
// isPCExit describes, and records that it did. The record is what tells the
// "not on PC" answer from every other 3xx a request can end on -- a redirect
// the caller's own policy stopped, a 302 without a Location, a 304 -- and
// those reach the caller as the StatusError they are.
//
// Every other redirect is followed as before, google.com/sorry and consent
// pages included, so soft-block detection sees them where it always has.
type pcExitPolicy struct {
	next    func(req *http.Request, via []*http.Request) error
	stopped atomic.Bool
}

func (p *pcExitPolicy) checkRedirect(req *http.Request, via []*http.Request) error {
	// The caller's policy, or net/http's limit, has the first word: a chain it
	// would refuse is refused here too, whatever the last hop is, and a hop it
	// stops itself is its answer rather than ours.
	if err := p.allowed(req, via); err != nil {
		return err
	}
	if isPCExit(via[len(via)-1].URL, req.URL) {
		p.stopped.Store(true)
		return http.ErrUseLastResponse
	}
	return nil
}

// allowed applies the caller's CheckRedirect, or the limit net/http applies
// when there is none.
func (p *pcExitPolicy) allowed(req *http.Request, via []*http.Request) error {
	if p.next != nil {
		return p.next(req, via)
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

// isPCExit reports whether a redirect is the PC store's answer for an app
// without a PC build: from the PC listing of a package to the phone listing
// of the same package, on the same host. That is the only shape observed.
// from is the request being redirected, not the first one in the chain.
func isPCExit(from, to *url.URL) bool {
	id := from.Query().Get("id")
	return id != "" &&
		strings.EqualFold(from.Hostname(), to.Hostname()) &&
		from.Path == pcDetailsPath &&
		to.Path == mobileDetailsPath &&
		to.Query().Get("id") == id
}

// parsePCAppPage reads a PC listing. The app record is ds:4; the system
// requirements are ds:3, the A6yuRe response, and a page without them still
// parses.
func parsePCAppPage(body []byte, appID, pageURL string) (*PCApp, error) {
	ds4, ok := dataBlock(body, "ds:4")
	if !ok {
		return nil, fmt.Errorf("PC listing block not found")
	}
	rec, err := parseAppRecord(ds4, appID, pageURL)
	if err != nil {
		return nil, err
	}

	pc := &PCApp{
		AppID:        appID,
		URL:          pageURL,
		OnPC:         true,
		Title:        rec.Title,
		Developer:    rec.Developer,
		Icon:         rec.Icon,
		HeaderImage:  rec.HeaderImage,
		Screenshots:  rec.Screenshots,
		Video:        rec.Video,
		YouTubeID:    youTubeID(rec.Video),
		PreviewVideo: rec.PreviewVideo,
		Score:        rec.Score,
		ScoreText:    rec.ScoreText,
		Ratings:      rec.Ratings,
	}
	if ds3, ok := dataBlock(body, "ds:3"); ok {
		pc.Requirements = parsePCRequirements(ds3)
	}
	return pc, nil
}

// parsePCRequirements reads ds:3, whose [2][0] is a list of [value, label]
// pairs; the label is missing on lines that stand alone.
func parsePCRequirements(ds3 any) []PCRequirement {
	lines, _ := getPath(ds3, 2, 0).([]any)
	var out []PCRequirement
	for _, line := range lines {
		value, _ := getPath(line, 0).(string)
		if value == "" {
			continue
		}
		label, _ := getPath(line, 1).(string)
		out = append(out, PCRequirement{Label: label, Value: value})
	}
	return out
}

// youTubeID returns the video id a trailer URL names, in either form the
// listings use: Play's player (.../player/yt:movie:<id>?autoplay=1) or a
// youtube.com watch URL (?v=<id>). Anything else yields "".
func youTubeID(videoURL string) string {
	u, err := url.Parse(videoURL)
	if err != nil {
		return ""
	}
	if host := u.Hostname(); host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") {
		return u.Query().Get("v")
	}
	if _, id, ok := strings.Cut(u.Path, "yt:movie:"); ok {
		return id
	}
	return ""
}
