package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/xyzbuilds/ws1-uem-agent/internal/api"
	"github.com/xyzbuilds/ws1-uem-agent/internal/approval"
	"github.com/xyzbuilds/ws1-uem-agent/internal/audit"
	"github.com/xyzbuilds/ws1-uem-agent/internal/auth"
	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
	"github.com/xyzbuilds/ws1-uem-agent/internal/policy"
)

// snapshotFields are the device-record fields we capture at approval time
// and re-check at execute time (spec section 7.2 freshness check).
var snapshotFields = []string{
	"DeviceID",
	"SerialNumber",
	"EnrollmentUser",
	"EnrollmentStatus",
	"OrganizationGroupID",
	"OrganizationGroupName",
}

// captureSnapshot extracts only the snapshot fields, ignoring everything
// else the API returned. Keeps the snapshot tight and the freshness check
// precise.
func captureSnapshot(device map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range snapshotFields {
		if v, ok := device[k]; ok {
			out[k] = v
		}
	}
	return out
}

func newDevicesLockCmd() *cobra.Command {
	var idsFlag string
	cmd := &cobra.Command{
		Use:   "lock",
		Short: "Queue a device lock command (single or bulk)",
		Long: `Lock one or more devices. Single target with --id; multiple targets with
--ids "12345,12346,12347". Reversible: the device unlocks on next user
authentication. Per spec, this op is write-class so single targets do not
require approval; bulk operations exceeding the policy's blast_radius_threshold
do go through the browser approval flow.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			runDeviceCommand(cmd, idsFlag, deviceCmdLock)
		},
	}
	cmd.Flags().StringVar(&idsFlag, "ids", "",
		"comma-separated device IDs (or --id <single>)")
	cmd.Flags().Int("id", 0, "single device ID (alternative to --ids)")
	return cmd
}

func newDevicesWipeCmd() *cobra.Command {
	var idsFlag string
	cmd := &cobra.Command{
		Use:   "wipe",
		Short: "Queue an enterprise wipe (DESTRUCTIVE; always requires approval)",
		Long: `Wipe enterprise data on a device and unenroll it. IRREVERSIBLE.
Every invocation goes through the browser approval flow regardless of count.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			runDeviceCommand(cmd, idsFlag, deviceCmdWipe)
		},
	}
	cmd.Flags().StringVar(&idsFlag, "ids", "",
		"comma-separated device IDs (or --id <single>)")
	cmd.Flags().Int("id", 0, "single device ID")
	return cmd
}

func newDevicesBulkCommandCmd() *cobra.Command {
	var command, idsFlag string
	cmd := &cobra.Command{
		Use:   "bulkcommand",
		Short: "Queue a generic command across many devices",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			runDeviceCommand(cmd, idsFlag, command)
		},
	}
	cmd.Flags().StringVar(&command, "command", "Lock", "command to send (Lock, Wipe, ClearPasscode, ...)")
	cmd.Flags().StringVar(&idsFlag, "ids", "", "comma-separated device IDs (required)")
	return cmd
}

const (
	deviceCmdLock = "Lock"
	deviceCmdWipe = "EnterpriseWipe"
)

