package googleplayscraper

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestPermissions(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	permissions, err := client.Permissions(ctx, PermissionsOptions{
		AppID:   "com.google.android.apps.translate",
		Lang:    "en",
		Country: "us",
	})

	if err != nil {
		t.Fatalf("Permissions() error = %v", err)
	}

	if len(permissions) == 0 {
		t.Fatal("Expected at least one permission")
	}

	t.Logf("Found %d permissions", len(permissions))

	// Check that permissions have required fields
	for i, perm := range permissions {
		if perm.Permission == "" {
			t.Errorf("Permission %d has empty permission name", i)
		}
		if i < 5 {
			t.Logf("Permission %d: Type=%s, Permission=%s", i, perm.Type, perm.Permission)
		}
	}
}

func TestPermissionsRequiresAppID(t *testing.T) {
	client := NewClient()
	ctx := context.Background()

	_, err := client.Permissions(ctx, PermissionsOptions{})

	if err == nil {
		t.Fatal("Expected error for missing appID")
	}
}

func TestPermissionsInstagram(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	permissions, err := client.Permissions(ctx, PermissionsOptions{
		AppID: "com.instagram.android",
	})

	if err != nil {
		t.Fatalf("Permissions() error = %v", err)
	}

	t.Logf("Instagram has %d permissions", len(permissions))

	// Instagram should have many permissions
	if len(permissions) < 5 {
		t.Errorf("Expected Instagram to have at least 5 permissions, got %d", len(permissions))
	}
}

// permissionsBatchMock answers a batched xdSrCf request by reading the app ids
// out of the request body and naming each one in its own frame, so a mismatched
// pairing shows up as the wrong app's permission rather than as a length error.
// Frames are served in reverse, which is the shape the live endpoint produces.
func permissionsBatchMock(t *testing.T, requests *int, fail func(n int) bool) routeFunc {
	t.Helper()
	appRe := regexp.MustCompile(`\[\\"([^"\\]+)\\",7\]`)
	return func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		*requests++
		if fail != nil && fail(*requests) {
			return mockResponse{Status: http.StatusInternalServerError}, true
		}
		body, _ := io.ReadAll(req.Body)
		decoded, _ := url.QueryUnescape(strings.TrimPrefix(string(body), "f.req="))

		var ids []string
		for _, m := range appRe.FindAllStringSubmatch(decoded, -1) {
			ids = append(ids, m[1])
		}

		byIndex := map[string]string{}
		var order []string
		for i, id := range ids {
			byIndex[fmt.Sprint(i)] = fmt.Sprintf(`[[["Location",null,[[null,%q]]]]]`, id)
			order = append([]string{fmt.Sprint(i)}, order...) // reversed
		}
		return mockResponse{Body: framesEnvelope("xdSrCf", byIndex, order)}, true
	}
}

// The point of the whole exercise: N apps cost ceil(N/32) requests, not N. The
// throttle meters requests, so this ratio is the speedup.
func TestPermissionsManyPacksAppsIntoFewRequests(t *testing.T) {
	var requests int
	c := newMockClient(t, permissionsBatchMock(t, &requests, nil))

	var appIDs []string
	for i := range 70 {
		appIDs = append(appIDs, fmt.Sprintf("com.example.app%d", i))
	}

	got := c.PermissionsMany(context.Background(), appIDs, PermissionsOptions{})
	if requests != 3 {
		t.Errorf("made %d requests for 70 apps, want 3 at a pack size of %d", requests, maxRPCsPerRequest)
	}
	if len(got) != len(appIDs) {
		t.Fatalf("got %d results, want %d", len(got), len(appIDs))
	}
	// Every app must carry its own permission, whatever order Google answered in.
	for i, r := range got {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.AppID, r.Err)
		}
		if r.AppID != appIDs[i] {
			t.Fatalf("result %d is for %s, want %s", i, r.AppID, appIDs[i])
		}
		if len(r.Permissions) != 1 || r.Permissions[0].Permission != appIDs[i] {
			t.Errorf("%s got %v -- answers paired to the wrong app", appIDs[i], r.Permissions)
		}
	}
}

// One failed request must not discard the apps that were fetched successfully:
// a sweep of thousands should lose a chunk, not the run.
func TestPermissionsManySurvivesAFailedChunk(t *testing.T) {
	var requests int
	c := newMockClient(t, permissionsBatchMock(t, &requests, func(n int) bool { return n == 2 }))

	var appIDs []string
	for i := range 70 {
		appIDs = append(appIDs, fmt.Sprintf("com.example.app%d", i))
	}

	got := c.PermissionsMany(context.Background(), appIDs, PermissionsOptions{})

	var failed, ok int
	for _, r := range got {
		if r.Err != nil {
			failed++
		} else {
			ok++
		}
	}
	if failed != maxRPCsPerRequest {
		t.Errorf("%d apps failed, want the %d in the failed chunk", failed, maxRPCsPerRequest)
	}
	if ok != len(appIDs)-maxRPCsPerRequest {
		t.Errorf("%d apps succeeded, want %d", ok, len(appIDs)-maxRPCsPerRequest)
	}
	// The surviving apps must still be the right ones.
	for i, r := range got {
		if r.Err == nil && r.Permissions[0].Permission != appIDs[i] {
			t.Fatalf("%s kept another app's data after a neighbouring chunk failed", appIDs[i])
		}
	}
}

func TestPermissionsManyOfNothing(t *testing.T) {
	var requests int
	c := newMockClient(t, permissionsBatchMock(t, &requests, nil))
	if got := c.PermissionsMany(context.Background(), nil, PermissionsOptions{}); len(got) != 0 {
		t.Errorf("got %d results for no apps", len(got))
	}
	if requests != 0 {
		t.Errorf("made %d requests for no apps", requests)
	}
}

// Against the live endpoint, batched answers must equal the one-at-a-time
// answers exactly. This is the test that makes the optimisation trustworthy:
// packing calls is only worth doing if it changes nothing but the request
// count, and only a real response can show that. The mock tests above prove the
// pairing logic; this proves Google agrees.
func TestPermissionsManyMatchesIndividualFetches(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	appIDs := []string{
		"com.spotify.music",
		"com.whatsapp",
		"com.google.android.youtube",
		"com.instagram.android",
		"com.ebay.mobile",
		"com.duolingo",
	}

	client := NewClient(WithThrottle(300 * time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	one := make([][]Permission, len(appIDs))
	for i, id := range appIDs {
		p, err := client.Permissions(ctx, PermissionsOptions{AppID: id})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if len(p) == 0 {
			t.Fatalf("%s returned no permissions; the comparison would be vacuous", id)
		}
		one[i] = p
	}

	many := client.PermissionsMany(ctx, appIDs, PermissionsOptions{})
	if len(many) != len(appIDs) {
		t.Fatalf("got %d results, want %d", len(many), len(appIDs))
	}

	for i, r := range many {
		if r.Err != nil {
			t.Errorf("%s: %v", r.AppID, r.Err)
			continue
		}
		if r.AppID != appIDs[i] {
			t.Fatalf("result %d is for %s, want %s", i, r.AppID, appIDs[i])
		}
		if !reflect.DeepEqual(one[i], r.Permissions) {
			t.Errorf("%s: batched result differs from the individual fetch\n  one:  %v\n  many: %v",
				appIDs[i], one[i], r.Permissions)
		}
	}
}
