package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kryuchenko/google-play-scraper/records"
)

// Tests for the acceptance run's findings on the release candidate: each
// pins a behaviour the run found wrong.

func codeOf(t *testing.T, err error) records.Code {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	return records.Classify(err)
}

// No count is meaningful below zero. search -num -1 panicked in the library;
// reviews -limit -1 read as "no limit" and downloaded every review.
func TestNegativeCountsAreRefused(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	for name, run := range map[string]func() error{
		"search -num -1":    func() error { _, _, err := runVerb(t, cmdSearch, "-num", "-1", "chess"); return err },
		"reviews -limit -1": func() error { _, _, err := runVerb(t, cmdReviews, "-limit", "-1", "com.a.b"); return err },
		"list -num -1":      func() error { _, _, err := runVerb(t, cmdList, "-num", "-1"); return err },
		"-concurrency -2":   func() error { _, _, err := runVerb(t, cmdApp, "-concurrency", "-2", "com.a.b"); return err },
	} {
		if code := codeOf(t, run()); code != records.CodeInvalid {
			t.Errorf("%s: code %s, want invalid_argument", name, code)
		}
	}
	if n := store.hitCount(pathBatch) + store.hitCount("/store/search"); n != 0 {
		t.Errorf("%d requests went out for arguments refused up front", n)
	}
}

// A name where an id belongs, and an empty search term, are the caller's
// mistakes: invalid_argument before any request, not a retryable store_error
// after an HTTP 400. A store link is taken for the id it carries.
func TestArgumentsAreCheckedBeforeAnyRequest(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	if code := codeOf(t, func() error { _, _, err := runVerb(t, cmdApp, "Spotify Music"); return err }()); code != records.CodeInvalid {
		t.Errorf("a name as the id: %s, want invalid_argument", code)
	}
	if code := codeOf(t, func() error { _, _, err := runVerb(t, cmdApp, "com.ok", "not an id"); return err }()); code != records.CodeInvalid {
		t.Errorf("a name in a batch: %s, want invalid_argument", code)
	}
	if code := codeOf(t, func() error { _, _, err := runVerb(t, cmdSearch, "  "); return err }()); code != records.CodeInvalid {
		t.Errorf("an empty search term: %s, want invalid_argument", code)
	}
	if code := codeOf(t, func() error { _, _, err := runVerb(t, cmdSuggest, ""); return err }()); code != records.CodeInvalid {
		t.Errorf("an empty suggest term: %s, want invalid_argument", code)
	}
	if n := store.hitCount(pathBatch) + store.hitCount("/store/apps/details") + store.hitCount("/store/search"); n != 0 {
		t.Errorf("%d requests went out for arguments refused up front", n)
	}
	if id, err := checkAppID("https://play.google.com/store/apps/details?id=com.spotify.music&hl=en"); err != nil || id != "com.spotify.music" {
		t.Errorf("store link: %q, %v", id, err)
	}
}

// A batch in which every id failed the same way carries that code: every id
// not found is not_found (exit 3), not a retryable store_error.
func TestABatchThatFailedTheSameWayCarriesTheCode(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	appPermissionsStub(t, store, map[string]string{})

	_, _, err := runVerb(t, cmdApp, "com.bad", "com.bad2")
	if code := codeOf(t, err); code != records.CodeNotFound || code.ExitCode() != 3 {
		t.Errorf("app with every id missing: %s (exit %d), want not_found (exit 3): %v", code, code.ExitCode(), err)
	}
	_, _, err = runVerb(t, cmdPermissions, "com.bad")
	// The permissions RPC has no details page to fall back on, so a missing
	// id reads as not_listed; either way it is exit 3, not a retryable 1.
	if code := codeOf(t, err); code.ExitCode() != 3 {
		t.Errorf("permissions with its one id missing: %s (exit %d), want exit 3 like app: %v", code, code.ExitCode(), err)
	}
	mixed := batchFailed([]error{records.Errorf(records.CodeNotFound, "x"), records.Errorf(records.CodeRateLimited, "y")})
	if records.Classify(mixed) != records.CodeStore {
		t.Errorf("mixed failures: %s, want store_error", records.Classify(mixed))
	}
}

// -fields keeps what says an entry failed; projected to app_id,title a failed
// entry used to become {"app_id"}, a record with its fields merely missing.
func TestFieldsKeepTheFailureOnAFailedEntry(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	appPermissionsStub(t, store, map[string]string{"com.ok": `[null,[null,null,[["ok"]]]]`})

	out, _, err := runVerb(t, cmdApp, "-fields", "app_id,title", "com.ok", "com.bad")
	if err != nil {
		t.Fatal(err)
	}
	recs := jsonLines(t, out)
	if len(recs) != 2 || recs[1]["code"] != "not_found" || recs[1]["error"] == nil {
		t.Errorf("records = %v, want the failed entry to keep code and error", recs)
	}
	if _, ok := recs[0]["code"]; ok {
		t.Errorf("a successful record grew a code: %v", recs[0])
	}
}