// runDeviceCommand is the shared core for lock/wipe/bulkcommand.
// Sequence (spec section 7.1):
//  1. Resolve targets, fetch each device for the snapshot.
//  2. Classify the op via policy; decide whether approval is required.
//  3. If required, run the browser approval flow.
//  4. Re-fetch each target; freshness-check vs the snapshot.
//  5. Execute the API call. For one target -> /devices/{id}/commands/<verb>;
//     for many -> /devices/commands/bulk.
//  6. Append the audit-log entry.
//  7. Emit the envelope (write-success or partial-success).
func runDeviceCommand(cmd *cobra.Command, idsFlag, command string) {
	start := time.Now()
	op := opForCommand(command)

	cli, og, errEnv := buildAPIClient()
	if errEnv != nil {
		emitAndExit(errEnv.WithDuration(time.Since(start)))
		return
	}

	id, _ := cmd.Flags().GetInt("id")
	ids, errEnv := resolveIDs(id, idsFlag)
	if errEnv != nil {
		emitAndExit(errEnv.WithDuration(time.Since(start)))
		return
	}

	// 1. Snapshot every target.
	snapshots, errEnv := fetchSnapshots(cli, ids)
	if errEnv != nil {
		emitAndExit(errEnv.WithDuration(time.Since(start)))
		return
	}

	// 2. Classify and decide approval.
	pol := loadActivePolicy()
	entry := pol.Classify(op)
	needApproval := entry.RequiresApproval(len(ids))

	activeName, _ := auth.Active()
	if globalFlags.profile != "" {
		activeName = globalFlags.profile
	}
	if !canPerformClass(activeName, entry) {
		emitAndExit(envelope.NewError(op,
			envelope.CodeAuthInsufficientForOp,
			fmt.Sprintf("profile %q cannot perform %s ops", activeName, entry.Class)).
			WithErrorDetails(map[string]any{
				"active_profile":  activeName,
				"operation_class": string(entry.Class),
			}).
			WithDuration(time.Since(start)))
		return
	}

	// 3. Approval if needed (skip in --dry-run).
	var approvedRequestID string
	if needApproval && !globalFlags.dryRun {
		req := approval.Request{
			Operation:     op,
			OperationDesc: humanDescForOp(op),
			Class:         string(entry.Class),
			Reversibility: string(entry.Reversible),
			Profile:       activeName,
			Tenant:        og,
			Targets:       targetsFromSnapshots(snapshots),
			Args:          map[string]any{"command": command, "ids": ids},
		}
		res, err := approval.Run(context.Background(), req)
		if err != nil {
			emitAndExit(envelope.NewError(op,
				envelope.CodeInternalError, err.Error()).
				WithDuration(time.Since(start)))
			return
		}
		switch res.Outcome {
		case approval.OutcomeApproved:
			approvedRequestID = res.RequestID
		case approval.OutcomeDenied:
			emitAndExit(envelope.NewError(op, envelope.CodeApprovalDenied,
				"User denied approval in browser").
				WithErrorDetails(map[string]any{"request_id": res.RequestID}).
				WithDuration(time.Since(start)))
			return
		case approval.OutcomeTimeout:
			emitAndExit(envelope.NewError(op, envelope.CodeApprovalTimeout,
				"Approval window elapsed without a decision").
				WithErrorDetails(map[string]any{"request_id": res.RequestID}).
				WithDuration(time.Since(start)))
			return
		default:
			emitAndExit(envelope.NewError(op, envelope.CodeInternalError,
				"unexpected approval outcome: "+res.Outcome.String()).
				WithDuration(time.Since(start)))
			return
		}
	}

	// 4. Re-fetch + freshness check.
	if !globalFlags.dryRun && needApproval {
		current, errEnv := fetchSnapshots(cli, ids)
		if errEnv != nil {
			emitAndExit(errEnv.WithDuration(time.Since(start)))
			return
		}
		for did, before := range snapshots {
			now, ok := current[did]
			if !ok {
				emitAndExit(envelope.NewError(op,
					envelope.CodeStaleResource,
					fmt.Sprintf("device %d disappeared between approval and execute", did)).
					WithErrorDetails(map[string]any{"device_id": did}).
					WithDuration(time.Since(start)))
				return
			}
			if err := approval.FreshnessCheck(before, now); err != nil {
				details := map[string]any{}
				var d *approval.DriftError
				if errors.As(err, &d) {
					details = d.AsDetails()
				}
				emitAndExit(envelope.NewError(op,
					envelope.CodeStaleResource, err.Error()).
					WithErrorDetails(details).
					WithDuration(time.Since(start)))
				return
			}
		}
	}

	// 5. Execute.
	if globalFlags.dryRun {
		emitAndExit(envelope.New(op).
			WithData(map[string]any{
				"dry_run":      true,
				"would_target": ids,
				"command":      command,
			}).
			WithDuration(time.Since(start)))
		return
	}

	if len(ids) == 1 {
		executeSingle(cli, op, ids[0], command, approvedRequestID, start)
		return
	}
	executeBulk(cli, op, ids, command, approvedRequestID, start)
}

