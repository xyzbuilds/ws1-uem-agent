package envelope

// Error is the structured error surface attached to a non-ok envelope.
// Every error.code value comes from the finite taxonomy in spec section 6;
// adding a new code requires design discussion (CLAUDE.md locked decision #4).
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Error implements the standard error interface so envelope errors can flow
// through normal Go error-handling paths.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

// Finite error taxonomy per spec section 6. The constant identifier mirrors
// the wire format in PascalCase; the literal value is the wire string.
const (
	// CodeAuthInsufficientForOp - active profile cannot perform the op's
	// class. Agent should surface to user and ask whether to switch profile.
	CodeAuthInsufficientForOp = "AUTH_INSUFFICIENT_FOR_OP"

	// CodeApprovalRequired - destructive or scale-gated op needs approval
	// and the CLI couldn't prompt (typically headless). Should not normally
	// surface; CLI handles approval inline.
	CodeApprovalRequired = "APPROVAL_REQUIRED"

	// CodeApprovalTimeout - user did not approve within the 5-min window.
	// Agent should surface to user and ask whether to retry.
	CodeApprovalTimeout = "APPROVAL_TIMEOUT"

	// CodeApprovalDenied - user clicked Deny in browser. Agent should stop;
	// not retry.
	CodeApprovalDenied = "APPROVAL_DENIED"

	// CodeIdentifierAmbiguous - lookup returned multiple candidates. Agent
	// should surface candidates and ask user to pick.
	CodeIdentifierAmbiguous = "IDENTIFIER_AMBIGUOUS"

	// CodeIdentifierNotFound - lookup returned zero matches. Agent should
	// surface to user; consider whether the search itself is wrong.
	CodeIdentifierNotFound = "IDENTIFIER_NOT_FOUND"

	// CodeTenantRequired - no OG context set; every command requires --og
	// or a default via `ws1 og use`.
	CodeTenantRequired = "TENANT_REQUIRED"

	// CodeRateLimited - API rate limit hit. Agent should backoff per
	// details.retry_after_seconds and retry.
	CodeRateLimited = "RATE_LIMITED"

	// CodeAsyncJobPending - job started but not yet complete (returned only
	// when re-checking a job's status).
	CodeAsyncJobPending = "ASYNC_JOB_PENDING"

	// CodeStaleResource - the approved target's state changed between
	// approval and execute. Approval is NOT consumed; agent should re-fetch
	// and re-confirm with the user.
	CodeStaleResource = "STALE_RESOURCE"

	// CodeUnknownOperation - op exists in spec.json but is not classified
	// in operations.policy.yaml. Treated as destructive (fail-closed).
	CodeUnknownOperation = "UNKNOWN_OPERATION"

	// CodeSpecVersionMismatch - the CLI's compiled spec is older than the
	// tenant's API Explorer version. Agent should recommend `ws1 update`.
	CodeSpecVersionMismatch = "SPEC_VERSION_MISMATCH"

	// CodeNetworkError - could not reach the API. Agent should surface and
	// ask user to check connectivity.
	CodeNetworkError = "NETWORK_ERROR"

	// CodeInternalError - bug in the CLI. Agent should surface and
	// recommend `ws1 send-feedback`.
	CodeInternalError = "INTERNAL_ERROR"
)
