package main

import (
	"net/http"
	"os"
	"strings"
	"testing"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

// The tests run full sweeps against a fake store with no terminal attached,
// which the -yes guard would refuse; they consent here once. The guard's own
// test turns it back off.
//
// Two child modes start the test binary as gpscrape itself. With
// GPSCRAPE_TEST_MAIN set it is the process cli_harness_test.go's runCLI
// started, against the fixture client. With asMainEnv set it runs main()
// against the store at storeEnv; that is how codes_test.go checks what a
// process reports and exits with, which no in-process call can see.
func TestMain(m *testing.M) {
	if os.Getenv(envTestMain) != "" {
		runMainForTest()
		return
	}
	if os.Getenv(asMainEnv) == "1" {
		runAsMain()
		return
	}
	assumeYes = true
	os.Exit(m.Run())
}

const (
	asMainEnv = "GPSCRAPE_TEST_AS_MAIN"
	storeEnv  = "GPSCRAPE_TEST_STORE"
)

// runAsMain is the child half of runStoreCLI. No retries and no throttle, like
// useFakeClient: the store under test answers once, the way it was told to.
func runAsMain() {
	host := strings.TrimPrefix(os.Getenv(storeEnv), "http://")
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		r2 := req.Clone(req.Context())
		r2.URL.Scheme, r2.URL.Host, r2.Host = "http", host, host
		return http.DefaultTransport.RoundTrip(r2)
	})
	newClientHook = func(c *common) *googleplayscraper.Client {
		return googleplayscraper.NewClient(
			googleplayscraper.WithHTTPClient(&http.Client{Transport: rt}),
			googleplayscraper.WithConcurrency(c.concurrency))
	}
	// A command that is a bug, for the one code nothing else can produce.
	commands["test-panic"] = func([]string) error { panic("deliberate test panic") }
	main()
	os.Exit(0)
}
