package googleplayscraper

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

// Soft blocks.
//
// Google does not always refuse with a 429. A blocked client can get a 200
// that redirected to www.google.com/sorry, or a page that is not the one it
// asked for. Judged by status alone, such a response is a success: it counted
// towards the next speed-up, was never retried, and reached a parser that
// returned an empty slice with a nil error -- so "no results" and "blocked"
// looked the same to the caller. See docs/adr/0001-soft-blocks-and-cooldown.md.
//
// Every response is therefore classified before the controller or a hook sees
// it: by where the redirect chain ended, by status, and by a per-endpoint check
// of the body. The checks look only for the structure the endpoint's parser
// already depends on, so they cannot reject a page the parser would have read.

// Verdict is the classification of one response.
type Verdict int

const (
	// VerdictOK means the expected data is there.
	VerdictOK Verdict = iota
	// VerdictEmpty means a page that positively says it has no data, such as
	// a search with no matches. It is an answer, not a failure.
	VerdictEmpty
	// VerdictBlocked means the server refused without saying 429: a redirect
	// to google.com/sorry, or an HTML page where data or an RPC envelope
	// belongs. It is treated like a 429.
	VerdictBlocked
	// VerdictInterstitial means a consent or sign-in page. Waiting will not
	// change it, so it is neither retried nor taken as a reason to slow down.
	VerdictInterstitial
	// VerdictMalformed means a store page without the structure the parser
	// needs. More often a markup change than a refusal, so it gets the gentle
	// slowdown and never pauses the client.
	VerdictMalformed
)

func (v Verdict) String() string {
	switch v {
	case VerdictOK:
		return "ok"
	case VerdictEmpty:
		return "empty"
	case VerdictBlocked:
		return "blocked"
	case VerdictInterstitial:
		return "interstitial"
	case VerdictMalformed:
		return "malformed"
	}
	return fmt.Sprintf("verdict(%d)", int(v))
}

// SoftBlockError reports a 200 response, or a redirect, that was not the data
// asked for. Callers tell it from an empty result with errors.As.
type SoftBlockError struct {
	Verdict Verdict
	// Reason is a short machine-readable cause, e.g. "sorry-redirect" or
	// "no-ds:5".
	Reason string
	// URL is where the redirect chain ended.
	URL string
	// Code is the HTTP status of the final response.
	Code int
}

func (e *SoftBlockError) Error() string {
	return fmt.Sprintf("%s response (%s) from %s", e.Verdict, e.Reason, e.URL)
}

// validator classifies a 200 body. Endpoints attach their own; a nil validator
// accepts any body, which is right for sitemaps and robots.txt.
type validator func(body []byte) (Verdict, string)

// classifyFinalURL judges where a redirect chain ended. Only Google's own
// block and interstitial hosts decide anything here; every other host --
// the one that was asked, or one a caller's transport rewrote it to (a proxy,
// a test server) -- is left to the status and body checks.
func classifyFinalURL(requested, final *url.URL) (Verdict, string) {
	if final == nil || requested == nil || strings.EqualFold(final.Hostname(), requested.Hostname()) {
		return VerdictOK, ""
	}
	host := strings.ToLower(final.Hostname())
	switch {
	case (host == "www.google.com" || host == "google.com") &&
		(final.Path == "/sorry" || strings.HasPrefix(final.Path, "/sorry/")):
		return VerdictBlocked, "sorry-redirect"
	case host == "consent.google.com":
		return VerdictInterstitial, "consent"
	case host == "accounts.google.com":
		return VerdictInterstitial, "sign-in"
	}
	return VerdictOK, ""
}

// validateHTMLPage is the check every store page passes: Google renders its
// data into AF_initDataCallback blocks, and a page with none at all is a
// challenge or error page, not a store page with nothing on it.
func validateHTMLPage(body []byte) (Verdict, string) {
	for range rawBlockSeq(body) {
		return VerdictOK, ""
	}
	return VerdictBlocked, "no-data-blocks"
}

// requireBlock is validateHTMLPage plus the one block a parser reads.
func requireBlock(key string) validator {
	reason := "no-" + key
	return func(body []byte) (Verdict, string) {
		found, seen := false, false
		for k := range rawBlockSeq(body) {
			seen = true
			if k == key {
				found = true
				break
			}
		}
		switch {
		case !seen:
			return VerdictBlocked, "no-data-blocks"
		case !found:
			return VerdictMalformed, reason
		}
		return VerdictOK, ""
	}
}

// validateRPC accepts anything the envelope decoder can try, and rejects only
// an HTML page served where a batchexecute envelope belongs. Frame-level
// errors stay with the decoder, which reports them per item.
func validateRPC(body []byte) (Verdict, string) {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '<' {
		return VerdictBlocked, "html-for-rpc"
	}
	return VerdictOK, ""
}

// zeroResultsMarker is what Google names the section it shows instead of a
// result grid. It sits inside a base64 token rather than in visible text,
// which is the point: the visible "No results for ..." line is localized.
const zeroResultsMarker = "zero_results_cluster"

// isZeroResultsSearch reports whether a search page's ds:4 block positively
// says the query matched nothing.
func isZeroResultsSearch(ds4 any) bool {
	sections, ok := getPath(ds4, 0, 1).([]any)
	if !ok {
		return false
	}
	for _, s := range sections {
		tok, _ := getPath(s, 3, 0).(string)
		if tok == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(tok)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(tok)
		}
		if err == nil && bytes.Contains(raw, []byte(zeroResultsMarker)) {
			return true
		}
	}
	return false
}
