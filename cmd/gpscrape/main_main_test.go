package main

import (
	"os"
	"testing"
)

// The tests run full sweeps against a fake store with no terminal attached,
// which the -yes guard would refuse; they consent here once. The guard's own
// test turns it back off.
//
// With GPSCRAPE_TEST_MAIN set the binary is not running tests at all: it is
// the gpscrape process runCLI started, and main() takes it over.
func TestMain(m *testing.M) {
	if os.Getenv(envTestMain) != "" {
		runMainForTest()
	}
	assumeYes = true
	os.Exit(m.Run())
}
