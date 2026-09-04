package googleplayscraper

import (
	"bytes"
	"encoding/json"
	"iter"
)

// Markers for the AF_initDataCallback blocks Google Play embeds in its HTML.
// Scanning for these is what replaced a regular expression here; see
// parseDataBlocks for why.
var (
	blockOpen  = []byte("AF_initDataCallback({key:")
	blockData  = []byte("data:")
	blockClose = []byte(", sideChannel:")
	blockQuote = []byte("'")
)

// parseDataBlocks extracts every AF_initDataCallback script block from a Google
// Play HTML page and returns its decoded JSON payload keyed by the ds:N
// identifier. Blocks whose data fails to unmarshal are skipped, mirroring the
// lenient parsing these pages require.
//
// This used to be a regular expression:
//
//	AF_initDataCallback\(\{key:\s*'(ds:\d+)'.*?data:(.*?), sideChannel:
//
// Two lazy quantifiers scanning a megabyte of HTML backtrack badly, and the
// surrounding code copied the whole page into a string to feed it and then
// copied each payload back out. Locating the blocks by substring search
// instead measured 46x faster on the recorded fixtures -- 8.5ms to 0.18ms --
// and cut a details page from 11.5ms to 1.7ms with 70% less garbage. The
// equivalence is held down by a differential fuzz test that compares this
// against the original expression on arbitrary input.
func parseDataBlocks(body []byte) map[string]any {
	dataBlocks := make(map[string]any)
	for key, data := range dataBlockSeq(body) {
		dataBlocks[key] = data
	}
	return dataBlocks
}

// dataBlock decodes one block by key, skipping the rest.
//
// Most callers want a single ds:N. Locating all the blocks is cheap -- 0.16ms
// on a details page -- but decoding them is not: 1.09ms of the 1.31ms it takes
// to parse a page goes into building trees that are then thrown away. Reading
// only the one that was asked for measured 4.8x faster and allocated 7.6x
// less: 265us and 79KB against 1.29ms and 602KB.
//
// This matters most where the same page is fetched many times over. An
// Availability sweep probes 177 countries for one app and reads ds:5 from each
// response, so it was decoding twelve unwanted trees 177 times.
func dataBlock(body []byte, key string) (any, bool) {
	for k, raw := range rawBlockSeq(body) {
		if k != key {
			continue // boundaries found, JSON left alone
		}
		var data any
		if err := json.Unmarshal(raw, &data); err != nil {
			return nil, false
		}
		return data, true
	}
	return nil, false
}

// dataBlockSeq yields the same blocks in document order.
//
// Order matters to the callers that build lists from them -- a map would
// randomise the order of clusters on a category page, which is the order the
// page itself presents them in.
func dataBlockSeq(body []byte) iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		for key, raw := range rawBlockSeq(body) {
			var data any
			if err := json.Unmarshal(raw, &data); err != nil {
				continue
			}
			if !yield(key, data) {
				return
			}
		}
	}
}

// rawBlockSeq yields each block's key and its undecoded payload.
//
// Separating locating from decoding is what lets dataBlock skip the twelve
// blocks nobody asked for: finding the boundaries costs almost nothing, and
// building a tree out of them costs almost everything.
func rawBlockSeq(body []byte) iter.Seq2[string, []byte] {
	return func(yield func(string, []byte) bool) {
		rest := body
		for {
			i := bytes.Index(rest, blockOpen)
			if i < 0 {
				return
			}
			rest = rest[i+len(blockOpen):]

			// The key sits between the next pair of single quotes, and only
			// whitespace may precede the opening one. The expression this
			// replaced matched `key:\s*'`, and without the same restriction a
			// scanner accepts `key:0'ds:0'` as a block -- found by the
			// differential fuzzer, not by reading the code.
			ws := 0
			for ws < len(rest) && isASCIISpace(rest[ws]) {
				ws++
			}
			if ws >= len(rest) || rest[ws] != '\'' {
				continue
			}
			q1 := ws
			q2 := bytes.Index(rest[q1+1:], blockQuote)
			if q2 < 0 {
				return
			}
			key := rest[q1+1 : q1+1+q2]
			rest = rest[q1+1+q2+1:]

			// The expression this replaced matched 'ds:\d+' and nothing else.
			// Without the same check a malformed page yields a block under an
			// empty key -- harmless to every current caller, which looks up a
			// specific ds:N, and exactly the kind of drift that accumulates
			// unnoticed. Found by the differential fuzzer, not by review.
			if !isDataBlockKey(key) {
				continue
			}

			d := bytes.Index(rest, blockData)
			if d < 0 {
				return
			}
			payload := rest[d+len(blockData):]

			end := bytes.Index(payload, blockClose)
			if end < 0 {
				return
			}
			raw := bytes.TrimSpace(payload[:end])
			rest = payload[end:]

			if !yield(string(key), raw) {
				return
			}
		}
	}
}

// isDataBlockKey reports whether key has the ds:N form, matching the
// `'(ds:\d+)'` capture of the expression this file used to run.
func isDataBlockKey(key []byte) bool {
	const prefix = "ds:"
	if len(key) <= len(prefix) || string(key[:len(prefix)]) != prefix {
		return false
	}
	for _, c := range key[len(prefix):] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isASCIISpace matches what a regular expression's \s does for the bytes that
// can appear here: Google's pages separate these tokens with ordinary spaces
// and newlines.
func isASCIISpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	}
	return false
}
