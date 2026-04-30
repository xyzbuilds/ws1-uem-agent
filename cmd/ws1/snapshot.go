package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/xyzbuilds/ws1-uem-agent/internal/api"
	"github.com/xyzbuilds/ws1-uem-agent/internal/approval"
	"github.com/xyzbuilds/ws1-uem-agent/internal/generated"
)

// findSnapshotOp returns the GET op (in the same section) whose path
// matches the resource-prefix of meta's path. Used to capture target
// state at approval time so the freshness check can detect drift.
//
// Heuristic: for a write op like
//
//	POST /devices/{deviceUuid}/commands/{commandName}
//
// the resource prefix is /devices/{deviceUuid}, so we look for any GET
// op whose path normalizes to the same shape (param names may differ —
// `mdmv2.commandsv2.execute` uses `{deviceUuid}` while
// `mdmv2.devicesv2.getbyuuid` uses `{uuid}` for the same position).
//
// Returns (zero, false) when no obvious snapshot can be derived. The
// caller treats that as "no freshness check" — approval is still
// captured but drift detection is skipped with a note in details.
func findSnapshotOp(meta generated.OpMeta) (snap generated.OpMeta, executeParamForSnapshot string, ok bool) {
	prefix := pathFirstResourcePrefix(meta.PathTemplate)
	if prefix == "" {
		return generated.OpMeta{}, "", false
	}
	prefixNorm := normalizePathParams(prefix)
	// Don't snapshot a GET against itself.
	if prefixNorm == normalizePathParams(meta.PathTemplate) && meta.HTTPMethod == "GET" {
		return generated.OpMeta{}, "", false
	}
	for _, op := range generated.Ops {
		if op.HTTPMethod != "GET" || op.Section != meta.Section {
			continue
		}
		if normalizePathParams(op.PathTemplate) != prefixNorm {
			continue
		}
		// Found. Identify which of meta's path params occupies the
		// position that matches snap's single path param. The snap's
		// path has exactly one {param}; meta's prefix ends at the same
		// {param} position.
		executeParam := lastPathParam(prefix)
		return op, executeParam, true
	}
	return generated.OpMeta{}, "", false
}

// pathFirstResourcePrefix returns the path up through its first {param}
// segment. That's the "resource by id" shape.
//
//	/devices/{uuid}                       -> /devices/{uuid}
//	/devices/{uuid}/commands/{cmd}        -> /devices/{uuid}
//	/devices/{uuid}/apps/{app}/install    -> /devices/{uuid}
//	/devices                              -> ""    (no params)
//	/devices/search                       -> ""    (no params)
func pathFirstResourcePrefix(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			return strings.Join(segs[:i+1], "/")
		}
	}
	return ""
}

// lastPathParam returns the name (without braces) of the last {param}
// segment in p, or "" if none exists.
func lastPathParam(p string) string {
	segs := strings.Split(p, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		s := segs[i]
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			return s[1 : len(s)-1]
		}
	}
	return ""
}

var pathParamRE = regexp.MustCompile(`\{[^}]+\}`)

// normalizePathParams replaces every {paramname} with the literal {} so
// two paths that differ only in param naming compare equal.
func normalizePathParams(p string) string {
	return pathParamRE.ReplaceAllString(p, "{}")
}

// captureSnapshotsFor resolves the snapshot op for the given write/dest
// op and fetches a snapshot for each target identified by the user's
// args. Returns the snapshots keyed by the target identifier value, the
// snapshot op metadata, ok flag, and any error.
//
// Snapshot keys are stringified target values (e.g. the UUID or integer
// ID). For ops with multiple path params, only the param at the
// resource-prefix position is used (e.g. for `/devices/{deviceUuid}/
// commands/{commandName}` we only look up by deviceUuid).
func captureSnapshotsFor(cli *api.Client, meta generated.OpMeta, args api.Args) (snaps map[string]map[string]any, snapMeta generated.OpMeta, ok bool, err error) {
	snapMeta, executeParam, ok := findSnapshotOp(meta)
	if !ok {
		return nil, generated.OpMeta{}, false, nil
	}
	// The snap op has exactly one path param.
	var snapParam string
	for _, p := range snapMeta.Parameters {
		if p.In == "path" {
			snapParam = p.Name
			break
		}
	}
	if snapParam == "" || executeParam == "" {
		return nil, snapMeta, false, nil
	}
	val, has := args[executeParam]
	if !has {
		return nil, snapMeta, false, nil
	}
	resp, err := cli.Do(context.Background(), snapMeta.Op, api.Args{snapParam: val})
	if err != nil {
		return nil, snapMeta, false, err
	}
	if resp.StatusCode == 404 {
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
	snaps = map[string]map[string]any{fmt.Sprint(val): raw}
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
