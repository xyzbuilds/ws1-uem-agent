package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/xyzbuilds/ws1-uem-agent/internal/api"
	"github.com/xyzbuilds/ws1-uem-agent/internal/approval"
	"github.com/xyzbuilds/ws1-uem-agent/internal/audit"
	"github.com/xyzbuilds/ws1-uem-agent/internal/auth"
	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
	"github.com/xyzbuilds/ws1-uem-agent/internal/generated"
	"github.com/xyzbuilds/ws1-uem-agent/internal/policy"
)

// registerSectionCommands walks the compiled-in operation index and
// dynamically attaches a section -> tag -> verb subcommand tree to root.
// Every op exposed by the spec becomes a CLI command of the shape
//
//	ws1 <section> <tag> <verb> [--<param> <value>] ...
//
// Flags are derived from each op's parameter metadata. Path params are
// required; query params are optional. Body shape is permissive: any
// argv not declared as a path/query param is folded into a JSON body.
//
// Read-class ops execute and emit a read envelope. Write/destructive
// ops route through the approval + freshness + audit flow (see
// runStateChangingOp).
func registerSectionCommands(root *cobra.Command) {
	bySection := map[string]map[string]map[string]generated.OpMeta{}
	for id, meta := range generated.Ops {
		_ = id
		if bySection[meta.Section] == nil {
			bySection[meta.Section] = map[string]map[string]generated.OpMeta{}
		}
		if bySection[meta.Section][meta.Tag] == nil {
			bySection[meta.Section][meta.Tag] = map[string]generated.OpMeta{}
		}
		bySection[meta.Section][meta.Tag][meta.Verb] = meta
	}

	sections := make([]string, 0, len(bySection))
	for s := range bySection {
		sections = append(sections, s)
	}
	sort.Strings(sections)

	for _, section := range sections {
		secCmd := &cobra.Command{
			Use:   section,
			Short: humanSectionShort(section),
		}
		tagNames := make([]string, 0, len(bySection[section]))
		for t := range bySection[section] {
			tagNames = append(tagNames, t)
		}
		sort.Strings(tagNames)
		for _, tag := range tagNames {
			tagCmd := &cobra.Command{
				Use:   tag,
				Short: fmt.Sprintf("Operations in %s.%s", section, tag),
			}
			verbs := make([]string, 0, len(bySection[section][tag]))
			for v := range bySection[section][tag] {
				verbs = append(verbs, v)
			}
			sort.Strings(verbs)
			for _, verb := range verbs {
				tagCmd.AddCommand(buildOpCommand(bySection[section][tag][verb]))
			}
			secCmd.AddCommand(tagCmd)
		}
		root.AddCommand(secCmd)
	}
}

// humanSectionShort returns a human-readable description for the section
// command's --help. The mapping is small and stable across spec syncs.
func humanSectionShort(section string) string {
	switch section {
	case "mamv1", "mamv2":
		return "MAM API operations (mobile application management)"
	case "mcmv1":
		return "MCM API operations (mobile content management)"
	case "mdmv1", "mdmv2", "mdmv3", "mdmv4":
		return "MDM API operations (mobile device management)"
	case "memv1":
		return "MEM API operations (mobile email management)"
	case "systemv1", "systemv2":
		return "System API operations (users, OGs, admins, roles)"
	default:
		return "Operations under " + section
	}
}

