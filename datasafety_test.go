package googleplayscraper

import (
	"context"
	"testing"
	"time"
)

func TestDataSafety(t *testing.T) {
	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	safety, err := client.DataSafety(ctx, DataSafetyOptions{
		AppID:   "com.instagram.android",
		Lang:    "en",
		Country: "us",
	})

	if err != nil {
		t.Fatalf("DataSafety() error = %v", err)
	}

	if safety == nil {
		t.Fatal("Expected non-nil result")
	}

	t.Logf("Collected data entries: %d", len(safety.CollectedData))
	t.Logf("Shared data entries: %d", len(safety.SharedData))
	t.Logf("Security practices: %d", len(safety.SecurityPractices))
	t.Logf("Privacy policy URL: %s", safety.PrivacyPolicyURL)

	// Instagram should have data collection info
	if len(safety.CollectedData) == 0 && len(safety.SharedData) == 0 {
		t.Log("Warning: No data collection/sharing info found")
	}

	// Show some entries
	for i, entry := range safety.CollectedData {
		if i >= 3 {
			break
		}
		t.Logf("Collected: Type=%s, Data=%s, Purpose=%s, Optional=%v",
			entry.Type, entry.Data, entry.Purpose, entry.Optional)
	}

	for i, practice := range safety.SecurityPractices {
		if i >= 3 {
			break
		}
		t.Logf("Practice: %s - %s", practice.Practice, practice.Description)
	}
}

func TestDataSafetyRequiresAppID(t *testing.T) {
	client := NewClient()
	ctx := context.Background()

	_, err := client.DataSafety(ctx, DataSafetyOptions{})

	if err == nil {
		t.Fatal("Expected error for missing appID")
	}
}

func TestDataSafetyGoogleApp(t *testing.T) {
	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	safety, err := client.DataSafety(ctx, DataSafetyOptions{
		AppID: "com.google.android.apps.maps",
	})

	if err != nil {
		t.Fatalf("DataSafety() error = %v", err)
	}

	if safety == nil {
		t.Fatal("Expected non-nil result")
	}

	t.Logf("Google Maps - Collected: %d, Shared: %d, Practices: %d",
		len(safety.CollectedData), len(safety.SharedData), len(safety.SecurityPractices))

	// Google Maps should have privacy policy
	if safety.PrivacyPolicyURL != "" {
		t.Logf("Privacy policy: %s", safety.PrivacyPolicyURL)
	}
}

func TestDataSafetySimpleApp(t *testing.T) {
	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Test with a simple app that might have minimal data collection
	safety, err := client.DataSafety(ctx, DataSafetyOptions{
		AppID: "com.google.android.calculator",
	})

	if err != nil {
		t.Fatalf("DataSafety() error = %v", err)
	}

	if safety == nil {
		t.Fatal("Expected non-nil result")
	}

	t.Logf("Calculator - Collected: %d, Shared: %d, Practices: %d",
		len(safety.CollectedData), len(safety.SharedData), len(safety.SecurityPractices))
}
