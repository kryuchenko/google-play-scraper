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

	frames, err := c.batchCallFrames(ctx, opts.Lang, opts.Country,
		[]rpcCall{permissionsRPC(opts.AppID)})
	if err != nil {
		return nil, err
	}
	// A frame that never arrived is not the app's answer. Read as an empty
	// payload it is reported as "no data returned for X", which reads as "this
	// app does not exist" -- a claim about the app made on the strength of a
	// short response.
	if !frames[0].Present {
		return nil, fmt.Errorf("no frame returned for %s", shortenID(opts.AppID))
	}

	return parsePermissionsPayload(opts.AppID, frames[0].Payload)
}

// parsePermissionsPayload reads the inner JSON of one xdSrCf frame. It is split
// from the envelope handling in batchCallFrames because a batched request
// decodes the envelope once and then has one payload per app to read.
func parsePermissionsPayload(appID, dataStr string) ([]Permission, error) {
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
// marks every app in that pack with the error and leaves the rest intact.
//
// The packs go out over WithConcurrency workers.
func (c *Client) PermissionsMany(ctx context.Context, appIDs []string, opts PermissionsOptions) []PermissionsResult {
	ctx, endTask := startTask(ctx, traceTaskPermissionsMany)
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

	c.fanOutPacked(ctx, opts.Lang, opts.Country, appIDs,
		func(i int, id string) (rpcCall, bool) {
			// An empty entry is rejected here rather than sent. The singular
			// form validates up front, and a batch that quietly spent a request
			// slot on a blank id -- then reported "no data returned for " --
			// was the same call behaving differently for no reason a caller
			// could see.
			if id == "" {
				out[i].Err = errors.New("appID is required")
				return rpcCall{}, false
			}
			return permissionsRPC(id), true
		},
		func(i int, id string, frame rpcFrame, err error) {
			switch {
			case err != nil:
				out[i].Err = err
			case !frame.Present:
				out[i].Err = fmt.Errorf("no frame returned for %s", shortenID(id))
			default:
				out[i].Permissions, out[i].Err = parsePermissionsPayload(id, frame.Payload)
			}
		})
	return out
}