// buildOpCommand constructs one cobra.Command for a single op. Flags are
// derived from meta.Parameters; the Run func collects flag values into an
// api.Args map and dispatches to runOp.
//
// Path params become required flags. Query params are optional. Verbatim
// param names from the spec (often camelCase) are used so a flag's name
// matches the spec exactly; this lets agents map argv 1:1 with the op
// description visible via `ws1 ops describe <op>`.
func buildOpCommand(meta generated.OpMeta) *cobra.Command {
	stringFlags := map[string]*string{}
	intFlags := map[string]*int{}
	boolFlags := map[string]*bool{}

	cmd := &cobra.Command{
		Use:   meta.Verb,
		Short: meta.Summary,
		Long:  longDescriptionFor(meta),
	}

	requiredFlags := []string{}
	for _, p := range meta.Parameters {
		desc := p.Description
		if desc == "" {
			desc = fmt.Sprintf("(%s, %s)", p.In, p.Type)
		}
		switch p.Type {
		case "integer":
			v := new(int)
			intFlags[p.Name] = v
			cmd.Flags().IntVar(v, p.Name, 0, desc)
		case "boolean":
			v := new(bool)
			boolFlags[p.Name] = v
			cmd.Flags().BoolVar(v, p.Name, false, desc)
		default:
			v := new(string)
			stringFlags[p.Name] = v
			cmd.Flags().StringVar(v, p.Name, "", desc)
		}
		if p.In == "path" {
			requiredFlags = append(requiredFlags, p.Name)
		}
	}
	for _, name := range requiredFlags {
		_ = cmd.MarkFlagRequired(name)
	}

	cmd.Run = func(cmd *cobra.Command, args []string) {
		// Collect flag values into an api.Args. Skip flags the user
		// didn't pass (cobra Changed() check) — that lets the API see
		// "field absent" rather than "field = zero value", which matters
		// for query params with defaults.
		out := api.Args{}
		for name, ptr := range stringFlags {
			if cmd.Flags().Changed(name) {
				out[name] = *ptr
			}
		}
		for name, ptr := range intFlags {
			if cmd.Flags().Changed(name) {
				out[name] = *ptr
			}
		}
		for name, ptr := range boolFlags {
			if cmd.Flags().Changed(name) {
				out[name] = *ptr
			}
		}
		runOp(meta, out)
	}
	return cmd
}

// longDescriptionFor pulls together the spec description plus a single
// trailing line summarizing classification (read/write/destructive,
// reversibility, identifier shape) so an agent reading --help sees the
// safety contour without having to call `ws1 ops describe`.
func longDescriptionFor(meta generated.OpMeta) string {
	pol := loadActivePolicy()
	entry := pol.Classify(meta.Op)

	var b strings.Builder
	if meta.Description != "" {
		b.WriteString(meta.Description)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "Operation: %s\n", meta.Op)
	fmt.Fprintf(&b, "Method:    %s %s\n", meta.HTTPMethod, meta.PathTemplate)
	fmt.Fprintf(&b, "Class:     %s", entry.Class)
	if entry.Reversible != "" && entry.Reversible != policy.ReversibleUnknown {
		fmt.Fprintf(&b, " (reversible: %s)", entry.Reversible)
	}
	if entry.Approval == policy.ApprovalAlwaysRequired {
		b.WriteString(", browser approval always required")
	}
	if entry.Synthetic {
		b.WriteString("\nWARNING: this op is not classified in operations.policy.yaml; treated as destructive (fail-closed).")
	}
	return b.String()
}

// runOp is the single dispatch point for every generated command. It
// classifies the op via policy, then routes:
//   - read class: directly execute and emit the read envelope
//   - write / destructive: route through runStateChangingOp (Phase B;
//     until that's wired, we surface a clear stub envelope)
func runOp(meta generated.OpMeta, args api.Args) {
	start := time.Now()
	cli, og, errEnv := buildAPIClient()
	if errEnv != nil {
		emitAndExit(errEnv.WithDuration(time.Since(start)))
		return
	}

	// Auto-inject the active OG into the args map IF the op declares an
	// `organizationgroupid` query param and the user didn't already
	// supply one. Avoids forcing every read invocation to repeat --og.
	if og != "" {
		for _, p := range meta.Parameters {
			if p.In != "query" {
				continue
			}
			if !strings.EqualFold(p.Name, "organizationgroupid") &&
				!strings.EqualFold(p.Name, "lgid") {
				continue
			}
			if _, has := args[p.Name]; !has {
				args[p.Name] = og
			}
		}
	}

	pol := loadActivePolicy()
	entry := pol.Classify(meta.Op)

	switch entry.Class {
	case policy.ClassRead:
		executeRead(cli, meta, args, start)
		return
	case policy.ClassWrite, policy.ClassDestructive:
		runStateChangingOp(cli, meta, entry, args, start)
		return
	default:
		emitAndExit(envelope.NewError(meta.Op, envelope.CodeInternalError,
			"unhandled op class: "+string(entry.Class)).
			WithDuration(time.Since(start)))
	}
}

