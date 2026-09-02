package googleplayscraper

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// decodeBatchEnvelope unwraps a Google Play batchexecute response.
//
// Responses come in two shapes. Both begin with the ")]}'" anti-JSON-hijacking
// prefix; some (e.g. the vyAe2 list RPC) additionally interleave numeric
// byte-length chunk markers between the JSON frames. In either case the payload
// of interest is a "[["wrb.fr", rpcID, dataJSON, ...]]" frame, where dataJSON is
// itself a JSON-encoded string. This helper scans the lines, finds that frame
// and returns the parsed inner data.
//
// It returns (nil, nil) when the frame carries a null payload, which Google
// uses to signal "no more data" (e.g. an exhausted pagination token), and when
// the response is well-formed but contains no wrb.fr frame.
func decodeBatchEnvelope(body []byte) ([]any, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("batchexecute: empty response")
	}

	parsedAny := false
	for line := range bytes.SplitSeq(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '[' {
			continue // skip ")]}'" prefix and numeric chunk markers
		}

		var frames [][]any
		if err := json.Unmarshal(line, &frames); err != nil {
			continue // not the frame line (e.g. the trailing "di"/"af.httprm" line)
		}
		parsedAny = true

		for _, frame := range frames {
			if len(frame) < 3 {
				continue
			}
			if tag, _ := frame[0].(string); tag != "wrb.fr" {
				continue
			}
			dataStr, ok := frame[2].(string)
			if !ok {
				// Null payload: Google's "no more data" signal.
				return nil, nil
			}

			var data []any
			if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
				return nil, fmt.Errorf("batchexecute: parse payload: %w", err)
			}
			return data, nil
		}
	}

	if !parsedAny {
		return nil, fmt.Errorf("batchexecute: no JSON frames in response")
	}
	return nil, nil
}

// decodeBatchFrames returns every wrb.fr frame in a batchexecute response,
// keyed by the index the caller put in the f.req tuple.
//
// decodeBatchEnvelope above returns the first frame and stops, which is all a
// single-RPC request can produce. A batchexecute POST may carry several RPCs,
// and then the first frame is merely the first one Google finished -- not the
// first one asked for. Measured against the live endpoint, a request sending
// indices 7 and 9 came back 9 then 7. Anything that pairs responses to requests
// positionally will therefore hand back another app's data, silently and
// intermittently, which is the worst shape a bug can take. Hence a map: the
// index is the only trustworthy link between a call and its answer.
//
// Payloads are returned undecoded so each caller unmarshals into its own shape.
// A frame carrying a null payload -- Google's "no more data" signal -- is
// present in the map with an empty string, which a caller distinguishes from an
// absent frame by the comma-ok of the lookup.
func decodeBatchFrames(body []byte) (map[string]string, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("batchexecute: empty response")
	}

	frames := make(map[string]string)
	parsedAny := false

	for line := range bytes.SplitSeq(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '[' {
			continue // skip ")]}'" prefix and numeric chunk markers
		}

		var batch [][]any
		if err := json.Unmarshal(line, &batch); err != nil {
			continue // not the frame line
		}
		parsedAny = true

		for _, frame := range batch {
			if len(frame) < 3 {
				continue
			}
			if tag, _ := frame[0].(string); tag != "wrb.fr" {
				continue
			}
			// The index we supplied, echoed back. Single-RPC responses have
			// been seen without it; there the only possible answer is the
			// only call, so "0" is not a guess.
			idx := "0"
			if len(frame) >= 7 {
				if s, ok := frame[6].(string); ok {
					idx = s
				}
			}
			payload, _ := frame[2].(string) // non-string means null: keep ""
			frames[idx] = payload
		}
	}

	if !parsedAny {
		return nil, fmt.Errorf("batchexecute: no JSON frames in response")
	}
	return frames, nil
}
