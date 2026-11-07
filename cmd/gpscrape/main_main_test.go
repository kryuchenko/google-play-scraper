package main

import (
	"os"
	"testing"
)

// The tests run full sweeps against a fake store with no terminal attached,
// which the -yes guard would refuse; they consent here once. The guard's own
// test turns it back off.
func TestMain(m *testing.M) {
	assumeYes = true
	os.Exit(m.Run())
}