// executeRead runs an HTTP GET (or HEAD/OPTIONS) op and emits a read
// envelope. The response body is unmarshaled as `any` and surfaced
// verbatim under data; pagination meta is populated when the response
// looks like a search result (Total + array sibling).
func executeRead(cli *api.Client, meta generated.OpMeta, args api.Args, start time.Time) {
	resp, err := cli.Do(context.Background(), meta.Op, args)
	if err != nil {
		emitAndExit(envelope.NewError(meta.Op, envelope.CodeNetworkError, err.Error()).
			WithDuration(time.Since(start)))
		return
	}
	if resp.StatusCode >= 400 {
		emitAndExit(httpErrorEnvelope(meta.Op, resp).WithDuration(time.Since(start)))
		return
	}
	var raw any
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &raw); err != nil {
			emitAndExit(envelope.NewError(meta.Op, envelope.CodeInternalError,
				"parse: "+err.Error()).WithDuration(time.Since(start)))
			return
		}
	}
	env := envelope.New(meta.Op).WithData(raw).WithDuration(time.Since(start))
	if c, p, ps, more, ok := extractPaginationMeta(raw); ok {
		env = env.WithPagination(c, p, ps, more)
	}
	emitAndExit(env)
}

// extractPaginationMeta pulls (count, page, page_size, has_more) from a
// search-shaped response object. WS1 read endpoints typically return a
// shape like {"Total": <n>, "Page": <n>, "PageSize": <n>, "Devices":
// [...]} where the array has the same name as the resource. We treat
// any object with a `Total` numeric field at root as pageable.
func extractPaginationMeta(raw any) (count, page, pageSize int, hasMore, ok bool) {
	m, isMap := raw.(map[string]any)
	if !isMap {
		return 0, 0, 0, false, false
	}
	totalF, hasTotal := numField(m, "Total", "total")
	pageF, _ := numField(m, "Page", "page")
	pageSizeF, _ := numField(m, "PageSize", "page_size", "pagesize")
	if !hasTotal {
		return 0, 0, 0, false, false
	}
	count = int(totalF)
	page = int(pageF)
	pageSize = int(pageSizeF)
	if pageSize > 0 {
		hasMore = (page+1)*pageSize < count
	}
	return count, page, pageSize, hasMore, true
}

func numField(m map[string]any, names ...string) (float64, bool) {
	for _, n := range names {
		if v, ok := m[n]; ok {
			if f, ok := v.(float64); ok {
				return f, true
			}
		}
	}
	return 0, false
}

