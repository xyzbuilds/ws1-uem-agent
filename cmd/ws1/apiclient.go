package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xyzbuilds/ws1-uem-agent/internal/api"
	"github.com/xyzbuilds/ws1-uem-agent/internal/auth"
	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
)

// buildAPIClient resolves profile + token source + OG and returns the
// client and effective OG. Failures are returned as a partial envelope
// (caller adds duration).
//
// Mock-mode shortcut: if WS1_MOCK_TOKEN is set, skip the profiles file
// entirely and use the mock token source. Used by tests + the demo.
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

// httpErrorEnvelope maps a non-2xx Response to the canonical envelope
// error code. Used by every CLI command so the mapping is consistent.
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

// parseRetryAfter reads the Retry-After header as an integer (seconds).
// Empty / unparseable returns 0.
func parseRetryAfter(resp *api.Response) int {
	if v := resp.Headers.Get("Retry-After"); v != "" {
		var n int
		_ = json.Unmarshal([]byte(v), &n)
		return n
	}
	return 0
}
