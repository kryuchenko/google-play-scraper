// Package lightfeed provides a browser-driven deep paginator for the Google
// Play category/cluster feed, implementing googleplayscraper.FeedPaginator.
//
// It exists as a SEPARATE Go module so that the root google-play-scraper package
// remains zero-dependency: only callers who opt into FeedBrowser pull in the
// browser driver (chromedp) and run a headless browser.
//
// # How it works
//
// The Play Store loads only the first ~18 apps of a category page into the
// initial HTML; the rest stream in as the user scrolls (an infinite feed backed
// by the private qnKhOb RPC). The root package's FeedLightweight mode replays
// that RPC statelessly and reaches ~77 apps on GAME_ACTION. To go deeper, this
// package drives a real headless browser — Lightpanda — to actually scroll the
// page and read every app link the live feed renders (~149 on GAME_ACTION).
//
// Lightpanda (https://lightpanda.io) is a ~64 MB static headless browser
// (V8 + NetSurf, no Chromium) that speaks a subset of the Chrome DevTools
// Protocol. We connect to it over CDP using chromedp's RemoteAllocator.
//
// # Getting the browser binary
//
// Download a nightly build for your platform, e.g. Apple Silicon macOS:
//
//	curl -sL -o lightpanda \
//	  https://github.com/lightpanda-io/browser/releases/download/nightly/lightpanda-aarch64-macos
//	chmod +x lightpanda
//	./lightpanda version
//
// Builds for linux-x86_64 and linux-aarch64 are published under the same release
// tag.
//
// # Usage
//
// Two wiring modes are supported. Autostart manages the browser process for you:
//
//	p, err := lightfeed.New(lightfeed.WithLightpandaPath("./lightpanda"))
//	if err != nil { ... }
//	defer p.Close()
//
//	res, err := client.Cluster(ctx, googleplayscraper.ClusterOptions{
//	    Path:          "/store/apps/category/GAME_ACTION",
//	    FeedMode:      googleplayscraper.FeedBrowser,
//	    FeedPaginator: p,
//	})
//
// Or connect to a browser you run yourself
// (lightpanda serve --host 127.0.0.1 --port 9222):
//
//	p, err := lightfeed.New(lightfeed.WithCDPEndpoint("ws://127.0.0.1:9222"))
//
// # Responsible use and disclaimer
//
// This package automates the same private endpoints a browser hits. It is NOT
// affiliated with or endorsed by Google. The qnKhOb feed is an undocumented
// internal API and may change or break at any time. Scrape responsibly: keep the
// default throttle (or raise it), cap your scroll rounds, and respect Google's
// Terms of Service and any applicable rate limits. You are responsible for how
// you use it.
package lightfeed
