package envelope

// Exit codes per spec section 5.7. main.go should call e.ExitCode() and pass
// the result to os.Exit so shell pipelines can branch on outcome class
// without parsing JSON.
const (
	ExitOK            = 0 // ok: true, no failures
	ExitPartial       = 1 // ok: true, meta.failure_count > 0
	ExitRecoverable   = 2 // ok: false, retry-class error
	ExitConfigAuth    = 3 // ok: false, config / auth class
	ExitValidation    = 4 // ok: false, validation class
	ExitInternalError = 5 // ok: false, bug or unmapped code
)

// errorExitCategory maps each finite error code to its exit-code class.
// Codes not present here fall through to ExitInternalError, which matches
// spec semantics for "bug in CLI / unmapped code".
var errorExitCategory = map[string]int{
	// Recoverable: agent or user retries.
	CodeRateLimited:     ExitRecoverable,
	CodeApprovalTimeout: ExitRecoverable,
	CodeApprovalDenied:  ExitRecoverable,
	CodeStaleResource:   ExitRecoverable,
	CodeNetworkError:    ExitRecoverable,
	CodeAsyncJobPending: ExitRecoverable,

	// Config / auth: requires user-side change before retry succeeds.
	CodeAuthInsufficientForOp: ExitConfigAuth,
	CodeTenantRequired:        ExitConfigAuth,
	CodeApprovalRequired:      ExitConfigAuth,
	CodeSpecVersionMismatch:   ExitConfigAuth,

	// Validation: caller's argv is wrong or ambiguous.
	CodeIdentifierAmbiguous: ExitValidation,
	CodeIdentifierNotFound:  ExitValidation,
	CodeUnknownOperation:    ExitValidation,

	// Internal: unrecoverable bug.
	CodeInternalError: ExitInternalError,
}

// ExitCode returns the process exit code that should accompany this envelope.
func (e *Envelope) ExitCode() int {
	if e.OK {
		if e.IsPartial() {
			return ExitPartial
		}
		return ExitOK
	}
	if e.Error == nil {
		return ExitInternalError
	}
	if cat, ok := errorExitCategory[e.Error.Code]; ok {
		return cat
	}
	return ExitInternalError
}
