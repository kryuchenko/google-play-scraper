package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// Permission represents an app permission
type Permission struct {
	Type       string `json:"type"`
	Permission string `json:"permission"`
}

// PermissionsOptions configures the permissions request
type PermissionsOptions struct {
	AppID   string
	Lang    string
	Country string
	Short   bool // Return only permission names
}

// Permissions fetches app permissions
func (c *Client) Permissions(ctx context.Context, opts PermissionsOptions) ([]Permission, error) {
	if opts.AppID == "" {
		return nil, fmt.Errorf("appID is required")
	}

	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}

	reqURL := fmt.Sprintf("%s/_/PlayStoreUi/data/batchexecute?rpcids=xdSrCf&hl=%s&gl=%s",
		BaseURL, opts.Lang, opts.Country)

	body := fmt.Sprintf(`f.req=%%5B%%5B%%5B%%22xdSrCf%%22%%2C%%22%%5B%%5Bnull%%2C%%5B%%5C%%22%s%%5C%%22%%2C7%%5D%%2C%%5B%%5D%%5D%%5D%%22%%2Cnull%%2C%%221%%22%%5D%%5D%%5D`,
		url.QueryEscape(opts.AppID))

	respBody, err := c.post(ctx, reqURL, "application/x-www-form-urlencoded;charset=UTF-8", body)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return parsePermissionsResponse(respBody, opts.Short)
}

func parsePermissionsResponse(body []byte, short bool) ([]Permission, error) {
	// Skip the )]}'  prefix
	start := 0
	for i := range body {
		if body[i] == '\n' {
			start = i + 1
			break
		}
	}

	if start >= len(body) {
		return nil, fmt.Errorf("invalid response")
	}

	var outer [][]interface{}
	if err := json.Unmarshal(body[start:], &outer); err != nil {
		return nil, err
	}

	if len(outer) == 0 || len(outer[0]) < 3 {
		return nil, nil
	}

	dataStr, ok := outer[0][2].(string)
	if !ok {
		return nil, nil
	}

	var data []interface{}
	if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
		return nil, err
	}

	if data == nil {
		return nil, nil
	}

	var permissions []Permission

	// Process common permissions (index 0) and other permissions (index 1)
	for permType := 0; permType <= 1; permType++ {
		if permType >= len(data) {
			continue
		}

		typeData, ok := data[permType].([]interface{})
		if !ok {
			continue
		}

		typeName := "Common"
		if permType == 1 {
			typeName = "Other"
		}

		for _, group := range typeData {
			groupArr, ok := group.([]interface{})
			if !ok || len(groupArr) < 3 {
				continue
			}

			// Group type at [0]
			groupType := toString(groupArr[0])
			if groupType == "" {
				groupType = typeName
			}

			// Permissions at [2]
			perms, ok := groupArr[2].([]interface{})
			if !ok {
				continue
			}

			for _, perm := range perms {
				permArr, ok := perm.([]interface{})
				if !ok || len(permArr) < 2 {
					continue
				}

				permName := toString(permArr[1])
				if permName != "" {
					permissions = append(permissions, Permission{
						Type:       groupType,
						Permission: permName,
					})
				}
			}
		}
	}

	return permissions, nil
}
