package googleplayscraper

import (
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
)

// qnkhobPayloadTemplate is the URL-encoded "f.req" body for the qnKhOb RPC,
// captured byte-for-byte from a live browser request against
// /store/apps/category/GAME_ACTION (2026-06-12). It carries a single __TOKEN__
// placeholder where the recommendation-topic continuation blob goes.
//
// Re-capturing when this drifts (Google rebuilds the flag list periodically; a
// stale body makes Google return a NULL payload):
//
//  1. Open DevTools → Network on https://play.google.com/store/apps/category/GAME_ACTION
//  2. Scroll until you see POST .../batchexecute?rpcids=qnKhOb requests.
//  3. Copy the request's raw "f.req=..." form body (URL-encoded, ~38 KB).
//  4. Locate the continuation token — the long base64url string just before
//     the trailing `%22%5D%2C%5B1%5D%5D` (decoded: `"],[1]]`) — and replace it
//     verbatim with __TOKEN__. Do not decode/re-encode the rest of the body;
//     keep it byte-exact.
//
//go:embed qnkhob_payload.txt
var qnkhobPayloadTemplate string

// qnkhobParams carries the per-request metadata the qnKhOb RPC needs. fsid and
// bl are read live from the page's WIZ_global_data (see extractWizData); a stale
// bl or a mismatched sourcePath makes Google reject the request with a NULL
// payload.
type qnkhobParams struct {
	lang       string
	country    string
	sourcePath string // e.g. /store/apps/category/GAME_ACTION
	fsid       string
	bl         string
}

// nextReqID returns a monotonically increasing _reqid for batchexecute URLs.
// Google expects this value to grow across a session's requests; the absolute
// starting point does not matter, only that it increases.
func (c *Client) nextReqID() int64 {
	return 100000 + atomic.AddInt64(&c.reqIDSeq, 1)
}

// fetchQnKhOb performs a single qnKhOb batchexecute request for the given
// continuation token and returns the page's apps plus the next continuation
// token, or "" when the feed is exhausted.
//
// It mirrors listViaBatch in list.go: the URL is built with url.Values and the
// body is the embedded template with the token substituted. A NULL payload
// (Google's "no more data" signal, surfaced as data == nil by
// decodeBatchEnvelope) yields no results and an empty next token.
func (c *Client) fetchQnKhOb(ctx context.Context, token string, p qnkhobParams) ([]SearchResult, string, error) {
	body := strings.NewReplacer("__TOKEN__", token).Replace(qnkhobPayloadTemplate)

	query := url.Values{
		"rpcids":       {"qnKhOb"},
		"source-path":  {p.sourcePath},
		"f.sid":        {p.fsid},
		"bl":           {p.bl},
		"hl":           {p.lang},
		"gl":           {p.country},
		"authuser":     {""},
		"soc-app":      {"121"},
		"soc-platform": {"1"},
		"soc-device":   {"1"},
		"_reqid":       {fmt.Sprintf("%d", c.nextReqID())},
		"rt":           {"c"},
	}

	reqURL := fmt.Sprintf("%s/_/PlayStoreUi/data/batchexecute?%s", BaseURL, query.Encode())
	respBody, err := c.post(ctx, reqURL, "application/x-www-form-urlencoded;charset=UTF-8", body)
	if err != nil {
		return nil, "", err
	}

	data, err := decodeBatchEnvelope(respBody)
	if err != nil {
		return nil, "", err
	}
	results, next := parseQnKhObResponse(data)
	return results, next, nil
}

// parseQnKhObResponse extracts apps and the response's echo token from a decoded
// qnKhOb payload (data == decodeBatchEnvelope output).
//
// Apps live at [0][21][0][N] with the package id at [N][0][0]; the layout
// matches the search/cluster app entries, so we reuse bestAppsArray to locate
// the largest grid robustly even if the wrapper indices shift.
//
// The token at [0][3][0] re-references the SAME recommendation topic and does
// NOT advance the feed — replaying it returns a NULL payload, because Google
// allocates each next topic server-side per session and never echoes it. We
// still return it for response-shape drift detection, but pagination does not
// use it: paginateQnKhOb fetches one token per recommendation section instead,
// each derived statelessly from the page's cluster URLs (see extractFeedTokens).
func parseQnKhObResponse(data []interface{}) ([]SearchResult, string) {
	if data == nil {
		return nil, ""
	}

	apps := findAppsInData(getPath(data, 0, 21, 0))
	if apps == nil {
		apps = findAppsInData(data)
	}

	results := make([]SearchResult, 0, len(apps))
	for _, app := range apps {
		if r := parseSearchResultNew(app); r.AppID != "" {
			results = append(results, r)
		}
	}

	token := toString(getPath(data, 0, 3, 0))
	return results, token
}

