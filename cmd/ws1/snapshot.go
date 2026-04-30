package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xyzbuilds/ws1-uem-agent/internal/api"
	"github.com/xyzbuilds/ws1-uem-agent/internal/approval"
	"github.com/xyzbuilds/ws1-uem-agent/internal/generated"
)

// findSnapshotOp returns the GET op (in the same section) whose path
// matches the longest "resource prefix" of meta's path. Used to capture
// target state at approval time so the freshness check can detect drift.
//
// Heuristic: for a write op like
//
//	POST /devices/{deviceUuid}/commands/{commandName}
//
// the resource prefix is /devices/{deviceUuid}, so we look for any GET
// op whose PathTemplate equals that. If found, the snapshot op's single
// path param ({deviceUuid}) is populated from the user's args using the
// same name.
//
// Returns (zero, false) when no obvious snapshot can be derived. The
// caller treats that as "no freshness check" — the approval is still
// captured, but drift detection is skipped with a note in details.
func findSnapshotOp(meta generated.OpMeta) (generated.OpMeta, bool) {
	prefix := pathPrefixUpToLastParam(meta.PathTemplate)
	if prefix == "" || prefix == meta.PathTemplate {
		return generated.OpMeta{}, false
	}
	for _, op := range generated.Ops {
		if op.HTTPMethod == "GET" && op.Section == meta.Section && op.PathTemplate == prefix {
			return op, true
		}
	}
	return generated.OpMeta{}, false
}

// pathPrefixUpToLastParam returns the path up through its last {param}
// segment, or "" if the path has no params.
//
//	/devices/{uuid}/commands/{cmd}  -> /devices/{uuid}
//	/devices/{uuid}                 -> /devices/{uuid}     (no shorter prefix)
//	/devices                        -> ""                  (no params)
func pathPrefixUpToLastParam(p string) string {
	segs := strings.Split(p, "/")
	lastParam := -1
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			lastParam = i
		}
	}
	if lastParam < 0 {
		return ""
	}
	if lastParam == len(segs)-1 {
		// path itself ends in a param; no shorter prefix possible.
		return p
	}
	return strings.Join(segs[:lastParam+1], "/")
}

// captureSnapshotsFor resolves the snapshot op for the given write/dest
// op and fetches a snapshot for each target identified by the user's
// args. Returns the snapshots keyed by the target's path-arg value, plus
// a flag for whether the snapshot was actually captured.
//
// Snapshot keys are stringified path-arg values (e.g. the UUID or
// integer ID). For ops with multiple path params, only the param matching
// the snapshot op's path param is used.
func captureSnapshotsFor(cli *api.Client, meta generated.OpMeta, args api.Args) (snaps map[string]map[string]any, snapMeta generated.OpMeta, ok bool, err error) {
	snapMeta, ok = findSnapshotOp(meta)
	if !ok {
		return nil, generated.OpMeta{}, false, nil
	}
	snaps = map[string]map[string]any{}
	// The snapshot op has exactly one path param (because it's the
	// "resource by id" GET). Pull its value from args, by the same name.
	var paramName string
	for _, p := range snapMeta.Parameters {
		if p.In == "path" {
			paramName = p.Name
			break
		}
	}
	if paramName == "" {
		return nil, snapMeta, false, nil
	}
	val, has := args[paramName]
	if !has {
		return nil, snapMeta, false, nil
	}
	resp, err := cli.Do(context.Background(), snapMeta.Op, api.Args{paramName: val})
	if err != nil {
		return nil, snapMeta, false, err
	}
	if resp.StatusCode == 404 {
		// Target doesn't exist — caller will surface IDENTIFIER_NOT_FOUND.
		return nil, snapMeta, false, fmt.Errorf("target not found: %v", val)
	}
	if resp.StatusCode >= 400 {
		return nil, snapMeta, false, fmt.Errorf("snapshot fetch returned %d", resp.StatusCode)
	}
	var raw map[string]any
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &raw); err != nil {
			return nil, snapMeta, false, fmt.Errorf("snapshot parse: %w", err)
		}
	}
	snaps[fmt.Sprint(val)] = raw
	return snaps, snapMeta, true, nil
}

// targetsFromSnapshots builds approval.Target entries from the captured
// snapshots. Identifier shape (uuid vs int) is reflected in the
// DisplayLabel so the approval page is unambiguous.
func targetsFromSnapshots(snaps map[string]map[string]any) []approval.Target {
	out := make([]approval.Target, 0, len(snaps))
	for id, snap := range snaps {
		label := id
		if name, ok := snap["FriendlyName"].(string); ok && name != "" {
			label = fmt.Sprintf("%s (%s)", name, id)
		} else if name, ok := snap["userName"].(string); ok && name != "" {
			label = fmt.Sprintf("%s (%s)", name, id)
		} else if name, ok := snap["displayName"].(string); ok && name != "" {
			label = fmt.Sprintf("%s (%s)", name, id)
		}
		out = append(out, approval.Target{
			ID:           id,
			DisplayLabel: label,
			Snapshot:     snap,
		})
	}
	return out
}
