package googleplayscraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// SuggestOptions configures the search suggestions request
type SuggestOptions struct {
	Term    string
	Lang    string
	Country string
}

// Suggest returns search suggestions for a query
func (c *Client) Suggest(ctx context.Context, opts SuggestOptions) ([]string, error) {
	ctx, endTask := startTask(ctx, traceTaskSuggest)
	defer endTask()

	if opts.Term == "" {
		return nil, fmt.Errorf("term is required")
	}

	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}

	payloads, err := c.batchCall(ctx, opts.Lang, opts.Country, []rpcCall{suggestRPC(opts.Term)})
	if err != nil {
		return nil, err
	}

	return parseSuggestPayload(payloads[0])
}

func parseSuggestResponse(body []byte) ([]string, error) {
	// Skip the )]}'  prefix
	start := 0
	for i := range body {
		if body[i] == '\n' {
			start = i + 1
			break
		}
	}

	if start >= len(body) {
		return nil, fmt.Errorf("invalid response")
	}

	var outer [][]any
	if err := json.Unmarshal(body[start:], &outer); err != nil {
		return nil, err
	}

	if len(outer) == 0 || len(outer[0]) < 3 {
		return nil, nil
	}

	dataStr, ok := outer[0][2].(string)
	if !ok {
		return nil, nil
	}
	return parseSuggestPayload(dataStr)
}

// parseSuggestPayload reads the inner JSON of one IJ4APc frame, split from the
// envelope handling so a batched request can decode the envelope once and then
// read one payload per term.
func parseSuggestPayload(dataStr string) ([]string, error) {
	if dataStr == "" {
		return nil, nil
	}

	var data []any
	if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
		return nil, err
	}

	if data == nil {
		return nil, nil
	}

	// Suggestions in data[0][0]
	suggestions := getPath(data, 0, 0)
	if suggestions == nil {
		return nil, nil
	}

	suggestionsArr, ok := suggestions.([]any)
	if !ok {
		return nil, nil
	}

	var result []string
	for _, s := range suggestionsArr {
		if arr, ok := s.([]any); ok && len(arr) > 0 {
			if str, ok := arr[0].(string); ok {
				result = append(result, str)
			}
		}
	}

	return result, nil
}

// suggestRPC builds the IJ4APc call for one term.
func suggestRPC(term string) rpcCall {
	return rpcCall{
		id:      "IJ4APc",
		payload: fmt.Sprintf(`[[null,[%s],[10],[2],4]]`, jsonString(term)),
	}
}

// SuggestResult is one term's outcome in a SuggestMany fan-out.
type SuggestResult struct {
	Term        string
	Suggestions []string
	Err         error
}

// SuggestMany returns suggestions for many terms, packing up to
// maxRPCsPerRequest of them into each HTTP request.
//
// This is the operation the packing was measured on: 64 terms at a 200ms
// interval took 18.94s one at a time and 0.65s at 32 per request, with
// identical results. Keyword research over a term list is exactly the shape
// that made a request per term expensive.
//
// Results are positional: out[i] describes terms[i], whatever order Google
// answers in. A request that fails marks every term in that chunk.
func (c *Client) SuggestMany(ctx context.Context, terms []string, opts SuggestOptions) []SuggestResult {
	ctx, endTask := startTask(ctx, traceTaskSuggest)
	defer endTask()

	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}

	out := make([]SuggestResult, len(terms))
	for i, term := range terms {
		out[i].Term = term
	}

	base := 0
	for _, chunk := range chunked(terms, maxRPCsPerRequest) {
		// An empty entry is rejected here rather than sent. The singular form
		// validates up front, and a batch that quietly spent a request slot on
		// a blank id -- then reported "no data returned for " -- was the same
		// call behaving differently for no reason a caller could see.
		calls := make([]rpcCall, 0, len(chunk))
		slots := make([]int, 0, len(chunk))
		for i, term := range chunk {
			if term == "" {
				out[base+i].Err = errors.New("term is required")
				continue
			}
			calls = append(calls, suggestRPC(term))
			slots = append(slots, i)
		}
		if len(calls) == 0 {
			base += len(chunk)
			continue
		}

		payloads, err := c.batchCall(ctx, opts.Lang, opts.Country, calls)
		for j, i := range slots {
			if err != nil {
				out[base+i].Err = err
				continue
			}
			out[base+i].Suggestions, out[base+i].Err = parseSuggestPayload(payloads[j])
		}
		base += len(chunk)
	}
	return out
}
