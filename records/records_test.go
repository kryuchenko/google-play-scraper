package records

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	gps "github.com/kryuchenko/google-play-scraper"
)

// The field names are the contract both interfaces share: a jq filter or a
// parser written against them must keep working. A rename shows up here as
// a diff against testdata/fields.txt; rerun with UPDATE_FIELDS=1 only when
// the change is meant.
func TestFieldNamesAreStable(t *testing.T) {
	var b strings.Builder
	for _, v := range []any{App{}, AppSummary{}, Review{}, Permissions{}, DataSafety{}, DataEntry{}, SecurityPractice{}, Availability{}, AppError{}} {
		ty := reflect.TypeOf(v)
		fmt.Fprintf(&b, "%s:", ty.Name())
		for i := range ty.NumField() {
			name, _, _ := strings.Cut(ty.Field(i).Tag.Get("json"), ",")
			fmt.Fprintf(&b, " %s", name)
		}
		b.WriteString("\n")
	}
	got := b.String()
	if os.Getenv("UPDATE_FIELDS") != "" {
		if err := os.WriteFile("testdata/fields.txt", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/fields.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("record fields changed:\n%s\nwant:\n%s", got, want)
	}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		err  error
		want Code
	}{
		{Invalid("bad"), CodeInvalid},
		{fmt.Errorf("wrapped: %w", Errorf(CodeBusy, "busy")), CodeBusy},
		{gps.ErrNotFound, CodeNotFound},
		{&gps.StatusError{Code: 404}, CodeNotFound},
		{gps.ErrBlocked, CodeRateLimited},
		{&gps.StatusError{Code: 429}, CodeRateLimited},
		{context.DeadlineExceeded, CodeTimeout},
		{context.Canceled, CodeCancelled},
		{errors.New("no data returned for com.a.b"), CodeNotListed},
		{errors.New("boom"), CodeStore},
	} {
		if got := Classify(c.err); got != c.want {
			t.Errorf("Classify(%v) = %s, want %s", c.err, got, c.want)
		}
	}
	if Classify(nil) != "" {
		t.Error("nil error classified")
	}
}

func TestExitCodes(t *testing.T) {
	for c, want := range map[Code]int{
		CodeInvalid: 2, CodeConfirmation: 2, CodeNotFound: 3, CodeNotListed: 3,
		CodeRateLimited: 4, CodeTimeout: 5, CodeBusy: 5, CodeCancelled: 130,
		CodeStore: 1, CodeInternal: 1,
	} {
		if got := c.ExitCode(); got != want {
			t.Errorf("%s exits %d, want %d", c, got, want)
		}
	}
	if CodeInvalid.Retryable() || !CodeRateLimited.Retryable() {
		t.Error("retryable set is wrong")
	}
}

func TestFromAppConciseAndDetailed(t *testing.T) {
	a := &gps.App{AppID: "com.a.b", Title: "A", Free: true, Price: 0, Currency: "USD", Score: 4.3449,
		Summary: "Tom &amp; Jerry", Description: "long", AdSupported: true, Updated: 1762041600}
	c := FromApp(a, false)
	if c.Currency != "" || c.Score != 4.34 || c.Summary != "Tom & Jerry" || c.Description != "" || c.AdSupported != nil || c.Updated != "2025-11-02" {
		t.Errorf("concise: %+v", c)
	}
	d := FromApp(a, true)
	if d.Description != "long" || d.AdSupported == nil || !*d.AdSupported {
		t.Errorf("detailed: %+v", d)
	}
	a.AdSupported = false // the record must not alias the source
	if !*d.AdSupported {
		t.Error("detailed record aliases the source app")
	}
}

func TestReviewConcise(t *testing.T) {
	r := FromReview(gps.Review{ID: "1", Text: "привет мир как дела", UserName: "u", ReplyText: "thanks", ReplyDate: time.Unix(1, 0)}, "ru")
	if r.Lang != "ru" || r.ReplyDate == "" || r.UserName != "u" {
		t.Errorf("full: %+v", r)
	}
	c := r.Concise(12)
	if c.Text != "привет мир…" || !c.Truncated || c.UserName != "" || c.ReplyText != "" || c.ReplyDate != "" {
		t.Errorf("concise: %+v", c)
	}
	b, _ := json.Marshal(FromReview(gps.Review{ID: "2", Text: "ok"}, ""))
	if strings.Contains(string(b), "reply_date") || strings.Contains(string(b), "lang") {
		t.Errorf("empty optional fields leaked: %s", b)
	}
}

func TestTruncate(t *testing.T) {
	if s, cut := truncate("short", 10); cut || s != "short" {
		t.Error("short text changed")
	}
	// The only space sits a quarter of the way in: by bytes it is past the
	// middle of a Cyrillic string, by runes it is not. The cut must keep the
	// full budget rather than stop at the space.
	cyr := strings.Repeat("я", 30) + " " + strings.Repeat("я", 100)
	if s, _ := truncate(cyr, 100); len([]rune(s)) != 101 {
		t.Errorf("cut at %d runes, want 100 plus the ellipsis", len([]rune(s))-1)
	}
	if s, _ := truncate(strings.Repeat("я", 20), 10); s != strings.Repeat("я", 10)+"…" {
		t.Errorf("no-space cut: %q", s)
	}
}

func TestFromAvailabilityAndPermissions(t *testing.T) {
	av := FromAvailability("com.a.b", gps.AvailabilityResult{
		Statuses: map[string]gps.Status{"us": gps.StatusAvailable, "de": gps.StatusAvailable, "ru": gps.StatusNotInRegion, "kp": gps.StatusNotFound},
		Errors:   map[string]error{"br": gps.ErrBlocked}, Checked: 4,
	})
	if strings.Join(av.Available, ",") != "de,us" || av.RegionLocked[0] != "ru" || av.NotListed[0] != "kp" || !strings.HasPrefix(av.Errors["br"], "rate_limited: ") {
		t.Errorf("availability: %+v", av)
	}
	p := FromPermissions("com.a.b", []gps.Permission{{Type: "Camera", Permission: "take pictures"}, {Type: "Camera", Permission: "take pictures"}, {Type: "Location", Permission: "precise"}})
	if len(p.Permissions["Camera"]) != 1 || len(p.Permissions["Location"]) != 1 {
		t.Errorf("permissions: %+v", p)
	}
}