// runStateChangingOp is the write/destructive path. Per spec section 7.1:
//
//  1. Verify the active profile can perform the op's class.
//  2. Capture a target snapshot via the corresponding GET op (if one
//     can be auto-derived from the path).
//  3. If approval is required (destructive always; write above blast
//     threshold), launch the browser approval flow.
//  4. Re-fetch the target; freshness-check vs. the snapshot. Drift ->
//     STALE_RESOURCE; approval is NOT consumed.
//  5. Execute the op.
//  6. Append a hash-chained audit entry.
//  7. Emit the envelope.
//
// --dry-run skips approval + execution and returns a "would_target"
// envelope so an agent can preflight without side effects.
func runStateChangingOp(cli *api.Client, meta generated.OpMeta, entry policy.Entry, args api.Args, start time.Time) {
	// 1. Capability check.
	activeName, _ := auth.Active()
	if globalFlags.profile != "" {
		activeName = globalFlags.profile
	}
	prof := &auth.Profile{Name: activeName}
	if !prof.Can(string(entry.Class)) {
		emitAndExit(envelope.NewError(meta.Op, envelope.CodeAuthInsufficientForOp,
			fmt.Sprintf("profile %q cannot perform %s ops", activeName, entry.Class)).
			WithErrorDetails(map[string]any{
				"active_profile":  activeName,
				"operation_class": string(entry.Class),
			}).WithDuration(time.Since(start)))
		return
	}

	// 2. Snapshot the target if we can find a matching GET. For ops
	// with no obvious snapshot path (bulk endpoints, ops on collections,
	// etc.) we proceed without freshness check and note that in details.
	snaps, _, snapped, snapErr := captureSnapshotsFor(cli, meta, args)
	if snapErr != nil {
		emitAndExit(envelope.NewError(meta.Op, envelope.CodeIdentifierNotFound,
			snapErr.Error()).WithDuration(time.Since(start)))
		return
	}

	// 3. Approval if required and not dry-run.
	needApproval := entry.RequiresApproval(targetCountFor(args))
	var approvalRequestID string
	if needApproval && !globalFlags.dryRun {
		var targets []approval.Target
		if snapped {
			targets = targetsFromSnapshots(snaps)
		} else {
			// No snapshot path — surface argv-derived target so the
			// approval page still has something concrete to show.
			targets = []approval.Target{{
				ID:           pathArgsAsLabel(meta, args),
				DisplayLabel: pathArgsAsLabel(meta, args),
				Snapshot:     map[string]any{"note": "snapshot op not auto-derivable; freshness check skipped"},
			}}
		}
		og, _ := auth.ResolveOG(globalFlags.og)
		req := approval.Request{
			Operation:     meta.Op,
			OperationDesc: meta.Summary,
			Class:         string(entry.Class),
			Reversibility: string(entry.Reversible),
			Profile:       activeName,
			Tenant:        og,
			Targets:       targets,
			Args:          map[string]any(args),
		}
		res, err := approval.Run(context.Background(), req)
		if err != nil {
			emitAndExit(envelope.NewError(meta.Op, envelope.CodeInternalError,
				err.Error()).WithDuration(time.Since(start)))
			return
		}
		switch res.Outcome {
		case approval.OutcomeApproved:
			approvalRequestID = res.RequestID
		case approval.OutcomeDenied:
			emitAndExit(envelope.NewError(meta.Op, envelope.CodeApprovalDenied,
				"User denied approval in browser").
				WithErrorDetails(map[string]any{"request_id": res.RequestID}).
				WithDuration(time.Since(start)))
			return
		case approval.OutcomeTimeout:
			emitAndExit(envelope.NewError(meta.Op, envelope.CodeApprovalTimeout,
				"Approval window elapsed without a decision").
				WithErrorDetails(map[string]any{"request_id": res.RequestID}).
				WithDuration(time.Since(start)))
			return
		default:
			emitAndExit(envelope.NewError(meta.Op, envelope.CodeInternalError,
				"unexpected approval outcome: "+res.Outcome.String()).
				WithDuration(time.Since(start)))
			return
		}
	}

	// 4. Freshness check (only when we have a snapshot AND approval ran).
	if snapped && needApproval && !globalFlags.dryRun {
		current, _, ok, err := captureSnapshotsFor(cli, meta, args)
		if err != nil || !ok {
			emitAndExit(envelope.NewError(meta.Op, envelope.CodeStaleResource,
				"could not re-fetch target for freshness check").
				WithDuration(time.Since(start)))
			return
		}
		for id, before := range snaps {
			now, ok := current[id]
			if !ok {
				emitAndExit(envelope.NewError(meta.Op, envelope.CodeStaleResource,
					fmt.Sprintf("target %s disappeared between approval and execute", id)).
					WithDuration(time.Since(start)))
				return
			}
			if err := approval.FreshnessCheck(before, now); err != nil {
				details := map[string]any{}
				var d *approval.DriftError
				if errors.As(err, &d) {
					details = d.AsDetails()
				}
				emitAndExit(envelope.NewError(meta.Op, envelope.CodeStaleResource, err.Error()).
					WithErrorDetails(details).
					WithDuration(time.Since(start)))
				return
			}
		}
	}

	// 5. Execute (or dry-run).
	if globalFlags.dryRun {
		emitAndExit(envelope.New(meta.Op).WithData(map[string]any{
			"dry_run": true,
			"args":    args,
			"class":   string(entry.Class),
		}).WithDuration(time.Since(start)))
		return
	}
	resp, err := cli.Do(context.Background(), meta.Op, args)
	if err != nil {
		emitAndExit(envelope.NewError(meta.Op, envelope.CodeNetworkError, err.Error()).
			WithDuration(time.Since(start)))
		return
	}
	if resp.StatusCode >= 400 {
		emitAndExit(httpErrorEnvelope(meta.Op, resp).WithDuration(time.Since(start)))
		return
	}
	var raw any
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &raw); err != nil {
			emitAndExit(envelope.NewError(meta.Op, envelope.CodeInternalError, err.Error()).
				WithDuration(time.Since(start)))
			return
		}
	}

	// 6. Audit.
	auditEntry := writeAuditRow(meta, entry, args, "ok", approvalRequestID, time.Since(start), activeName)

	// 7. Envelope.
	env := envelope.New(meta.Op).WithData(raw).WithDuration(time.Since(start))
	if approvalRequestID != "" {
		env = env.WithApproval(approvalRequestID)
	}
	if auditEntry != "" {
		env = env.WithAudit(auditEntry)
	}
	emitAndExit(env)
}

