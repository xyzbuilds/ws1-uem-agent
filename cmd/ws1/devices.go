package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zhangxuyang/ws1-uem-agent/internal/api"
	"github.com/zhangxuyang/ws1-uem-agent/internal/auth"
	"github.com/zhangxuyang/ws1-uem-agent/internal/envelope"
	"github.com/zhangxuyang/ws1-uem-agent/internal/generated"
)

// newDevicesCmd builds the `ws1 mdmv4 devices ...` subtree. The CLI surface
// follows the operation identifier shape: <section> <tag> <verb>, mirroring
// docs/spec-acquisition.md's "CLI command path" table.
//
// Read-class verbs (search/get) land here; lock/wipe/bulkcommand are
// destructive/write and live in lock.go because they share the approval
// flow.
func newDevicesCmd() *cobra.Command {
	mdmv4 := &cobra.Command{
		Use:   "mdmv4",
		Short: "MDM API V4 commands",
	}
	devices := &cobra.Command{
		Use:   "devices",
		Short: "Devices in this tenant",
	}
	devices.AddCommand(
		newDevicesSearchCmd(),
		newDevicesGetCmd(),
		newDevicesLockCmd(),
		newDevicesWipeCmd(),
		newDevicesBulkCommandCmd(),
	)
	mdmv4.AddCommand(devices)
	return mdmv4
}

func newDevicesSearchCmd() *cobra.Command {
	var user, platform string
	var page, pageSize int
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search devices",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			cli, og, err := buildAPIClient()
			if err != nil {
				emitAndExit(err.WithDuration(time.Since(start)))
				return
			}
			a := api.Args{
				"page":     page,
				"pagesize": pageSize,
			}
			if user != "" {
				a["user"] = user
			}
			if platform != "" {
				a["platform"] = platform
			}
			if og != "" {
				a["organizationgroupid"] = og
			}
			resp, err2 := cli.Do(context.Background(), "mdmv4.devices.search", a)
			if err2 != nil {
				emitAndExit(envelope.NewError("mdmv4.devices.search",
					envelope.CodeNetworkError, err2.Error()).WithDuration(time.Since(start)))
				return
			}
			if resp.StatusCode >= 400 {
				emitAndExit(httpErrorEnvelope("mdmv4.devices.search", resp).WithDuration(time.Since(start)))
				return
			}
			var parsed struct {
				Devices  []map[string]any `json:"Devices"`
				Page     int              `json:"Page"`
				PageSize int              `json:"PageSize"`
				Total    int              `json:"Total"`
			}
			if err := resp.JSON(&parsed); err != nil {
				emitAndExit(envelope.NewError("mdmv4.devices.search",
					envelope.CodeInternalError, "parse: "+err.Error()).
					WithDuration(time.Since(start)))
				return
			}
			hasMore := parsed.Page*parsed.PageSize+len(parsed.Devices) < parsed.Total
			emitAndExit(envelope.New("mdmv4.devices.search").
				WithData(parsed.Devices).
				WithPagination(parsed.Total, parsed.Page, parsed.PageSize, hasMore).
				WithDuration(time.Since(start)))
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "filter by enrollment user (username or email)")
	cmd.Flags().StringVar(&platform, "platform", "", "filter by platform (Apple, Android, ...)")
	cmd.Flags().IntVar(&page, "page", 0, "page number (0-indexed)")
	cmd.Flags().IntVar(&pageSize, "pagesize", 100, "page size")
	return cmd
}

