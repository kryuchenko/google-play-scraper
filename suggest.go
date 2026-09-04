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

	frames, err := c.batchCallFrames(ctx, opts.Lang, opts.Country, []rpcCall{suggestRPC(opts.Term)})
	if err != nil {
		return nil, err
	}
	// "No suggestions" is an answer; a missing frame is not. A dropped response
	// otherwise reads as a keyword nobody searches for.
	if !frames[0].Present {
		return nil, fmt.Errorf("no frame returned for %s", shortenID(opts.Term))
	}

	return parseSuggestPayload(frames[0].Payload)
}

// parseSuggestPayload reads the inner JSON of one IJ4APc frame, split from the
// envelope handling in batchCallFrames so a batched request can decode the
// envelope once and then read one payload per term.
//
// An empty payload means no suggestions, which is a real answer for a term
// nobody searches for -- unlike the sibling parser in permissions.go, where the
// same emptiness means the app does not exist. The callers reject a frame that
// never arrived before reaching here, so emptiness only ever gets here as data.
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
// answers in. A request that fails marks every term in that pack.
//
// The packs go out over WithConcurrency workers.
func (c *Client) SuggestMany(ctx context.Context, terms []string, opts SuggestOptions) []SuggestResult {
	ctx, endTask := startTask(ctx, traceTaskSuggestMany)
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

	c.fanOutPacked(ctx, opts.Lang, opts.Country, terms,
		func(i int, term string) (rpcCall, bool) {
			// An empty entry is rejected here rather than sent. The singular
			// form validates up front, and a batch that quietly spent a request
			// slot on a blank id -- then reported "no data returned for " --
			// was the same call behaving differently for no reason a caller
			// could see.
			if term == "" {
				out[i].Err = errors.New("term is required")
				return rpcCall{}, false
			}
			return suggestRPC(term), true
		},
		func(i int, term string, frame rpcFrame, err error) {
			switch {
			case err != nil:
				out[i].Err = err
			case !frame.Present:
				// "No suggestions" is an answer; a missing frame is not. A
				// dropped response otherwise reads as an empty keyword.
				out[i].Err = fmt.Errorf("no frame returned for %s", shortenID(term))
			default:
				out[i].Suggestions, out[i].Err = parseSuggestPayload(frame.Payload)
			}
		})
	return out
}
