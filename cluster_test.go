package googleplayscraper

import (
	"context"
	"testing"
	"time"
)

func TestAbsoluteURL(t *testing.T) {
	cases := map[string]string{
		"/store/apps/collection/cluster?gsr=x": BaseURL + "/store/apps/collection/cluster?gsr=x",
		"https://play.google.com/foo":          "https://play.google.com/foo",
		"":                                     "",
	}
	for in, want := range cases {
		if got := absoluteURL(in); got != want {
			t.Errorf("absoluteURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWithLangCountry(t *testing.T) {
	if got := withLangCountry(BaseURL+"/x", "en", "us"); got != BaseURL+"/x?hl=en&gl=us" {
		t.Errorf("got %q", got)
	}
	if got := withLangCountry(BaseURL+"/x?gsr=tok", "es", "es"); got != BaseURL+"/x?gsr=tok&hl=es&gl=es" {
		t.Errorf("got %q", got)
	}
}

func TestParseClusterURLs(t *testing.T) {
	// section[21][1][0] = title, section[21][1][2][4][2] = cluster URL.
	// One section carries a cluster link; the other is an inlined grid (no link).
	cluster := `[null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,` +
		`[null,["Popular apps",null,[null,null,null,null,[null,null,"/store/apps/collection/cluster?gsr=ABC"]]]]]`
	grid := `[null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,` +
		`[null,["Inlined grid",null,null]]]`
	block := `AF_initDataCallback({key: 'ds:3', hash: '1', data:` +
		`[[null,[` + cluster + `,` + grid + `]]], sideChannel: {}});`
	clusters := parseClusterURLs([]byte(block))
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(clusters))
	}
	if clusters[0].Title != "Popular apps" {
		t.Errorf("title = %q", clusters[0].Title)
	}
	if clusters[0].URL != BaseURL+"/store/apps/collection/cluster?gsr=ABC" {
		t.Errorf("url = %q", clusters[0].URL)
	}

	// No cluster links -> empty result.
	if got := parseClusterURLs([]byte(`AF_initDataCallback({key: 'ds:3', data:[[]], sideChannel: {}});`)); len(got) != 0 {
		t.Errorf("expected 0 clusters, got %d", len(got))
	}
}

func TestClusterRequiresPath(t *testing.T) {
	_, err := NewClient().Cluster(context.Background(), ClusterOptions{})
	if err == nil {
		t.Error("expected error when Path is empty")
	}
}

// TestClusterURLsCategory verifies cluster discovery for a category page.
func TestClusterURLsCategory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	clusters, err := client.ClusterURLs(ctx, ClusterURLsOptions{Category: CategoryGameAction})
	if err != nil {
		t.Fatalf("ClusterURLs() error = %v", err)
	}
	if len(clusters) == 0 {
		t.Fatal("expected at least one cluster for GAME_ACTION")
	}
	t.Logf("GAME_ACTION: %d clusters", len(clusters))
	for _, c := range clusters {
		t.Logf("  %q -> %s", c.Title, c.URL)
		if c.Title == "" || c.URL == "" {
			t.Error("cluster missing title or URL")
		}
	}
}

// TestClusterURLsTop verifies cluster discovery from the top-charts page.
func TestClusterURLsTop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	clusters, err := client.ClusterURLs(ctx, ClusterURLsOptions{})
	if err != nil {
		t.Fatalf("ClusterURLs() error = %v", err)
	}
	if len(clusters) == 0 {
		t.Fatal("expected at least one cluster on top page")
	}
	t.Logf("top: %d clusters; first %q", len(clusters), clusters[0].Title)
}

// TestClusterApps fetches the apps of the first cluster of a category.
func TestClusterApps(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	clusters, err := client.ClusterURLs(ctx, ClusterURLsOptions{Category: CategoryGameAction})
	if err != nil {
		t.Fatalf("ClusterURLs() error = %v", err)
	}
	if len(clusters) == 0 {
		t.Skip("no clusters to fetch")
	}

	apps, err := client.Cluster(ctx, ClusterOptions{Path: clusters[0].URL, Num: 100})
	if err != nil {
		t.Fatalf("Cluster() error = %v", err)
	}
	if len(apps) == 0 {
		t.Fatal("expected at least one app from cluster")
	}
	t.Logf("cluster %q: %d apps; first %s (%s)", clusters[0].Title, len(apps), apps[0].Title, apps[0].AppID)

	for _, a := range apps {
		if a.AppID == "" {
			t.Error("app missing AppID")
		}
	}
}
