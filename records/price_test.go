package records

import (
	"encoding/json"
	"strings"
	"testing"

	gps "github.com/kryuchenko/google-play-scraper"
)

// A row that carries no price -- read without full details -- says nothing
// about free; it used to say "free": true for paid apps. A known price is
// reported as before.
func TestFromSearchResultPrice(t *testing.T) {
	for name, c := range map[string]struct {
		row  gps.SearchResult
		want string
	}{
		"unknown":    {gps.SearchResult{AppID: "com.paid", Free: true}, `{"app_id":"com.paid","title":"","developer":"","score":0}`},
		"known free": {gps.SearchResult{AppID: "com.free", Free: true, PriceKnown: true, Currency: "USD"}, `{"app_id":"com.free","title":"","developer":"","score":0,"free":true}`},
		"known paid": {gps.SearchResult{AppID: "com.paid", Price: 0.99, Currency: "USD", PriceKnown: true}, `{"app_id":"com.paid","title":"","developer":"","score":0,"free":false,"price":0.99,"currency":"USD"}`},
	} {
		b, err := json.Marshal(FromSearchResult(c.row))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(b)); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, c.want)
		}
	}
}