// feedClusterMarker is the recommendation-cluster URL fragment whose gsr query
// value carries a usable recs_topic continuation token. In rendered HTML the
// query separator is JSON-escaped (=), so we normalize that before scanning.
const feedClusterMarker = "cluster?gsr="

// gsrEscapedEquals is the JSON-unicode escape Google emits for '=' inside the
// embedded data blocks (a backslash followed by u003d). We restore it to '='
// so feedClusterMarker matches. Built from runes to keep the backslash literal
// unambiguous.
var gsrEscapedEquals = string([]byte{'\\', 'u', '0', '0', '3', 'd'})

// extractFeedTokens harvests stateless page-1 continuation tokens for the
// qnKhOb recommendation feed from a category/cluster HTML page.
//
// Each "recommended for you" section on the page links to
// /store/apps/collection/cluster?gsr=<blob>. That gsr blob is a base64url
// protobuf: a field-9 (tag 0x4a) wrapper around the recs query
// (…recs_topic_<id>…). The qnKhOb RPC, however, expects the SAME recs query
// wrapped in field 12 (tag 0x62) — that is the form a live browser sends, minus
// the per-session "already seen" cursor. We therefore unwrap each gsr blob and
// re-wrap its inner query as field 12, yielding a clean-slate token that returns
// the topic's full app set (verified live 2026-06-12: GAME_ACTION +59,
// GAME_PUZZLE +33, SOCIAL +24 apps over the initial grid, zero dupes).
//
// Tokens are returned in page order and de-duplicated, so callers get one token
// per distinct recommendation topic. This is the working replacement for the
// dead [0][3][0] echo token, which re-references the current topic and is
// answered with a NULL payload on replay.
func extractFeedTokens(body []byte) []string {
	html := strings.ReplaceAll(string(body), gsrEscapedEquals, "=")

	var tokens []string
	seen := make(map[string]bool)
	for i := 0; ; {
		j := strings.Index(html[i:], feedClusterMarker)
		if j < 0 {
			break
		}
		start := i + j + len(feedClusterMarker)
		end := start
		for end < len(html) && isBase64URLByte(html[end]) {
			end++
		}
		i = end

		tok, ok := gsrToFeedToken(html[start:end])
		if !ok || seen[tok] {
			continue
		}
		seen[tok] = true
		tokens = append(tokens, tok)
	}
	return tokens
}

// gsrToFeedToken converts a cluster-URL gsr blob into a qnKhOb continuation
// token, or reports ok=false when the blob is not a recs_topic recommendation
// wrapper (e.g. promotion/new-release clusters, which paginate differently).
func gsrToFeedToken(gsr string) (token string, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(gsr, "="))
	if err != nil || len(raw) < 2 {
		return "", false
	}
	// The gsr blob wraps the recs query in protobuf field 9 (tag 0x4a, wire
	// type 2 → length-delimited). Anything else is a different cluster kind.
	if raw[0] != 0x4a {
		return "", false
	}
	innerLen := int(raw[1])
	if 2+innerLen > len(raw) {
		return "", false
	}
	inner := raw[2 : 2+innerLen]
	if !strings.Contains(string(inner), "recs_topic") {
		return "", false
	}
	// Drop a trailing field-31 marker (f8 01 00) the cluster URL carries but the
	// feed token does not.
	if n := len(inner); n >= 3 && inner[n-3] == 0xf8 && inner[n-2] == 0x01 && inner[n-1] == 0x00 {
		inner = inner[:n-3]
	}
	// Re-wrap the query in field 12 (tag 0x62), the shape the qnKhOb RPC reads.
	// inner is always < 256 bytes here (a single recs_topic query), so a
	// one-byte length prefix is sufficient and matches the live browser token.
	if len(inner) > 255 {
		return "", false
	}
	wrapped := make([]byte, 0, len(inner)+2)
	wrapped = append(wrapped, 0x62, byte(len(inner)))
	wrapped = append(wrapped, inner...)
	return base64.RawURLEncoding.EncodeToString(wrapped), true
}

// isBase64URLByte reports whether b is a base64url alphabet character, used to
// bound the gsr token in raw HTML without a regexp dependency.
func isBase64URLByte(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		return true
	case b == '-' || b == '_':
		return true
	default:
		return false
	}
}
