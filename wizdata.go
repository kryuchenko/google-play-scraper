package googleplayscraper

import (
	"encoding/json"
	"regexp"
)

// wizGlobalDataRegex captures the WIZ_global_data object literal that Google
// Play embeds in every rendered HTML page. The object carries per-session
// request metadata (session id, server build label) that the batchexecute RPCs
// echo back as URL parameters.
//
// The match is intentionally non-greedy up to the first "};" so it stops at the
// end of the object literal rather than the end of the script block.
var wizGlobalDataRegex = regexp.MustCompile(`WIZ_global_data\s*=\s*(\{.*?\});`)

// extractWizData pulls the f.sid (session id) and bl (server build label) values
// out of a rendered Google Play HTML page's WIZ_global_data block.
//
// The keys are obfuscated and date-stamped by Google: f.sid lives under
// "FdrFJe" and bl under "cfb2h" (verified against a live category page,
// 2026-06-12). Both values drift — bl is rebuilt roughly daily — so callers must
// read them live per page rather than hardcoding them.
//
// It returns ok=false when the block is absent or malformed, or when either key
// is missing, so callers can fall back rather than send a request Google will
// reject.
func extractWizData(body []byte) (fsid, bl string, ok bool) {
	match := wizGlobalDataRegex.FindSubmatch(body)
	if match == nil {
		return "", "", false
	}

	var data map[string]json.RawMessage
	if err := json.Unmarshal(match[1], &data); err != nil {
		return "", "", false
	}

	fsid = wizString(data["FdrFJe"])
	bl = wizString(data["cfb2h"])
	if fsid == "" || bl == "" {
		return "", "", false
	}
	return fsid, bl, true
}

// wizString decodes a WIZ_global_data value to its string form. f.sid is served
// as a JSON number (a 64-bit signed id) and bl as a JSON string, so we accept
// either rather than assuming one shape.
func wizString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	return ""
}
