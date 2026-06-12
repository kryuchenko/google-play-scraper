package apidoc

import googleplayscraper "github.com/kryuchenko/google-play-scraper"

// The aliases below re-export the root scraper's logical models so swaggo can
// build their schemas without forcing the root module to depend on swaggo. The
// schema names in the generated spec match these alias identifiers.

// App is the full app detail model parsed from the AF_initDataCallback ds:5
// block of GET /store/apps/details.
type App = googleplayscraper.App

// SearchResult is the compact app model returned by search, list, cluster and
// developer endpoints.
type SearchResult = googleplayscraper.SearchResult

// Review is a single user review.
type Review = googleplayscraper.Review

// Criteria is a per-aspect sub-rating attached to a Review.
type Criteria = googleplayscraper.Criteria

// ReviewsResult is a page of reviews plus the pagination token for the next
// batchexecute call.
type ReviewsResult = googleplayscraper.ReviewsResult

// DataSafety is the data-safety section parsed from the ds:3 block of
// GET /store/apps/datasafety.
type DataSafety = googleplayscraper.DataSafety

// DataSafetyEntry is one collected/shared data item within DataSafety.
type DataSafetyEntry = googleplayscraper.DataSafetyEntry

// SecurityPractice is one declared security practice within DataSafety.
type SecurityPractice = googleplayscraper.SecurityPractice

// Permission is a single app permission entry.
type Permission = googleplayscraper.Permission

// Status is the region-level availability of an app for a single probed country.
// It is an integer enum: 0 unknown, 1 available, 2 not_in_region, 3 not_found,
// 4 error (see x-enum-varnames in the generated schema).
type Status = googleplayscraper.Status

// AvailabilityResult is the aggregated result of an Availability sweep across
// countries (the synthetic GET /store/apps/details(availability) operation).
type AvailabilityResult = googleplayscraper.AvailabilityResult

// AvailabilityProgress is a single per-country progress event emitted during a
// sweep.
type AvailabilityProgress = googleplayscraper.AvailabilityProgress

// BatchExecuteEnvelope documents the raw POST /_/PlayStoreUi/data/batchexecute
// response wrapper. The body is NOT valid JSON as-is: it is prefixed with the
// XSSI guard `)]}'` and framed as a sequence of `wrb.fr` rows whose third field
// is the URL-encoded JSON payload the scraper actually parses. This type exists
// only to describe that envelope in the spec; it is not produced by the library.
type BatchExecuteEnvelope struct {
	// Prefix is the literal XSSI anti-hijacking guard that begins every
	// response: the four bytes `)]}'` followed by a newline.
	Prefix string `json:"prefix" example:")]}'"`
	// FrameType is the per-row tag; data rows use "wrb.fr".
	FrameType string `json:"frameType" example:"wrb.fr"`
	// RPCID echoes the requested rpcid for the data row.
	RPCID string `json:"rpcid" example:"oCPfdb"`
	// Payload is the inner, URL-encoded JSON string carrying the RPC result;
	// the scraper unescapes and decodes it into the logical model.
	Payload string `json:"payload"`
}

// FReqBody documents the single form field sent to
// POST /_/PlayStoreUi/data/batchexecute. It is encoded as
// application/x-www-form-urlencoded with exactly one field, `f.req`, whose value
// is a URL-encoded JSON array of the form
// [[[rpcid, "<inner-args-json>", null, "generic"]]].
type FReqBody struct {
	// FReq is the URL-encoded JSON envelope carrying the rpcid and its inner
	// argument string.
	FReq string `json:"f.req" example:"[[[\"IJ4APc\",\"[[null,[\\\"clash\\\"],[10],[2],4]]\"]]]"`
}
