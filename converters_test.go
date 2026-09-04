package googleplayscraper

import (
	"reflect"
	"testing"
)

// These tests cover small conversion/navigation helpers whose edge cases are not
// already pinned by app_test.go / availability_test.go: devIDFromURL,
// extractScreenshots, and the int64/json.Number type branches of the numeric
// converters.

func TestDevIDFromURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://play.google.com/store/apps/dev?id=5700313618786177705", "5700313618786177705"},
		{"https://play.google.com/store/apps/developer?id=Google+LLC", "Google+LLC"},
		{"no-query-here", "no-query-here"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := devIDFromURL(tt.in); got != tt.want {
			t.Errorf("devIDFromURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestToInt64TypedBranches(t *testing.T) {
	// app_test covers float64/int/string/nil; pin the int64 branch here.
	if got := toInt64(int64(9000000000)); got != 9000000000 {
		t.Errorf("toInt64(int64) = %d, want 9000000000", got)
	}
	// An unhandled type falls through to 0.
	if got := toInt64([]any{1}); got != 0 {
		t.Errorf("toInt64(slice) = %d, want 0", got)
	}
}

func TestToIntUnhandledTypeIsZero(t *testing.T) {
	if got := toInt(true); got != 0 {
		t.Errorf("toInt(bool) = %d, want 0", got)
	}
}

func TestToFloat64UnhandledTypeIsZero(t *testing.T) {
	if got := toFloat64(true); got != 0 {
		t.Errorf("toFloat64(bool) = %v, want 0", got)
	}
}

func TestExtractScreenshots(t *testing.T) {
	in := []any{
		[]any{nil, nil, nil, []any{0, 0, "https://img/1.png"}},
		[]any{nil, nil, nil, []any{0, 0, "https://img/2.png"}},
		[]any{"too", "short"}, // skipped: len <= 3
	}
	got := extractScreenshots(in)
	want := []string{"https://img/1.png", "https://img/2.png"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractScreenshots = %v, want %v", got, want)
	}

	if got := extractScreenshots(42); got != nil {
		t.Errorf("extractScreenshots(non-array) = %v, want nil", got)
	}
}

// makeAppDataWith18 builds an app-data array long enough to address index 18,
// whose [18][0] carries the supplied availability marker. Shared by orchestrator
// tests that synthesize app pages.
func makeAppDataWith18(marker int) []any {
	data := make([]any, 19)
	data[18] = []any{float64(marker)}
	return data
}