// targetCountFor approximates how many entities this invocation will hit.
// For single-target ops (one path param), it's 1. For bulk ops with a
// device_uuids/device_ids array argument, it's len(array). For everything
// else (collection-only ops, search), it's 1.
func targetCountFor(args api.Args) int {
	for _, key := range []string{"device_uuids", "device_ids", "uuids", "ids"} {
		if v, ok := args[key]; ok {
			if arr, ok := v.([]any); ok {
				return len(arr)
			}
			if arr, ok := v.([]string); ok {
				return len(arr)
			}
			if arr, ok := v.([]int); ok {
				return len(arr)
			}
		}
	}
	return 1
}

// pathArgsAsLabel produces a short human label like "deviceUuid=f3d4..."
// for cases where we have to render a target without a snapshot.
func pathArgsAsLabel(meta generated.OpMeta, args api.Args) string {
	parts := []string{}
	for _, p := range meta.Parameters {
		if p.In != "path" {
			continue
		}
		if v, ok := args[p.Name]; ok {
			parts = append(parts, fmt.Sprintf("%s=%v", p.Name, v))
		}
	}
	if len(parts) == 0 {
		return "(no path target)"
	}
	return joinComma(parts)
}

func joinComma(s []string) string {
	if len(s) == 0 {
		return ""
	}
	out := s[0]
	for _, p := range s[1:] {
		out += ", " + p
	}
	return out
}

// writeAuditRow appends one row to the local hash-chained audit log and
// returns the canonical "<ts>#<seq>" entry id for the envelope's
// meta.audit_log_entry field. Failures are non-fatal — we surface a
// stderr warning but don't kill the operation.
func writeAuditRow(meta generated.OpMeta, entry policy.Entry, args api.Args, result, approvalID string, dur time.Duration, profile string) string {
	path, err := audit.DefaultPath()
	if err != nil {
		return ""
	}
	l, err := audit.New(path)
	if err != nil {
		return ""
	}
	og, _ := auth.ResolveOG(globalFlags.og)
	argsHash := shortHashForArgs(args)
	e, err := l.Append(audit.Entry{
		Caller:            "ws1-cli",
		Operation:         meta.Op,
		ArgsHash:          argsHash,
		Class:             string(entry.Class),
		ApprovalRequestID: approvalID,
		Profile:           profile,
		Tenant:            og,
		Result:            result,
		DurationMs:        dur.Milliseconds(),
	})
	if err != nil {
		fmt.Fprintf(stderrWriter, "ws1: audit append failed: %v\n", err)
		return ""
	}
	return e.EntryID()
}

// shortHashForArgs is a stable per-invocation fingerprint used in the
// audit row's args_hash field. SHA-256 over a sorted key/value
// concatenation; not cryptographic privacy.
func shortHashForArgs(args api.Args) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%v\n", k, args[k])
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:32]
}
