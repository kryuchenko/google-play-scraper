//go:build killtest

// Test-only seams for the kill-and-resume test (killresume_test.go), compiled
// in only with -tags killtest; no released binary has them.
//
// The test runs gpscrape as a real process so it can SIGKILL it, and a
// process cannot be handed the in-process newClientHook the other catalog
// tests use. So:
//
//   - GPSCRAPE_TEST_ORIGIN, an http://host:port, receives every request the
//     library builds for play.google.com, the same rewrite fakeStore.transport
//     does in-process;
//   - GPSCRAPE_TEST_STOP_AT names a finish step; reaching it the process
//     writes GPSCRAPE_TEST_STOP_FILE and waits to be killed, so a kill can
//     land exactly between two of finish's writes, which no request marks.
package main

import (
	"net/http"
	"net/url"
	"os"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

func init() {
	if origin := os.Getenv("GPSCRAPE_TEST_ORIGIN"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			panic(err)
		}
		// A pool as wide as the concurrency: DefaultTransport keeps two idle
		// connections per host, so a sweep at -concurrency 4 closes the rest
		// after each request, and the test's dozens of runs exhaust the
		// local ports in TIME_WAIT.
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConnsPerHost = 64
		rt := roundTripper(func(req *http.Request) (*http.Response, error) {
			r2 := req.Clone(req.Context())
			r2.URL.Scheme, r2.URL.Host, r2.Host = u.Scheme, u.Host, u.Host
			return transport.RoundTrip(r2)
		})
		newClientHook = func(c *common) *googleplayscraper.Client {
			return googleplayscraper.NewClient(
				googleplayscraper.WithHTTPClient(&http.Client{Transport: rt}),
				googleplayscraper.WithConcurrency(c.concurrency))
		}
	}
	if stop := os.Getenv("GPSCRAPE_TEST_STOP_AT"); stop != "" {
		finishStep = func(step string) {
			if step != stop {
				return
			}
			_ = os.WriteFile(os.Getenv("GPSCRAPE_TEST_STOP_FILE"), []byte(step), 0o644)
			for {
				time.Sleep(time.Hour)
			}
		}
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
