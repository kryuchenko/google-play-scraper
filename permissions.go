package googleplayscraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Permission is a single app permission entry.
type Permission struct {
	// Type is the permission group label, e.g. "Location", "Camera". Falls back
	// to "Common"/"Other" when the group is unnamed.
	Type string `json:"type" example:"Location"`
	// Permission is the human-readable permission description, e.g.
	// "precise location".
	Permission string `json:"permission" example:"precise location"`
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
	ctx, endTask := startTask(ctx, traceTaskPermissions)
	defer endTask()

	if opts.AppID == "" {
		return nil, fmt.Errorf("appID is required")
	}

	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}

	payloads, err := c.batchCall(ctx, opts.Lang, opts.Country,
		[]rpcCall{permissionsRPC(opts.AppID)})
	if err != nil {
		return nil, err
	}

	return parsePermissionsPayload(opts.AppID, payloads[0], opts.Short)
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

	var outer [][]any
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
	return parsePermissionsPayload("", dataStr, short)
}

// parsePermissionsPayload reads the inner JSON of one xdSrCf frame. It is split
// from the envelope handling above because a batched request decodes the
// envelope once and then has one payload per app to read.
func parsePermissionsPayload(appID, dataStr string, short bool) ([]Permission, error) {
	// An empty payload is Google saying it has nothing for this id, which for
	// this RPC means the app does not exist. It is not the same as an app that
	// declares no permissions -- that one still comes back as a structure, and
	// a caller auditing a list of ids has to be able to tell the two apart.
	//
	// Returning (nil, nil) here made a missing app indistinguishable from a
	// permissionless one all the way out to the CLI, where it printed nothing
	// and exited 0. The sibling parser in ws7gdc.go always treated this as an
	// error; the two are now consistent.
	if dataStr == "" {
		return nil, fmt.Errorf("no data returned for %s", shortenID(appID))
	}

	var data []any
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

		typeData, ok := data[permType].([]any)
		if !ok {
			continue
		}

		typeName := "Common"
		if permType == 1 {
			typeName = "Other"
		}

		for _, group := range typeData {
			groupArr, ok := group.([]any)
			if !ok || len(groupArr) < 3 {
				continue
			}

			// Group type at [0]
			groupType := toString(groupArr[0])
			if groupType == "" {
				groupType = typeName
			}

			// Permissions at [2]
			perms, ok := groupArr[2].([]any)
			if !ok {
				continue
			}

			for _, perm := range perms {
				permArr, ok := perm.([]any)
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

// permissionsRPC builds the xdSrCf call for one app. The payload is written
// plainly here and quoted by buildFReq; the previous version spelled the same
// bytes as a hand-written %5B%5C%22 string, which was correct and unreadable.
func permissionsRPC(appID string) rpcCall {
	return rpcCall{
		id:      "xdSrCf",
		payload: fmt.Sprintf(`[[null,[%s,7],[]]]`, jsonString(appID)),
	}
}

// PermissionsResult is one app's outcome in a PermissionsMany fan-out. Each app
// carries its own error: a batch where one app is unknown should still return
// the other thirty-one.
type PermissionsResult struct {
	AppID       string
	Permissions []Permission
	Err         error
}

// PermissionsMany fetches permissions for many apps, packing up to
// maxRPCsPerRequest of them into each HTTP request.
//
// The throttle meters requests, so this is the difference between len(appIDs)
// intervals and len(appIDs)/32 of them. Results are returned in the order asked
// for, whatever order Google answers in.
//
// Results are positional: out[i] describes appIDs[i]. A request that fails
// marks every app in that chunk with the error and leaves the rest intact.
func (c *Client) PermissionsMany(ctx context.Context, appIDs []string, opts PermissionsOptions) []PermissionsResult {
	ctx, endTask := startTask(ctx, traceTaskPermissions)
	defer endTask()

	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}

	out := make([]PermissionsResult, len(appIDs))
	for i, id := range appIDs {
		out[i].AppID = id
	}

	base := 0
	for _, chunk := range chunked(appIDs, maxRPCsPerRequest) {
		// An empty entry is rejected here rather than sent. The singular form
		// validates up front, and a batch that quietly spent a request slot on
		// a blank id -- then reported "no data returned for " -- was the same
		// call behaving differently for no reason a caller could see.
		calls := make([]rpcCall, 0, len(chunk))
		slots := make([]int, 0, len(chunk))
		for i, id := range chunk {
			if id == "" {
				out[base+i].Err = errors.New("appID is required")
				continue
			}
			calls = append(calls, permissionsRPC(id))
			slots = append(slots, i)
		}
		if len(calls) == 0 {
			base += len(chunk)
			continue
		}

		payloads, err := c.batchCall(ctx, opts.Lang, opts.Country, calls)
		for j, i := range slots {
			if err != nil {
				out[base+i].Err = err
				continue
			}
			out[base+i].Permissions, out[base+i].Err =
				parsePermissionsPayload(chunk[i], payloads[j], opts.Short)
		}
		base += len(chunk)
	}
	return out
}