func newDevicesGetCmd() *cobra.Command {
	var id int
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get a device by ID",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			cli, _, err := buildAPIClient()
			if err != nil {
				emitAndExit(err.WithDuration(time.Since(start)))
				return
			}
			if id == 0 {
				emitAndExit(envelope.NewError("mdmv4.devices.get",
					envelope.CodeIdentifierAmbiguous, "--id is required").
					WithDuration(time.Since(start)))
				return
			}
			resp, err2 := cli.Do(context.Background(), "mdmv4.devices.get", api.Args{"id": id})
			if err2 != nil {
				emitAndExit(envelope.NewError("mdmv4.devices.get",
					envelope.CodeNetworkError, err2.Error()).WithDuration(time.Since(start)))
				return
			}
			if resp.StatusCode == 404 {
				emitAndExit(envelope.NewError("mdmv4.devices.get",
					envelope.CodeIdentifierNotFound,
					fmt.Sprintf("no device with id %d", id)).
					WithDuration(time.Since(start)))
				return
			}
			if resp.StatusCode >= 400 {
				emitAndExit(httpErrorEnvelope("mdmv4.devices.get", resp).WithDuration(time.Since(start)))
				return
			}
			var device map[string]any
			if err := resp.JSON(&device); err != nil {
				emitAndExit(envelope.NewError("mdmv4.devices.get",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			emitAndExit(envelope.New("mdmv4.devices.get").
				WithData(device).
				WithDuration(time.Since(start)))
		},
	}
	cmd.Flags().IntVar(&id, "id", 0, "device ID")
	return cmd
}

// buildAPIClient resolves profile + token source + OG and returns the
// client and effective OG. Failures are returned as a partial envelope
// (caller adds duration).
func buildAPIClient() (*api.Client, string, *envelope.Envelope) {
	activeName, err := auth.Active()
	if err != nil {
		return nil, "", envelope.NewError("ws1.api",
			envelope.CodeInternalError, err.Error())
	}
	if globalFlags.profile != "" {
		activeName = globalFlags.profile
	}

	og, _ := auth.ResolveOG(globalFlags.og)

	// In mock-mode we bypass the profiles file entirely.
	if v := getenv("WS1_MOCK_TOKEN"); v != "" {
		return api.New(&auth.MockTokenSource{
			BaseURLValue: getenv("WS1_BASE_URL"),
			TokenValue:   v,
		}), og, nil
	}

	prof, err := auth.FindProfile(activeName)
	if err != nil {
		return nil, og, envelope.NewError("ws1.api",
			envelope.CodeAuthInsufficientForOp, err.Error()).
			WithErrorDetails(map[string]any{"active_profile": activeName})
	}
	return api.New(auth.NewOAuthClient(prof)), og, nil
}

// httpErrorEnvelope maps a non-2xx Response to the right envelope error.
// Used by every read command so the mapping is consistent.
func httpErrorEnvelope(op string, resp *api.Response) *envelope.Envelope {
	body := strings.TrimSpace(string(resp.Body))
	if len(body) > 256 {
		body = body[:256] + "..."
	}
	switch {
	case resp.StatusCode == 401:
		return envelope.NewError(op, envelope.CodeAuthInsufficientForOp,
			"API returned 401 Unauthorized; check your profile credentials").
			WithErrorDetails(map[string]any{"status": resp.StatusCode, "body": body})
	case resp.StatusCode == 429:
		return envelope.NewError(op, envelope.CodeRateLimited,
			"API rate limit hit").
			WithErrorDetails(map[string]any{
				"status":              resp.StatusCode,
				"retry_after_seconds": parseRetryAfter(resp),
			})
	case resp.StatusCode == 404:
		return envelope.NewError(op, envelope.CodeIdentifierNotFound,
			"API returned 404 Not Found").
			WithErrorDetails(map[string]any{"status": resp.StatusCode, "body": body})
	case resp.StatusCode >= 500:
		return envelope.NewError(op, envelope.CodeInternalError,
			fmt.Sprintf("API returned %d", resp.StatusCode)).
			WithErrorDetails(map[string]any{"body": body})
	default:
		return envelope.NewError(op, envelope.CodeInternalError,
			fmt.Sprintf("API returned %d", resp.StatusCode)).
			WithErrorDetails(map[string]any{"body": body})
	}
}

func parseRetryAfter(resp *api.Response) int {
	if v := resp.Headers.Get("Retry-After"); v != "" {
		var n int
		_ = json.Unmarshal([]byte(v), &n)
		return n
	}
	return 0
}

// _ ensures we import generated even if no compile-time symbol is used here
// after future refactors. Helps catch missing-Ops bugs at link time.
var _ = generated.Ops
