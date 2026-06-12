package googleplayscraper

import (
	"os/exec"
	"strings"
	"testing"
)

// TestRootIsZeroDependency is the guard for the project's defining invariant:
// the root module must import nothing outside the standard library. The
// browser-driven feed (chromedp/Lightpanda) lives in the lightfeed submodule
// precisely so this stays true; a stray import there would regress it silently.
//
// It asserts two independent facts:
//
//  1. `go list -m all` reports only this module — no third-party requires have
//     leaked into go.mod/go.sum.
//  2. `go list -deps .` (the transitive import set of the root package) contains
//     no non-stdlib path, in particular nothing from chromedp or lightfeed.
func TestRootIsZeroDependency(t *testing.T) {
	t.Run("no third-party modules", func(t *testing.T) {
		out := goList(t, "-m", "all")
		mods := nonEmptyLines(out)
		if len(mods) != 1 {
			t.Fatalf("`go list -m all` returned %d modules, want exactly 1 (this module):\n%s",
				len(mods), out)
		}
		if got := mods[0]; got != rootModulePath {
			t.Errorf("`go list -m all` head = %q, want %q", got, rootModulePath)
		}
	})

	t.Run("no third-party imports", func(t *testing.T) {
		out := goList(t, "-deps", ".")
		for _, dep := range nonEmptyLines(out) {
			// This module's own packages are expected; everything else must be
			// stdlib.
			if dep == rootModulePath || strings.HasPrefix(dep, rootModulePath+"/") {
				continue
			}
			if strings.Contains(dep, "chromedp") || strings.Contains(dep, "lightfeed") {
				t.Errorf("root package transitively imports %q; the browser feed must stay isolated in lightfeed/", dep)
			}
			// A non-stdlib import path always contains a dotted domain in its
			// first segment (e.g. "github.com/..."); stdlib paths never do.
			if first, _, _ := strings.Cut(dep, "/"); strings.Contains(first, ".") {
				t.Errorf("root package imports non-stdlib path %q; the root module must be dependency-free", dep)
			}
		}
	})
}

const rootModulePath = "github.com/kryuchenko/google-play-scraper"

func goList(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("go", append([]string{"list"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("go list %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func nonEmptyLines(s string) []string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
