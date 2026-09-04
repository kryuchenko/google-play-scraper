package googleplayscraper

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// mockResponse is what a route returns for a matched request. A non-200 Status
// makes get/post produce a *StatusError; a nil Err with a 200 Status returns
// Body. Err, when set, is returned as a transport error (resp is nil), letting
// tests exercise the "do request" failure branch.
type mockResponse struct {
	Body   []byte
	Status int
	Err    error
	// Header lets a route model the response headers a retry policy reads,
	// Retry-After above all.
	Header http.Header
}

// routeFunc decides the response for a request. Returning ok=false means the
// route did not match; the router then tries the next route, falling back to a
// 404 if none match. A stateful routeFunc (closing over a call counter) models
// pagination: first call returns a page with a token, the next returns empty.
type routeFunc func(req *http.Request) (mockResponse, bool)

// routingTransport is a shared http.RoundTripper that dispatches a request to
// the first matching route. It matches by interface, independent of host, so it
// transparently intercepts the orchestrators (App/List/...) whose URLs are built
// from BaseURL — exactly like the httptest.Server-based request tests, but
// without a live listener.
type routingTransport struct {
	t      *testing.T
	routes []routeFunc
}

func (rt *routingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for _, route := range rt.routes {
		if resp, ok := route(req); ok {
			if resp.Err != nil {
				return nil, resp.Err
			}
			status := resp.Status
			if status == 0 {
				status = http.StatusOK
			}
			hdr := resp.Header
			if hdr == nil {
				hdr = make(http.Header)
			}
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader(string(resp.Body))),
				Header:     hdr,
				Request:    req,
			}, nil
		}
	}
	rt.t.Errorf("no mock route matched %s %s?%s", req.Method, req.URL.Path, req.URL.RawQuery)
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

// newMockClient builds a Client whose transport dispatches over the given
// routes, in order. The throttle is left at zero so tests run instantly.
func newMockClient(t *testing.T, routes ...routeFunc) *Client {
	t.Helper()
	rt := &routingTransport{t: t, routes: routes}
	return NewClient(WithHTTPClient(&http.Client{Transport: rt}))
}

// routePath matches any request whose URL path equals path, regardless of query
// string, and serves a fixed 200 body.
func routePath(path string, body []byte) routeFunc {
	return func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path == path {
			return mockResponse{Body: body}, true
		}
		return mockResponse{}, false
	}
}

// routePathStatus matches a path and serves a fixed status with no body, for
// error-branch coverage (404, 429, 500, ...).
func routePathStatus(path string, status int) routeFunc {
	return func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path == path {
			return mockResponse{Status: status}, true
		}
		return mockResponse{}, false
	}
}

// routeQuery matches a path plus a required subset of query parameters, serving
// a fixed 200 body. It lets a single endpoint (e.g. batchexecute) be split by
// rpcids, or a details page be split by id/gl.
func routeQuery(path string, query map[string]string, body []byte) routeFunc {
	return func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != path {
			return mockResponse{}, false
		}
		q := req.URL.Query()
		for k, want := range query {
			if q.Get(k) != want {
				return mockResponse{}, false
			}
		}
		return mockResponse{Body: body}, true
	}
}

// batchEnvelope wraps an inner JSON payload string in the wrb.fr frame that
// decodeBatchEnvelope expects, including the ")]}'" anti-hijacking prefix. The
// inner string is embedded verbatim, so callers pass already-escaped JSON (the
// same shape Google returns).
func batchEnvelope(rpcID, innerJSON string) []byte {
	escaped := strings.ReplaceAll(innerJSON, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return []byte(fmt.Sprintf(")]}'\n\n[[\"wrb.fr\",%q,\"%s\",null,null,null,\"generic\"]]", rpcID, escaped))
}

// droppedFrame is a well-formed batchexecute body that carries no wrb.fr frame
// at all: Google's `er` error frame. decodeBatchFrames parses it and returns an
// empty map, so every call in the request comes back Present=false.
//
// It is the distinction the rpcFrame type exists for. An emptyFrame() is an
// answer -- present, with a null payload -- and callers read data into it. This
// is the absence of an answer, and a caller that cannot tell the two apart
// reports a dropped response as a fact about the app.
func droppedFrame() []byte {
	return []byte(")]}'\n\n[[\"er\",null,null,null,null,[3]]]")
}

// htmlWithDataBlocks renders an HTML page carrying the given ds:N script blocks,
// each value being a JSON string. It mirrors the AF_initDataCallback layout that
// parseDataBlocks / scriptDataRegex consume, letting tests build minimal pages
// without committing a full HTML fixture.
func htmlWithDataBlocks(blocks map[string]string) []byte {
	var sb strings.Builder
	sb.WriteString("<!doctype html><html><body>")
	// A minimal WIZ_global_data block so extractWizData (used by the qnKhOb
	// pagination path) finds an f.sid/bl on mocked pages.
	sb.WriteString(`<script>window.WIZ_global_data = {"FdrFJe":"-1","cfb2h":"boq_test_p0"};</script>`)
	for key, data := range blocks {
		fmt.Fprintf(&sb,
			"<script>AF_initDataCallback({key: '%s', hash: '1', data:%s, sideChannel: {}});</script>",
			key, data,
		)
	}
	sb.WriteString("</body></html>")
	return []byte(sb.String())
}
