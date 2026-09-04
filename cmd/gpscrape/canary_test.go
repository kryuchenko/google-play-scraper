//go:build canary

// Live end-to-end tests for the gpscrape binary.
//
// The library canary next door proves the parse paths still match what Google
// serves. It does not prove the thing users actually run: the binary. Flag
// parsing, positional handling, the NDJSON emitter, the exit codes and the
// per-command wiring all live only here, and a command can be broken -- wrong
// flag name, output written to the wrong stream, a nil deref on an empty result
// -- while every library test stays green.
//
// Gated behind the `canary` build tag and named TestCanaryCLI so the scheduled
// workflow's `-run TestCanary ./...` picks it up alongside the library canary,
// and `go test -tags canary -run TestCanaryCLI ./cmd/gpscrape` runs it alone on
// demand.
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildBinary compiles gpscrape once into the test's temp dir. Testing the
// binary means testing the binary: calling the cmd* functions in-process would
// skip argument parsing and os.Exit handling, which is where this kind of bug
// actually lives.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gpscrape")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("build gpscrape: %v\n%s", err, out)
	}
	return bin
}

// run executes one gpscrape invocation with a throttle, returning stdout.
func run(t *testing.T, bin string, args ...string) string {
	t.Helper()
	// Flags go after the command, and after its verb when it has one: the
	// dispatchers read those positionally, before any flag parsing.
	n := 1
	if len(args) > 1 && args[0] == "catalog" && !strings.HasPrefix(args[1], "-") {
		n = 2
	}
	args = append(append(append([]string{}, args[:n]...), "-throttle", "700ms"), args[n:]...)
	cmd := exec.Command(bin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("gpscrape %s: %v\nstderr: %s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

// ndjson parses the output as newline-delimited JSON objects, which is the
// contract every command that emits records promises.
func ndjson(t *testing.T, out string) []map[string]any {
	t.Helper()
	var records []map[string]any
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d is not JSON: %v\n%.200s", i+1, err, line)
		}
		records = append(records, rec)
	}
	if len(records) == 0 {
		t.Fatal("no records emitted")
	}
	return records
}

func nonEmptyString(t *testing.T, rec map[string]any, field string) {
	t.Helper()
	v, ok := rec[field].(string)
	if !ok || v == "" {
		t.Errorf("field %q missing or empty: %v", field, rec[field])
	}
}

func TestCanaryCLI(t *testing.T) {
	bin := buildBinary(t)
	const stableApp = "com.google.android.apps.maps"

	t.Run("app", func(t *testing.T) {
		rec := ndjson(t, run(t, bin, "app", stableApp))[0]
		nonEmptyString(t, rec, "appId")
		nonEmptyString(t, rec, "title")
		nonEmptyString(t, rec, "developer")
		if rec["appId"] != stableApp {
			t.Errorf("appId = %v, want %s", rec["appId"], stableApp)
		}
	})

	// The batched path is a different code path from the single-app one, and
	// only the binary exercises the branch that chooses between them.
	t.Run("app_several", func(t *testing.T) {
		ids := []string{stableApp, "com.spotify.music", "com.duolingo"}
		recs := ndjson(t, run(t, bin, "app", ids[0], ids[1], ids[2]))
		if len(recs) != len(ids) {
			t.Fatalf("got %d records for %d apps", len(recs), len(ids))
		}
		for i, rec := range recs {
			if rec["appId"] != ids[i] {
				t.Errorf("record %d is for %v, want %s", i, rec["appId"], ids[i])
			}
			nonEmptyString(t, rec, "title")
		}
	})

	t.Run("search", func(t *testing.T) {
		recs := ndjson(t, run(t, bin, "search", "-num", "5", "maps"))
		if len(recs) > 5 {
			t.Errorf("-num 5 emitted %d records", len(recs))
		}
		nonEmptyString(t, recs[0], "appId")
		nonEmptyString(t, recs[0], "title")
	})

	t.Run("reviews", func(t *testing.T) {
		recs := ndjson(t, run(t, bin, "reviews", "-limit", "5", "com.instagram.android"))
		if len(recs) > 5 {
			t.Errorf("-limit 5 emitted %d records", len(recs))
		}
		nonEmptyString(t, recs[0], "id")
	})

	t.Run("similar", func(t *testing.T) {
		nonEmptyString(t, ndjson(t, run(t, bin, "similar", stableApp))[0], "appId")
	})

	t.Run("developer", func(t *testing.T) {
		nonEmptyString(t, ndjson(t, run(t, bin, "developer", "Google LLC"))[0], "appId")
	})

	// One app or several, the shape is the same: a record per app. That
	// uniformity is the point -- an earlier version emitted bare permission
	// objects for a single app, so a jq pipeline broke the day someone passed
	// a second id.
	t.Run("permissions", func(t *testing.T) {
		rec := ndjson(t, run(t, bin, "permissions", stableApp))[0]
		nonEmptyString(t, rec, "appId")
		perms, ok := rec["permissions"].([]any)
		if !ok || len(perms) == 0 {
			t.Fatalf("permissions missing or empty: %v", rec["permissions"])
		}
		first, _ := perms[0].(map[string]any)
		nonEmptyString(t, first, "permission")
		nonEmptyString(t, first, "type")
	})

	t.Run("permissions_several", func(t *testing.T) {
		ids := []string{stableApp, "com.spotify.music"}
		recs := ndjson(t, run(t, bin, "permissions", ids[0], ids[1]))
		if len(recs) != len(ids) {
			t.Fatalf("got %d records for %d apps", len(recs), len(ids))
		}
		for i, rec := range recs {
			if rec["appId"] != ids[i] {
				t.Errorf("record %d is for %v, want %s", i, rec["appId"], ids[i])
			}
			if _, ok := rec["permissions"].([]any); !ok {
				t.Errorf("%v: permissions missing", rec["appId"])
			}
		}
	})

	t.Run("datasafety", func(t *testing.T) {
		ndjson(t, run(t, bin, "datasafety", stableApp))
	})

	t.Run("suggest", func(t *testing.T) {
		out := strings.TrimSpace(run(t, bin, "suggest", "maps"))
		if out == "" {
			t.Fatal("suggest emitted nothing")
		}
	})

	t.Run("categories", func(t *testing.T) {
		out := strings.TrimSpace(run(t, bin, "categories"))
		if !strings.Contains(out, "GAME") {
			t.Errorf("categories output has no GAME entry:\n%.300s", out)
		}
	})

	// availability emits one record per app carrying a country->status map,
	// not one record per country.
	t.Run("availability", func(t *testing.T) {
		recs := ndjson(t, run(t, bin, "availability", "-countries", "us,de,jp", stableApp))
		if len(recs) != 1 {
			t.Fatalf("got %d records, want one per app", len(recs))
		}
		nonEmptyString(t, recs[0], "appId")
		statuses, ok := recs[0]["statuses"].(map[string]any)
		if !ok {
			t.Fatalf("statuses missing or not a map: %v", recs[0]["statuses"])
		}
		for _, country := range []string{"us", "de", "jp"} {
			if _, ok := statuses[country]; !ok {
				t.Errorf("no status for %q in %v", country, statuses)
			}
		}
		if n, ok := recs[0]["checked"].(float64); !ok || int(n) != 3 {
			t.Errorf("checked = %v, want 3", recs[0]["checked"])
		}
	})

	t.Run("catalog_ids", func(t *testing.T) {
		out := run(t, bin, "catalog", "ids", "-shards", "0")
		ids := strings.Fields(strings.TrimSpace(out))
		if len(ids) == 0 {
			t.Fatal("shard 0 yielded no package ids")
		}
		for _, id := range ids[:min(5, len(ids))] {
			if !strings.Contains(id, ".") {
				t.Errorf("%q does not look like a package id", id)
			}
		}
	})

	// sync -check is the cheap half of the batch job: it reports the
	// generation without starting a sweep, and without creating anything.
	t.Run("sync_check", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "not-created-yet")
		rec := ndjson(t, run(t, bin, "sync", "-check", "-dir", dir))[0]
		nonEmptyString(t, rec, "generation")
		if _, ok := rec["upToDate"].(bool); !ok {
			t.Errorf("upToDate missing or not a bool: %v", rec["upToDate"])
		}
		// Asking a question must not create the snapshot directory.
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("-check created %s as a side effect", dir)
		}
	})

	// The catalog verbs, at their cheap end. sweep is 83,445 requests and is
	// deliberately not exercised here.
	t.Run("catalog_check", func(t *testing.T) {
		rec := ndjson(t, run(t, bin, "catalog", "check"))[0]
		nonEmptyString(t, rec, "generation")
		if n, ok := rec["shards"].(float64); !ok || n < 10000 {
			t.Errorf("shards = %v", rec["shards"])
		}
		// The run id has been a Unix timestamp; if it stops being one the age
		// is simply absent rather than wrong.
		if _, ok := rec["built"].(string); ok {
			if h, ok := rec["ageHours"].(float64); !ok || h < 0 {
				t.Errorf("ageHours = %v", rec["ageHours"])
			}
		}
	})

	t.Run("catalog_new", func(t *testing.T) {
		dir := t.TempDir()
		out := run(t, bin, "catalog", "new", "-dir", dir, "-categories", "GAME_PUZZLE", "-num", "30")
		if strings.TrimSpace(out) == "" {
			t.Fatal("no newly seen apps on a first run against an empty log")
		}
		for _, rec := range ndjson(t, out) {
			nonEmptyString(t, rec, "appId")
			if rec["seen"] != "first" {
				t.Errorf("a first run reported %v", rec["seen"])
			}
		}
		// Every observation is logged, not only the new ones: the log is what
		// makes the signal's recall measurable later.
		if _, err := os.Stat(filepath.Join(dir, "signal.log")); err != nil {
			t.Errorf("no signal log: %v", err)
		}
	})

	t.Run("catalog_genres", func(t *testing.T) {
		dir := t.TempDir()
		ids := filepath.Join(dir, "ids.txt")
		if err := os.WriteFile(ids, []byte(
			"com.king.candycrushsaga\ncom.spotify.music\ncom.qa.definitely.not.real.zz\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		recs := ndjson(t, run(t, bin, "catalog", "genres", "-dir", dir, "-ids", ids, "-all"))
		if len(recs) != 3 {
			t.Fatalf("got %d records for 3 ids", len(recs))
		}
		byID := map[string]map[string]any{}
		for _, r := range recs {
			byID[r["appId"].(string)] = r
		}
		if g := byID["com.king.candycrushsaga"]["genreId"]; g == nil || !strings.HasPrefix(g.(string), "GAME") {
			t.Errorf("candy crush genre = %v", g)
		}
		if c := byID["com.qa.definitely.not.real.zz"]["change"]; c != "gone" {
			t.Errorf("the missing app reported %v, want gone", c)
		}
	})

	t.Run("unknown_command_fails", func(t *testing.T) {
		cmd := exec.Command(bin, "definitely-not-a-command")
		if err := cmd.Run(); err == nil {
			t.Error("an unknown command exited 0")
		}
	})

	t.Run("missing_argument_fails", func(t *testing.T) {
		cmd := exec.Command(bin, "app")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Error("app with no appID exited 0")
		}
		if !strings.Contains(string(out), "usage") {
			t.Errorf("error message has no usage line:\n%s", out)
		}
	})

}

