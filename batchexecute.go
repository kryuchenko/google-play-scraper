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
func decodeBatchEnvelope(body []byte) ([]interface{}, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("batchexecute: empty response")
	}

	parsedAny := false
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '[' {
			continue // skip ")]}'" prefix and numeric chunk markers
		}

		var frames [][]interface{}
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

			var data []interface{}
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