// A -fields name no record of the command has is refused before any request.
func TestFieldsAreCheckedBeforeAnyRequest(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	_, _, err := runVerb(t, cmdApp, "-fields", "app_id,nosuch", "com.a.b")
	if code := codeOf(t, err); code != records.CodeInvalid || !strings.Contains(err.Error(), "nosuch") {
		t.Errorf("err = %v (%s), want invalid_argument naming nosuch", err, code)
	}
	if n := store.hitCount(pathBatch) + store.hitCount("/store/apps/details"); n != 0 {
		t.Errorf("%d requests went out before the -fields check", n)
	}
}

// availability takes only Play Store country codes, as the MCP tool does:
// zz came back "available", and an empty list meant zero probes and exit 0.
// A run in which every country failed exits with their shared code.
func TestAvailabilityChecksItsCountriesAndItsOutcome(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	for _, arg := range []string{"us,zz", "us,cn", ",", " , "} {
		_, _, err := runVerb(t, cmdAvailability, "-countries", arg, "com.a.b")
		if code := codeOf(t, err); code != records.CodeInvalid {
			t.Errorf("-countries %q: %s, want invalid_argument", arg, code)
		}
	}
	if n := store.hitCount("/store/apps/details"); n != 0 {
		t.Fatalf("%d probes went out for refused countries", n)
	}

	store.set("/store/apps/details", http.StatusInternalServerError, nil)
	store.set(pathBatch, http.StatusInternalServerError, nil)
	out, _, err := runVerb(t, cmdAvailability, "-throttle", "0", "-countries", "us,gb", "com.a.b")
	if err == nil {
		t.Fatalf("every country failed and the run exited 0:\n%s", out)
	}
	if recs := jsonLines(t, out); len(recs) != 1 {
		t.Errorf("the record was not written before the error:\n%s", out)
	}
}

// Google answers an unknown id's reviews with an empty page, the same as an
// app with none. reviews used to exit 0 with nothing printed; it checks the
// details page when nothing came back, as the MCP tool does.
func TestReviewsOfAnUnknownAppIsNotFound(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	store.set(pathBatch, http.StatusOK, framesEnvelope("oCPfdb", map[string]string{"generic": `[[],[null,null]]`}, []string{"generic"}))
	store.set("/store/apps/details", http.StatusNotFound, nil)
	out, _, err := runVerb(t, cmdReviews, "-throttle", "0", "com.no.such.app")
	if code := codeOf(t, err); code != records.CodeNotFound {
		t.Errorf("reviews of an unknown app: %s (%v), want not_found; stdout %q", code, err, out)
	}
	if store.hitCount("/store/apps/details") == 0 {
		t.Error("no details request settled the empty page")
	}
}

// The sweep's confirmation names the generation's shard count rather than a
// figure that is right only for the full store.
func TestSweepConfirmationStatesTheShardCount(t *testing.T) {
	_, _, dir := newSweepFixture(t)
	assumeYes = false
	t.Cleanup(func() { assumeYes = true })
	_, _, err := runVerb(t, cmdSync, "-dir", dir)
	if err == nil || records.Classify(err) != records.CodeConfirmation || !strings.Contains(err.Error(), "4 requests, one per shard of "+fixtureGenA.ID()) {
		t.Errorf("err = %v, want a confirmation naming 4 requests for %s", err, fixtureGenA.ID())
	}
}

// Missing local state -- no snapshot, no genre table, one snapshot for diff --
// is not_found, not a retryable store_error.
func TestMissingLocalStateIsNotFound(t *testing.T) {
	dir := t.TempDir()
	_, _, err := runVerb(t, catalogGenres, "-dir", dir)
	if code := codeOf(t, err); code != records.CodeNotFound {
		t.Errorf("genres without a snapshot: %s: %v", code, err)
	}
	_, _, err = runVerb(t, catalogApps, "-dir", dir)
	if code := codeOf(t, err); code != records.CodeNotFound {
		t.Errorf("apps without a genre table: %s: %v", code, err)
	}
	snap := filepath.Join(dir, "snapshot-"+fixtureGenA.ID()+".txt.gz")
	if _, err := writeSnapshot(snap, []string{"com.a"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "manifest-"+fixtureGenA.ID()+".json"), manifest{Generation: fixtureGenA, File: filepath.Base(snap), IDs: 1}); err != nil {
		t.Fatal(err)
	}
	_, _, err = runVerb(t, catalogDiff, "-dir", dir)
	if code := codeOf(t, err); code != records.CodeNotFound {
		t.Errorf("diff with one snapshot: %s: %v", code, err)
	}
	if _, statErr := os.Stat(dir); errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("temp dir vanished")
	}
}
