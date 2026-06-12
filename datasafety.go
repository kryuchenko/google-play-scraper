package googleplayscraper

import (
	"context"
	"fmt"
)

// DataSafetyEntry is a single collected or shared data item.
type DataSafetyEntry struct {
	// Data is the specific data item name, e.g. "Email address".
	Data string `json:"data" example:"Email address"`
	// Optional is true when collection/sharing of this item is optional
	// (false = required).
	Optional bool `json:"optional" example:"true"`
	// Purpose is the comma-separated list of declared purposes, e.g.
	// "App functionality, Analytics".
	Purpose string `json:"purpose" example:"App functionality, Account management"`
	// Type is the category this item belongs to, e.g. "Personal info".
	Type string `json:"type" example:"Personal info"`
}

// SecurityPractice is one declared security practice.
type SecurityPractice struct {
	// Practice is the practice headline, e.g. "Data is encrypted in transit".
	Practice string `json:"practice" example:"Data is encrypted in transit"`
	// Description is the practice detail; it may contain inline HTML (e.g. an
	// anchor tag) exactly as Google serves it.
	Description string `json:"description" example:"Your data is transferred over a secure connection"`
}

// DataSafety is the data-safety section parsed from the ds:3 block of GET
// /store/apps/datasafety.
type DataSafety struct {
	// SharedData lists data the app shares with third parties; may be empty.
	SharedData []DataSafetyEntry `json:"sharedData"`
	// CollectedData lists data the app collects; may be empty.
	CollectedData []DataSafetyEntry `json:"collectedData"`
	// SecurityPractices lists the developer's declared security practices.
	SecurityPractices []SecurityPractice `json:"securityPractices"`
	// PrivacyPolicyURL is the privacy-policy URL from the data-safety page.
	PrivacyPolicyURL string `json:"privacyPolicyUrl" format:"uri" example:"http://www.google.com/policies/privacy"`
}

// DataSafetyOptions configures the data safety request
type DataSafetyOptions struct {
	AppID   string
	Lang    string
	Country string
}

// DataSafety fetches data safety information for an app
func (c *Client) DataSafety(ctx context.Context, opts DataSafetyOptions) (*DataSafety, error) {
	if opts.AppID == "" {
		return nil, fmt.Errorf("appID is required")
	}

	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}

	reqURL := fmt.Sprintf("%s/store/apps/datasafety?id=%s&hl=%s&gl=%s",
		BaseURL, opts.AppID, opts.Lang, opts.Country)

	body, err := c.get(ctx, reqURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return parseDataSafetyPage(body)
}

func parseDataSafetyPage(body []byte) (*DataSafety, error) {
	dataBlocks := parseDataBlocks(body)

	ds3, ok := dataBlocks["ds:3"]
	if !ok {
		return nil, nil
	}

	result := &DataSafety{}

	// Data safety section: [1][2][1][138]
	safetySection := getPath(ds3, 1, 2, 1, 138)

	if safetySection != nil {
		// Shared data: [4][0][0]
		if sharedData := getPath(safetySection, 4, 0, 0); sharedData != nil {
			result.SharedData = parseDataEntries(sharedData)
		}

		// Collected data: [4][1][0]
		if collectedData := getPath(safetySection, 4, 1, 0); collectedData != nil {
			result.CollectedData = parseDataEntries(collectedData)
		}

		// Security practices: [9][2]
		if practices := getPath(safetySection, 9, 2); practices != nil {
			result.SecurityPractices = parseSecurityPractices(practices)
		}
	}

	// Privacy policy URL: [1][2][1][100][0][5][2]
	if ppURL := getPath(ds3, 1, 2, 1, 100, 0, 5, 2); ppURL != nil {
		result.PrivacyPolicyURL = toString(ppURL)
	}

	return result, nil
}

func parseDataEntries(data interface{}) []DataSafetyEntry {
	arr, ok := data.([]interface{})
	if !ok {
		return nil
	}

	var entries []DataSafetyEntry

	for _, item := range arr {
		itemArr, ok := item.([]interface{})
		if !ok {
			continue
		}

		// Type name at [0][1] (e.g., "Personal info", "Device or other IDs")
		typeName := toString(getPath(itemArr, 0, 1))

		// Data items at [4]
		dataItems := getPath(itemArr, 4)
		if dataItems == nil {
			continue
		}

		dataItemsArr, ok := dataItems.([]interface{})
		if !ok {
			continue
		}

		for _, dataItem := range dataItemsArr {
			dataItemArr, ok := dataItem.([]interface{})
			if !ok {
				continue
			}

			entry := DataSafetyEntry{
				Type: typeName,
			}

			// Data name at [0]
			if v := getPath(dataItemArr, 0); v != nil {
				entry.Data = toString(v)
			}

			// Optional flag at [1] (0 = required, 1 = optional)
			if v := getPath(dataItemArr, 1); v != nil {
				entry.Optional = toFloat64(v) == 1
			}

			// Purpose at [2]
			if v := getPath(dataItemArr, 2); v != nil {
				entry.Purpose = toString(v)
			}

			if entry.Data != "" {
				entries = append(entries, entry)
			}
		}
	}

	return entries
}

func parseSecurityPractices(data interface{}) []SecurityPractice {
	arr, ok := data.([]interface{})
	if !ok {
		return nil
	}

	var practices []SecurityPractice

	for _, item := range arr {
		itemArr, ok := item.([]interface{})
		if !ok || len(itemArr) < 3 {
			continue
		}

		practice := SecurityPractice{}

		// Practice name at [1]
		if v := getPath(itemArr, 1); v != nil {
			practice.Practice = toString(v)
		}

		// Description at [2][1]
		if v := getPath(itemArr, 2, 1); v != nil {
			practice.Description = toString(v)
		}

		if practice.Practice != "" {
			practices = append(practices, practice)
		}
	}

	return practices
}