// The offline confirm-gone tests next door prove the pairing logic; this one
// proves it against the store, which is the only place the claim "an app
// listed in one country is still an app" can be checked. It lived in
// catalog_test.go behind testing.Short, which meant it ran nowhere: CI is
// -short and the canary workflow does not build that file.
// "Not listed in the storefront this run used" is not "removed from the
// store". Of 200 ids the pipeline had classified as dead, two were alive
// elsewhere: one only in Russia, and one in every market probed except the
// United States it was run from. An app available in one country is still an
// app, and burying it loses a real listing from the index.
func TestConfirmGoneRescuesWhatOneStorefrontCannotSee(t *testing.T) {
	dir := t.TempDir()
	ids := filepath.Join(dir, "ids.txt")
	// com.watchfacestudio.pixoledairking answers in de, in, br, jp, ru and tr
	// but not in us; com.imobpower.misericordia answers nowhere.
	if err := os.WriteFile(ids,
		[]byte("com.watchfacestudio.pixoledairking\ncom.imobpower.misericordia\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := catalogGenres([]string{
			"-dir", dir, "-ids", ids, "-all", "-throttle", "250ms",
		}); err != nil {
			t.Fatal(err)
		}
	})

	byID := map[string]genreRecord{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var r genreRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		byID[r.AppID] = r
	}

	alive := byID["com.watchfacestudio.pixoledairking"]
	if alive.Change == "gone" {
		t.Errorf("an app listed in six markets was buried because %q could not see it", alive.Country)
	}
	if alive.GenreID == "" {
		t.Errorf("rescued without a genre: %+v", alive)
	}
	if alive.Country == "" || alive.Country == "us" {
		t.Errorf("country = %q; it should name the storefront that answered", alive.Country)
	}

	dead := byID["com.imobpower.misericordia"]
	if dead.Change != "gone" {
		t.Errorf("an app no storefront can see was reported as %q", dead.Change)
	}
	// "gone" carries its own scope: absent from these, not proven absent from
	// every market Google runs.
	if !strings.Contains(dead.Country, ",") {
		t.Errorf("country = %q; a gone record should name every storefront asked", dead.Country)
	}

	// The rescued app is in the table; the dead one is not.
	table, _ := loadGenres(dir)
	if _, ok := table["com.watchfacestudio.pixoledairking"]; !ok {
		t.Error("the rescued app is missing from the genre table")
	}
	if _, ok := table["com.imobpower.misericordia"]; ok {
		t.Error("an app no storefront can see was written to the table")
	}
}
