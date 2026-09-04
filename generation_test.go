package googleplayscraper

import (
	"slices"
	"testing"
	"time"
)

const realShardURL = "https://play.google.com/sitemaps/play_sitemaps_2026-08-23_1787500934-00000-of-83445.xml.gz"

func TestParseGeneration(t *testing.T) {
	g, err := ParseGeneration(realShardURL)
	if err != nil {
		t.Fatal(err)
	}
	if g.Date != "2026-08-23" || g.Run != "1787500934" || g.Shards != 83445 {
		t.Errorf("got %+v", g)
	}
	if g.ID() != "2026-08-23_1787500934" {
		t.Errorf("ID = %q", g.ID())
	}

	for _, bad := range []string{
		"", "https://play.google.com/robots.txt",
		"https://play.google.com/sitemaps/sitemaps-index-0.xml",
		"play_sitemaps_2026-08-23-00000-of-83445.xml.gz", // no run
	} {
		if _, err := ParseGeneration(bad); err == nil {
			t.Errorf("parsed %q as a shard URL", bad)
		}
	}
}

// A shard list read while Google is republishing is half one build and half
// another. Sweeping that yields a catalog that never existed at any moment.
func TestGenerationOfRejectsAMixedList(t *testing.T) {
	same := []string{
		"…/play_sitemaps_2026-08-23_1787500934-00000-of-83445.xml.gz",
		"…/play_sitemaps_2026-08-23_1787500934-00001-of-83445.xml.gz",
	}
	if _, err := GenerationOf(same); err != nil {
		t.Errorf("a consistent list was rejected: %v", err)
	}

	mixed := append(slices.Clone(same),
		"…/play_sitemaps_2026-08-30_1788100000-00002-of-83500.xml.gz")
	g, err := GenerationOf(mixed)
	if err == nil {
		t.Fatalf("a list spanning two generations was accepted as %s", g)
	}

	if _, err := GenerationOf(nil); err == nil {
		t.Error("an empty list was accepted")
	}
}

func TestGenerationCompare(t *testing.T) {
	a := Generation{Date: "2026-08-23", Run: "1787500934"}
	newer := Generation{Date: "2026-08-30", Run: "1788100000"}

	if a.Compare(a) != 0 {
		t.Error("a generation is not equal to itself")
	}
	if a.Compare(newer) >= 0 || newer.Compare(a) <= 0 {
		t.Error("ordering by date is wrong")
	}

	// Run ids are compared as numbers. They grow without padding, so a
	// lexicographic comparison puts a shorter, larger-valued run first.
	short := Generation{Date: "2026-08-23", Run: "999999999"}
	long := Generation{Date: "2026-08-23", Run: "1787500934"}
	if short.Compare(long) >= 0 {
		t.Errorf("run %s sorted after %s; the comparison is lexicographic", short.Run, long.Run)
	}

	// Sorting oldest-first is the point of returning an int.
	gens := []Generation{newer, a, short}
	slices.SortFunc(gens, Generation.Compare)
	if gens[len(gens)-1].ID() != newer.ID() {
		t.Errorf("sorted order puts %s last, want %s", gens[len(gens)-1], newer)
	}
}

// The run id has been a Unix timestamp so far, but that is an observation
// about an undocumented scheme. A run that is not a plausible timestamp must
// say so rather than becoming a moment in 1970.
func TestGenerationBuilt(t *testing.T) {
	g := Generation{Date: "2026-08-23", Run: "1787500934"}
	built, ok := g.Built()
	if !ok {
		t.Fatal("a plausible run id reported no build time")
	}
	if got := built.UTC().Format("2006-01-02"); got != g.Date {
		t.Errorf("build date %s disagrees with the filename's %s", got, g.Date)
	}
	if built.After(time.Now()) {
		t.Error("built in the future")
	}

	for _, bad := range []string{"", "abc", "1", "99999999999999"} {
		if _, ok := (Generation{Run: bad}).Built(); ok {
			t.Errorf("run %q was accepted as a timestamp", bad)
		}
	}
}
