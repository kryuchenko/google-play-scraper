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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
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
//
// Every invocation writes its -debug log (each request's URL, status, attempt
// and latency, each retry) next to a record of the run: arguments, exit
// status, stderr and the first 4 KB of stdout. A test that fails keeps them
// under $GPSCRAPE_EVIDENCE_DIR/cli, or a temporary directory it logs; one that
// passes deletes them. The binary is another process, so this is what can be
// had of its traffic without a proxy: no response headers or bodies, which
// the library canary's evidence has.
func run(t *testing.T, bin string, args ...string) string {
	t.Helper()
	ev := cliEvidenceFor(t)
	// Flags go after the command, and after its verb when it has one: the
	// dispatchers read those positionally, before any flag parsing.
	n := 1
	if len(args) > 1 && args[0] == "catalog" && !strings.HasPrefix(args[1], "-") {
		n = 2
	}
	ev.n++
	logFile := filepath.Join(ev.dir, fmt.Sprintf("%02d-%s.log", ev.n, strings.Join(args[:n], "-")))
	args = append(append(append([]string{}, args[:n]...),
		"-throttle", "700ms", "-debug", "-log-file", logFile), args[n:]...)
	cmd := exec.Command(bin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	start := time.Now()
	out, err := cmd.Output()
	ev.runs = append(ev.runs, cliInvocation{
		Args: args, Started: start.UTC(), Took: time.Since(start).String(),
		Exit: cmd.ProcessState.ExitCode(), Stderr: stderr.String(),
		Stdout: string(out[:min(len(out), 4<<10)]), StdoutBytes: len(out), Log: filepath.Base(logFile),
	})
	if err != nil {
		t.Fatalf("gpscrape %s: %v\nstderr: %s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

type cliInvocation struct {
	Args        []string  `json:"args"`
	Started     time.Time `json:"started"`
	Took        string    `json:"took"`
	Exit        int       `json:"exit"`
	Stderr      string    `json:"stderr"`
	Stdout      string    `json:"stdout"`
	StdoutBytes int       `json:"stdout_bytes"`
	Log         string    `json:"log"`
}

type cliEvidence struct {
	dir  string
	n    int
	runs []cliInvocation
}

var (
	cliEvidenceMu   sync.Mutex
	cliEvidenceByT  = map[*testing.T]*cliEvidence{}
	cliEvidenceRoot string
)

// cliEvidenceFor is t's evidence directory, created on first use and kept
// only if t fails.
func cliEvidenceFor(t *testing.T) *cliEvidence {
	t.Helper()
	cliEvidenceMu.Lock()
	defer cliEvidenceMu.Unlock()
	if ev := cliEvidenceByT[t]; ev != nil {
		return ev
	}
	if cliEvidenceRoot == "" {
		cliEvidenceRoot = os.Getenv("GPSCRAPE_EVIDENCE_DIR")
		if cliEvidenceRoot == "" {
			d, err := os.MkdirTemp("", "gpscrape-evidence-")
			if err != nil {
				t.Fatal(err)
			}
			cliEvidenceRoot = d
		}
	}
	dir := filepath.Join(cliEvidenceRoot, "cli", regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(t.Name(), "_"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ev := &cliEvidence{dir: dir}
	cliEvidenceByT[t] = ev
	t.Cleanup(func() {
		if !t.Failed() {
			_ = os.RemoveAll(dir)
			return
		}
		b, _ := json.MarshalIndent(ev.runs, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "invocations.json"), b, 0o644); err != nil {
			t.Logf("evidence: %v", err)
		}
		t.Logf("evidence: the runs and their -debug logs are in %s", dir)
	})
	return ev
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
		nonEmptyString(t, rec, "app_id")
		nonEmptyString(t, rec, "title")
		nonEmptyString(t, rec, "developer")
		if rec["app_id"] != stableApp {
			t.Errorf("app_id = %v, want %s", rec["app_id"], stableApp)
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
			if rec["app_id"] != ids[i] {
				t.Errorf("record %d is for %v, want %s", i, rec["app_id"], ids[i])
			}
			nonEmptyString(t, rec, "title")
		}
	})

	// One record per id, and an app without a PC build is a record too.
	t.Run("pcapp", func(t *testing.T) {
		recs := ndjson(t, run(t, bin, "pcapp", "com.mytona.cookingdiary.android", "com.whatsapp"))
		if len(recs) != 2 {
			t.Fatalf("got %d records for 2 apps", len(recs))
		}
		if recs[0]["onPC"] != true {
			t.Errorf("a PC game gave onPC = %v", recs[0]["onPC"])
		}
		nonEmptyString(t, recs[0], "title")
		if recs[1]["appId"] != "com.whatsapp" || recs[1]["onPC"] != false {
			t.Errorf("an app without a PC build gave %v", recs[1])
		}
	})

	t.Run("search", func(t *testing.T) {
		recs := ndjson(t, run(t, bin, "search", "-num", "5", "maps"))
		if len(recs) > 5 {
			t.Errorf("-num 5 emitted %d records", len(recs))
		}
		nonEmptyString(t, recs[0], "app_id")
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
		nonEmptyString(t, ndjson(t, run(t, bin, "similar", stableApp))[0], "app_id")
	})

	t.Run("developer", func(t *testing.T) {
		nonEmptyString(t, ndjson(t, run(t, bin, "developer", "Google LLC"))[0], "app_id")
	})

	// One app or several, the shape is the same: a record per app. That
	// uniformity is the point -- an earlier version emitted bare permission
	// objects for a single app, so a jq pipeline broke the day someone passed
	// a second id.
	t.Run("permissions", func(t *testing.T) {
		rec := ndjson(t, run(t, bin, "permissions", stableApp))[0]
		nonEmptyString(t, rec, "app_id")
		groups, ok := rec["permissions"].(map[string]any)
		if !ok || len(groups) == 0 {
			t.Fatalf("permissions missing or empty: %v", rec["permissions"])
		}
		for group, perms := range groups {
			if list, ok := perms.([]any); !ok || len(list) == 0 || group == "" {
				t.Errorf("group %q: %v", group, perms)
			}
		}
	})

	t.Run("permissions_several", func(t *testing.T) {
		ids := []string{stableApp, "com.spotify.music"}
		recs := ndjson(t, run(t, bin, "permissions", ids[0], ids[1]))
		if len(recs) != len(ids) {
			t.Fatalf("got %d records for %d apps", len(recs), len(ids))
		}
		for i, rec := range recs {
			if rec["app_id"] != ids[i] {
				t.Errorf("record %d is for %v, want %s", i, rec["app_id"], ids[i])
			}
			if _, ok := rec["permissions"].(map[string]any); !ok {
				t.Errorf("%v: permissions missing", rec["app_id"])
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

	// availability emits one record per app, not one record per country.
	t.Run("availability", func(t *testing.T) {
		recs := ndjson(t, run(t, bin, "availability", "-countries", "us,de,jp", stableApp))
		if len(recs) != 1 {
			t.Fatalf("got %d records, want one per app", len(recs))
		}
		nonEmptyString(t, recs[0], "app_id")
		// The shared record sorts the countries into lists instead of a
		// country->status map.
		seen := map[string]bool{}
		for _, key := range []string{"available", "region_locked", "not_listed"} {
			list, ok := recs[0][key].([]any)
			if !ok {
				t.Fatalf("%s missing or not a list: %v", key, recs[0][key])
			}
			for _, c := range list {
				seen[c.(string)] = true
			}
		}
		for _, country := range []string{"us", "de", "jp"} {
			if !seen[country] {
				if errs, _ := recs[0]["errors"].(map[string]any); errs[country] == nil {
					t.Errorf("no status and no error for %q in %v", country, recs[0])
				}
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

	// catalog check against a directory is the cheap half of the batch job:
	// it reports the generation without starting a sweep, and without
	// creating anything. (It used to be spelled `sync -check`; both the
	// command and the flag are gone, and this is the one surface for it.)
	t.Run("catalog_check_dir", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "not-created-yet")
		rec := ndjson(t, run(t, bin, "catalog", "check", "-dir", dir))[0]
		nonEmptyString(t, rec, "generation")
		if _, ok := rec["up_to_date"].(bool); !ok {
			t.Errorf("up_to_date missing or not a bool: %v", rec["up_to_date"])
		}
		// Asking a question must not create the snapshot directory.
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("catalog check created %s as a side effect", dir)
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
			if h, ok := rec["age_hours"].(float64); !ok || h < 0 {
				t.Errorf("age_hours = %v", rec["age_hours"])
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
			nonEmptyString(t, rec, "app_id")
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
			byID[r["app_id"].(string)] = r
		}
		if g := byID["com.king.candycrushsaga"]["genre_id"]; g == nil || !strings.HasPrefix(g.(string), "GAME") {
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

// TestCanaryCatalogGames runs the whole-catalog games pipeline -- sweep,
// genres, apps -genre GAME_* -- on a 0.01% sample: about nine sitemap shards
// and a few dozen requests. It proves the chain is alive end to end (the
// sitemap index is reachable, shards parse, genre lookups resolve, the games
// filter reads the table), not that a full sweep finishes; that is 87k
// requests and hours, and nobody runs it to check a pipe.
func TestCanaryCatalogGames(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()

	var sweep map[string]any
	for _, rec := range ndjson(t, run(t, bin, "catalog", "sweep", "-dir", dir, "-sample", "0.01", "-yes")) {
		if _, ok := rec["ids"]; ok {
			sweep = rec
		}
	}
	ids, _ := sweep["ids"].(float64)
	if ids < 50 {
		t.Fatalf("sampled sweep listed %.0f ids, want at least 50: %v", ids, sweep)
	}

	genres := ndjson(t, run(t, bin, "catalog", "genres", "-dir", dir, "-all"))
	resolved := 0
	for _, g := range genres {
		if s, _ := g["genre_id"].(string); s != "" {
			resolved++
		}
	}
	// About nine in ten resolve; the rest are sitemap entries no storefront
	// serves any more. Under seven in ten means lookups are failing.
	if float64(resolved) < 0.7*float64(len(genres)) {
		t.Errorf("%d of %d apps got a genre, want at least 70%%", resolved, len(genres))
	}

	// apps reads the table on disk and makes no request, so it takes no
	// -throttle and runs without the helper that adds one.
	out, err := exec.Command(bin, "catalog", "apps", "-dir", dir, "-genre", "GAME_*", "-allow-sample").Output()
	if err != nil {
		t.Fatalf("catalog apps: %v", err)
	}
	games := ndjson(t, string(out))
	kinds := map[string]bool{}
	for _, g := range games {
		id, _ := g["genre_id"].(string)
		if !strings.HasPrefix(id, "GAME_") {
			t.Errorf("GAME_* filter let through %v", g)
		}
		kinds[id] = true
		nonEmptyString(t, g, "app_id")
	}
	// Games are about a tenth of the store, spread over 17 genres.
	if len(kinds) < 3 {
		t.Errorf("%d games in %d genres, want at least 3 genres: %v", len(games), len(kinds), kinds)
	}
	t.Logf("sample: %.0f ids, %d with a genre, %d games in %d genres", ids, resolved, len(games), len(kinds))
}
