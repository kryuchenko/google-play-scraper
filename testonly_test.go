package googleplayscraper

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"net/url"
)

// Functions only the tests call. Each was production code once and is kept
// for the tests that still use it: as the reference a fuzz or differential
// test compares the production path against, to build a request a fixture
// stands for, or as the buffered baseline a benchmark measures the streamed
// path against. Living here, they cannot be mistaken for code the package
// runs, and deadcode -- which CI runs -- stays empty.

// appDataNode navigates a parsed Google Play app page to the core app-info node
// (ds:5 → [1][2]), the node the App parser and the Availability probe read.
// ok is false when the page has no ds:5 block or the [1][2] node is absent
// (e.g. a non-app page or a malformed response).
func appDataNode(body []byte) (appData any, ok bool) {
	ds5, found := dataBlock(body, "ds:5")
	if !found {
		return nil, false
	}
	appData = getPath(ds5, 1, 2)
	return appData, appData != nil
}

// buildReviewsBody renders the form body one reviews call puts on the wire.
//
// It composes the same two functions the request path composes -- reviewsRPC
// and buildFReq -- so it cannot describe a request the client does not make.
// It exists because the payload above is a format, and the tests that pin the
// format should be able to read it without standing up a server to catch it.
func buildReviewsBody(appID string, opts ReviewOptions) string {
	return "f.req=" + url.QueryEscape(buildFReq([]rpcCall{reviewsRPC(appID, opts)}))
}

// parseReviewsResponse decodes one whole reviews response, the way the
// request path does frame by frame.
func parseReviewsResponse(body []byte, appID string) (*ReviewsResult, error) {
	frames, err := decodeBatchFrames(body)
	if err != nil {
		return nil, err
	}
	payload, ok := soleFrame(frames)
	if !ok {
		payload = frames["0"]
	}
	return parseReviewsPayload(payload, appID)
}

// parseSearchResult decodes one qnKhOb search row.
func parseSearchResult(item any) SearchResult {
	return decodeResultRow(item, qnKhObRowPaths)
}

// gunzipIfNeeded decompresses data when it carries the gzip magic header,
// otherwise returns it unchanged. Sitemap shards are served as .xml.gz files
// (the gzip is the resource, not an HTTP transfer encoding, so the transport
// does not transparently decode it), but indexes are plain XML — detecting the
// magic bytes handles both without assuming.
func gunzipIfNeeded(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	// A corrupt stream surfaces from the read below, not from Close, so the
	// close error carries nothing a caller could act on.
	defer func() { _ = zr.Close() }()

	// Allocate once from the size the format already states.
	//
	// io.ReadAll grows from nothing, doubling and copying: decompressing an
	// 8MB shard that way allocates 16.7MB and takes 6.4ms, against 8.07MB and
	// 4.4ms when the buffer is the right size to begin with. A catalog sweep
	// does this 83,445 times.
	//
	// RFC 1952 puts ISIZE -- the uncompressed length modulo 2^32 -- in the
	// last four bytes. Modulo, so it is a hint and not a promise: a stream
	// over 4GB reports the remainder, and the buffer grows from there as
	// normal. For a sitemap shard it is exact.
	buf := bytes.NewBuffer(make([]byte, 0, gunzipHint(data)))
	if _, err := buf.ReadFrom(zr); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// gunzipHint reads the uncompressed size a gzip stream declares, clamped to
// something it is safe to allocate on that word alone.
func gunzipHint(data []byte) int {
	if len(data) < 4 {
		return 0
	}
	size := binary.LittleEndian.Uint32(data[len(data)-4:])
	if size > gunzipHintMax {
		return gunzipHintMax
	}
	return int(size)
}

// gunzipHintMax caps the pre-allocation taken from a gzip trailer.
//
// A shard decompresses to about 8MB. The cap exists because the trailer is
// attacker-controlled: it is four bytes at the end of the stream that anyone
// serving the response chooses, so an unbounded hint is an unbounded
// allocation from an unauthenticated source. Beyond the cap the buffer simply
// grows as it always did.
const gunzipHintMax = 64 << 20
