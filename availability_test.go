package googleplayscraper

import (
	"context"
	"reflect"
	"testing"
)

// appDataWith18 builds a minimal app-data array whose [18] node is the given
// value, padding the leading slots so getPath(appData, 18, ...) is reachable.
// It lets the classifier be tested without a full page fixture.
func appDataWith18(node18 interface{}) []interface{} {
	appData := make([]interface{}, 19)
	appData[18] = node18
	return appData
}

func TestClassifyAvailability(t *testing.T) {
	tests := []struct {
		name   string
		node18 interface{}
		want   Status
	}{
		{"available", []interface{}{float64(2)}, StatusAvailable},
		{"preregister", []interface{}{float64(1)}, StatusNotInRegion},
		{"region_locked_empty", []interface{}{}, StatusNotInRegion},
		{"nil_node", nil, StatusNotInRegion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyAvailability(appDataWith18(tt.node18))
			if got != tt.want {
				t.Errorf("classifyAvailability([18]=%v) = %v, want %v", tt.node18, got, tt.want)
			}
		})
	}
}

// TestEarlyAccessDoesNotSuppressAvailable proves that an installable early-access
// app is still reported Available. Early access lives in [18][2] (a string),
// while availability is decided independently by [18][0]; the two must not be
// conflated. No live early-access fixture is used: such apps are rare and short-
// lived, and a sweep of ~1300 live listings (top-charts across 12 game
// categories plus multi-country "early access"/"beta" searches) surfaced none.
// This synthetic node reproduces the exact shape an installable early-access
// listing has — [18] = [2, nil, "Early access"] — and pins that:
//   - classifyAvailability still returns StatusAvailable (reads [18][0]==2), and
//   - extractDistribution still sets EarlyAccessEnabled (reads the [18][2] string),
//
// so early access can never produce a false Available=false.
func TestEarlyAccessDoesNotSuppressAvailable(t *testing.T) {
	appData := appDataWith18([]interface{}{float64(2), nil, "Early access"})

	if got := classifyAvailability(appData); got != StatusAvailable {
		t.Errorf("classifyAvailability(early-access [18]=[2,nil,string]) = %v, want StatusAvailable", got)
	}

	var app App
	extractDistribution(&app, appData)
	if !app.EarlyAccessEnabled {
		t.Error("EarlyAccessEnabled is false, want true ([18][2] is a string)")
	}
	if app.Preregister {
		t.Error("Preregister is true, want false (early access is installable, [18][0]==2 not 1)")
	}
}

// TestClassifyAvailabilityOnFixtures runs the classifier against the captured
// app pages, pinning the available vs region-locked signal to real bytes.
func TestClassifyAvailabilityOnFixtures(t *testing.T) {
	available, ok := appDataNode(readFixture(t, "app_page.html"))
	if !ok {
		t.Fatal("app_page.html: app data node not found")
	}
	if got := classifyAvailability(available); got != StatusAvailable {
		t.Errorf("Maps fixture: got %v, want StatusAvailable", got)
	}

	locked, ok := appDataNode(readFixture(t, "app_unavailable_region.html"))
	if !ok {
		t.Fatal("app_unavailable_region.html: app data node not found")
	}
	if got := classifyAvailability(locked); got != StatusNotInRegion {
		t.Errorf("region-locked fixture: got %v, want StatusNotInRegion", got)
	}
}

func TestStatusString(t *testing.T) {
	tests := map[Status]string{
		StatusUnknown:     "unknown",
		StatusAvailable:   "available",
		StatusNotInRegion: "not_in_region",
		StatusNotFound:    "not_found",
		StatusFetchError:  "error",
	}
	for status, want := range tests {
		if got := status.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", status, got, want)
		}
	}
}

func TestNormalizeCountries(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"lowercases", []string{"US", "De"}, []string{"us", "de"}},
		{"dedupes_case_insensitive", []string{"us", "US", "us"}, []string{"us"}},
		{"trims_and_drops_empty", []string{" us ", "", "  "}, []string{"us"}},
		{"preserves_first_seen_order", []string{"de", "us", "de", "fr"}, []string{"de", "us", "fr"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeCountries(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("normalizeCountries(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestFinalizeAvailability(t *testing.T) {
	tests := []struct {
		name            string
		statuses        map[string]Status
		wantChecked     int
		wantGlobalRemvd bool
	}{
		{
			name:            "all_not_found_is_globally_removed",
			statuses:        map[string]Status{"us": StatusNotFound, "de": StatusNotFound},
			wantChecked:     2,
			wantGlobalRemvd: true,
		},
		{
			name:            "one_available_is_not_globally_removed",
			statuses:        map[string]Status{"us": StatusAvailable, "de": StatusNotFound},
			wantChecked:     2,
			wantGlobalRemvd: false,
		},
		{
			name:            "errors_excluded_from_checked",
			statuses:        map[string]Status{"us": StatusNotFound, "de": StatusFetchError},
			wantChecked:     1,
			wantGlobalRemvd: true,
		},
		{
			name:            "all_errors_not_globally_removed",
			statuses:        map[string]Status{"us": StatusFetchError, "de": StatusFetchError},
			wantChecked:     0,
			wantGlobalRemvd: false,
		},
		{
			name:            "region_locked_is_not_removed",
			statuses:        map[string]Status{"us": StatusNotInRegion, "de": StatusNotInRegion},
			wantChecked:     2,
			wantGlobalRemvd: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := AvailabilityResult{Statuses: tt.statuses}
			finalizeAvailability(&r)
			if r.Checked != tt.wantChecked {
				t.Errorf("Checked = %d, want %d", r.Checked, tt.wantChecked)
			}
			if r.GloballyRemoved != tt.wantGlobalRemvd {
				t.Errorf("GloballyRemoved = %v, want %v", r.GloballyRemoved, tt.wantGlobalRemvd)
			}
		})
	}
}

func TestAvailabilityRequiresAppID(t *testing.T) {
	c := NewClient()
	if _, err := c.Availability(context.Background(), "", AvailabilityOptions{}); err == nil {
		t.Error("expected error for empty appID, got nil")
	}
}

// TestAvailabilityConcurrentRecordsEveryCountry drives the Availability sweep
// through the shared worker pool at concurrency>1 over several countries. Run
// under -race it proves the rewritten probe path records results without a data
// race (all shared-map writes go through the mu-guarded record closure) and that
// every requested country ends up with a status — no probe is dropped by the
// pool.
func TestAvailabilityConcurrentRecordsEveryCountry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	countries := []string{"us", "gb", "de", "fr", "es", "it", "br", "in"}

	res, err := NewClient().Availability(context.Background(), "com.google.android.apps.maps", AvailabilityOptions{
		Countries:   countries,
		Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("Availability failed: %v", err)
	}

	if len(res.Statuses) != len(countries) {
		t.Fatalf("recorded %d statuses, want %d", len(res.Statuses), len(countries))
	}
	for _, country := range countries {
		if _, ok := res.Statuses[country]; !ok {
			t.Errorf("country %q has no recorded status", country)
		}
	}
}
