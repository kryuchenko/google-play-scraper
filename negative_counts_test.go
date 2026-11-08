package googleplayscraper

import (
	"context"
	"testing"
)

// A negative Num means the default, like zero; in Search it used to reach the
// final slice and panic (slice bounds out of range [:-1]).
func TestSearchNegativeNumIsTheDefault(t *testing.T) {
	c := newMockClient(t, routePath("/store/search", readFixture(t, "search_page.html")))
	results, err := c.Search(context.Background(), SearchOptions{Term: "minecraft", Num: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || len(results) > 20 {
		t.Errorf("%d results for Num -1, want the default page of up to 20", len(results))
	}
}

// Developer treats a negative Num as its default too, rather than returning
// nothing.
func TestDeveloperNegativeNumIsTheDefault(t *testing.T) {
	c := newMockClient(t, routePath("/store/apps/dev", readFixture(t, "developer_page.html")))
	results, err := c.Developer(context.Background(), DeveloperOptions{DevID: "5700313618786177705", Num: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("Num -1 returned nothing; it should mean the default")
	}
}
