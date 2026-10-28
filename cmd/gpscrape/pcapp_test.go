package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pcStore answers the PC listing the way Google does: the recorded page for
// com.pc, a redirect to the mobile listing for com.mobile, 404 for anything
// else. Reaching the mobile listing fails the test, since the redirect alone
// is the answer.
func pcStore(t *testing.T) http.RoundTripper {
	t.Helper()
	page, err := os.ReadFile(filepath.Join("..", "..", "testdata", "pc_page_game.html"))
	if err != nil {
		t.Fatal(err)
	}
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Request: req}
		var body []byte
		switch id := req.URL.Query().Get("id"); {
		case req.URL.Path != "/pc-store/games/details":
			t.Errorf("followed the redirect to %s", req.URL)
		case id == "com.pc":
			body = page
		case id == "com.mobile":
			resp.StatusCode = http.StatusFound
			resp.Header.Set("Location", "https://play.google.com/store/apps/details?id=com.mobile")
		default:
			resp.StatusCode = http.StatusNotFound
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	})
}

func TestPCAppEmitsOneRecordPerIDInOrder(t *testing.T) {
	useFakeClient(t, pcStore(t))

	out, stderr, err := runVerb(t, cmdPCApp, "com.pc", "com.mobile", "com.gone")
	if err != nil {
		t.Fatalf("pcapp: %v; two of three ids answered", err)
	}
	recs := jsonLines(t, out)
	if len(recs) != 3 {
		t.Fatalf("stdout has %d lines for 3 ids:\n%s", len(recs), out)
	}
	if recs[0]["appId"] != "com.pc" || recs[0]["onPC"] != true || recs[0]["title"] == nil {
		t.Errorf("PC listing record = %v", recs[0])
	}
	if recs[1]["appId"] != "com.mobile" || recs[1]["onPC"] != false || recs[1]["error"] != nil {
		t.Errorf("an app without a PC build gave %v, want onPC false and no error", recs[1])
	}
	if recs[2]["appId"] != "com.gone" || recs[2]["error"] == nil || len(recs[2]) != 2 {
		t.Errorf("failure record = %v, want exactly {appId, error}", recs[2])
	}
	if !strings.Contains(stderr, "com.gone") {
		t.Errorf("stderr does not name the app that failed:\n%s", stderr)
	}
}

// One id behaves like `app`: a failure is the run's error, with no record.
func TestPCAppSingleIDFailureIsTheRunsError(t *testing.T) {
	useFakeClient(t, pcStore(t))

	out, _, err := runVerb(t, cmdPCApp, "com.gone")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v, want the 404", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}

	if _, _, err := runVerb(t, cmdPCApp, "com.gone", "com.gone2"); err == nil ||
		!strings.Contains(err.Error(), "all 2 apps failed") {
		t.Errorf("err = %v, want one naming the count", err)
	}
}