func opForCommand(command string) string {
	switch command {
	case deviceCmdLock:
		return "mdmv4.devices.lock"
	case deviceCmdWipe:
		return "mdmv4.devices.wipe"
	default:
		return "mdmv4.devices.bulkcommand"
	}
}

func humanDescForOp(op string) string {
	switch op {
	case "mdmv4.devices.lock":
		return "Lock device"
	case "mdmv4.devices.wipe":
		return "Wipe device (enterprise)"
	default:
		return "Send device command"
	}
}

func canPerformClass(profileName string, entry policy.Entry) bool {
	prof := &auth.Profile{Name: profileName}
	return prof.Can(string(entry.Class))
}

func resolveIDs(single int, idsFlag string) ([]int, *envelope.Envelope) {
	if idsFlag != "" && single != 0 {
		return nil, envelope.NewError("mdmv4.devices",
			envelope.CodeIdentifierAmbiguous,
			"specify either --id or --ids, not both")
	}
	if idsFlag != "" {
		var out []int
		for _, p := range strings.Split(idsFlag, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil, envelope.NewError("mdmv4.devices",
					envelope.CodeIdentifierAmbiguous,
					"non-integer device id: "+p)
			}
			out = append(out, n)
		}
		if len(out) == 0 {
			return nil, envelope.NewError("mdmv4.devices",
				envelope.CodeIdentifierAmbiguous, "--ids must contain at least one id")
		}
		return out, nil
	}
	if single != 0 {
		return []int{single}, nil
	}
	return nil, envelope.NewError("mdmv4.devices",
		envelope.CodeIdentifierAmbiguous, "specify --id or --ids")
}

func fetchSnapshots(cli *api.Client, ids []int) (map[int]map[string]any, *envelope.Envelope) {
	out := map[int]map[string]any{}
	for _, id := range ids {
		resp, err := cli.Do(context.Background(), "mdmv4.devices.get",
			api.Args{"id": id})
		if err != nil {
			return nil, envelope.NewError("mdmv4.devices.get",
				envelope.CodeNetworkError, err.Error())
		}
		if resp.StatusCode == 404 {
			return nil, envelope.NewError("mdmv4.devices.get",
				envelope.CodeIdentifierNotFound,
				fmt.Sprintf("no device with id %d", id))
		}
		if resp.StatusCode >= 400 {
			return nil, httpErrorEnvelope("mdmv4.devices.get", resp)
		}
		var dev map[string]any
		if err := resp.JSON(&dev); err != nil {
			return nil, envelope.NewError("mdmv4.devices.get",
				envelope.CodeInternalError, err.Error())
		}
		out[id] = captureSnapshot(dev)
	}
	return out, nil
}

func targetsFromSnapshots(snaps map[int]map[string]any) []approval.Target {
	out := make([]approval.Target, 0, len(snaps))
	for id, snap := range snaps {
		label := fmt.Sprintf("Device %d", id)
		if name, ok := snap["FriendlyName"].(string); ok && name != "" {
			label = name
		} else if serial, ok := snap["SerialNumber"].(string); ok && serial != "" {
			label = fmt.Sprintf("Device %d (%s)", id, serial)
		}
		out = append(out, approval.Target{
			ID:           strconv.Itoa(id),
			DisplayLabel: label,
			Snapshot:     snap,
		})
	}
	return out
}

func executeSingle(cli *api.Client, op string, id int, command, approvalID string, start time.Time) {
	commandOp := op
	if op == "mdmv4.devices.bulkcommand" {
		// Single device dispatched via lock/wipe specific endpoint; pick
		// the right one by command verb.
		switch command {
		case deviceCmdLock:
			commandOp = "mdmv4.devices.lock"
		case deviceCmdWipe:
			commandOp = "mdmv4.devices.wipe"
		}
	}
	resp, err := cli.Do(context.Background(), commandOp, api.Args{"id": id})
	if err != nil {
		emitAndExit(envelope.NewError(op,
			envelope.CodeNetworkError, err.Error()).WithDuration(time.Since(start)))
		return
	}
	if resp.StatusCode >= 400 {
		emitAndExit(httpErrorEnvelope(op, resp).WithDuration(time.Since(start)))
		return
	}
	var data map[string]any
	if len(resp.Body) > 0 {
		_ = resp.JSON(&data)
	}
	if data == nil {
		data = map[string]any{"DeviceID": id, "status": "Queued"}
	}
	auditEntry := writeAuditEntry(op, command, []int{id}, "ok", approvalID, time.Since(start))
	env := envelope.New(op).
		WithData(data).
		WithApproval(approvalID).
		WithAudit(auditEntry).
		WithDuration(time.Since(start))
	emitAndExit(env)
}

