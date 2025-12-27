package googleplayscraper

import (
	"context"
	"testing"
	"time"
)

func TestPermissions(t *testing.T) {
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

func TestParsePermissionsResponse(t *testing.T) {
	// Test with empty/invalid response
	_, err := parsePermissionsResponse([]byte("invalid"), false)
	if err == nil {
		t.Error("Expected error for invalid response")
	}

	// Test with response that has no data after prefix skip
	_, err = parsePermissionsResponse([]byte(")]}'\n"), false)
	if err != nil {
		t.Logf("Got expected error: %v", err)
	}
}
