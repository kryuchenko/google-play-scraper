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