func executeBulk(cli *api.Client, op string, ids []int, command, approvalID string, start time.Time) {
	resp, err := cli.Do(context.Background(), "mdmv4.devices.bulkcommand", api.Args{
		"command":    command,
		"device_ids": ids,
	})
	if err != nil {
		emitAndExit(envelope.NewError(op,
			envelope.CodeNetworkError, err.Error()).WithDuration(time.Since(start)))
		return
	}
	if resp.StatusCode >= 400 {
		emitAndExit(httpErrorEnvelope(op, resp).WithDuration(time.Since(start)))
		return
	}
	var parsed struct {
		Successes []map[string]any `json:"successes"`
		Failures  []map[string]any `json:"failures"`
	}
	if err := resp.JSON(&parsed); err != nil {
		emitAndExit(envelope.NewError(op,
			envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
		return
	}
	failures := make([]envelope.PartialFailure, 0, len(parsed.Failures))
	for _, f := range parsed.Failures {
		var ferr *envelope.Error
		if e, ok := f["error"].(map[string]any); ok {
			ferr = &envelope.Error{
				Code:    fmt.Sprint(e["code"]),
				Message: fmt.Sprint(e["message"]),
			}
		} else {
			ferr = &envelope.Error{Code: envelope.CodeInternalError, Message: "unspecified failure"}
		}
		target := map[string]any{}
		for k, v := range f {
			if k == "error" {
				continue
			}
			target[k] = v
		}
		failures = append(failures, envelope.PartialFailure{Target: target, Error: ferr})
	}
	successes := make([]any, 0, len(parsed.Successes))
	for _, s := range parsed.Successes {
		successes = append(successes, s)
	}
	result := envelope.PartialResult{Successes: successes, Failures: failures}

	totalCount := len(ids)
	failCount := len(failures)
	successCount := totalCount - failCount

	resStr := "ok"
	if failCount > 0 {
		resStr = "partial"
	}
	auditEntry := writeAuditEntry(op, command, ids, resStr, approvalID, time.Since(start))

	env := envelope.New(op).
		WithData(result).
		WithBulkCounts(totalCount, successCount, failCount).
		WithApproval(approvalID).
		WithAudit(auditEntry).
		WithDuration(time.Since(start))
	emitAndExit(env)
}

// writeAuditEntry appends one row to the local audit log and returns its
// canonical "<ts>#<seq>" identifier for the envelope's meta.audit_log_entry.
// Failures here are non-fatal: we surface a stderr warning but don't kill
// the operation.
func writeAuditEntry(op, command string, ids []int, result, approvalID string, dur time.Duration) string {
	path, err := audit.DefaultPath()
	if err != nil {
		return ""
	}
	l, err := audit.New(path)
	if err != nil {
		return ""
	}
	active, _ := auth.Active()
	if globalFlags.profile != "" {
		active = globalFlags.profile
	}
	og, _ := auth.ResolveOG(globalFlags.og)
	idsStr := make([]string, len(ids))
	for i, id := range ids {
		idsStr[i] = strconv.Itoa(id)
	}
	pol := loadActivePolicy()
	entry := pol.Classify(op)
	e, err := l.Append(audit.Entry{
		Caller:            "ws1-cli",
		Operation:         op,
		ArgsHash:          shortHash(command + ":" + strings.Join(idsStr, ",")),
		Class:             string(entry.Class),
		ApprovalRequestID: approvalID,
		Profile:           active,
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
