package googleplayscraper

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

// An id is a query value and is escaped like one. Interpolated as given, an
// id holding & rewrote the request: "x&gl=de" asked the German storefront
// for x.
func TestPageRequestsEscapeTheID(t *testing.T) {
	const id = "x&gl=de"
	for name, fetch := range map[string]func(c *Client) error{
		"App": func(c *Client) error {
			_, err := c.App(context.Background(), id, AppOptions{})
			return err
		},
		"Similar": func(c *Client) error {
			_, err := c.Similar(context.Background(), SimilarOptions{AppID: id})
			return err
		},
		"DataSafety": func(c *Client) error {
			_, err := c.DataSafety(context.Background(), DataSafetyOptions{AppID: id})
			return err
		},
		"PCApp": func(c *Client) error {
			_, err := c.PCApp(context.Background(), id, AppOptions{})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			var asked []url.Values
			c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
				asked = append(asked, req.URL.Query())
				return mockResponse{Status: http.StatusNotFound}, true
			})
			_ = fetch(c)
			if len(asked) == 0 {
				t.Fatal("no request was made")
			}
			for _, q := range asked {
				if q.Get("id") != id || q.Get("gl") != "us" {
					t.Errorf("asked id=%q gl=%q, want id=%q gl=us", q.Get("id"), q.Get("gl"), id)
				}
			}
		})
	}
}
