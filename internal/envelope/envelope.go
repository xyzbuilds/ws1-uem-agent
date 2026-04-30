// Package envelope is the canonical JSON envelope contract for the ws1 CLI.
//
// Every command's stdout is a single JSON object matching the schema documented
// in ws1-uem-agent-v0-spec.md section 5. The envelope is versioned via the
// EnvelopeVersion field; bump it on any breaking shape change. Agents consuming
// the CLI's stdout check this on every parse.
package envelope

import (
	"encoding/json"
	"time"
)

// EnvelopeVersion is the current envelope schema version. Agents should refuse
// to process envelopes with a higher version than they understand.
const EnvelopeVersion = 1

// Envelope is the top-level shape returned on stdout for every CLI invocation.
type Envelope struct {
	EnvelopeVersion int    `json:"envelope_version"`
	OK              bool   `json:"ok"`
	Operation       string `json:"operation"`
	// Data is omitted on error envelopes.
	Data any `json:"data,omitempty"`
	// Error is omitted on success envelopes.
	Error *Error `json:"error,omitempty"`
	Meta  Meta   `json:"meta"`
}

// Meta carries operation-shaped metadata. Fields are conditionally present
// depending on the envelope flavor (read / write / partial / async / error);
// see spec section 5.
type Meta struct {
	DurationMs  int64  `json:"duration_ms"`
	SpecVersion string `json:"spec_version,omitempty"`
	CLIVersion  string `json:"cli_version,omitempty"`

	// Read-flavor pagination.
	Count    *int  `json:"count,omitempty"`
	Page     *int  `json:"page,omitempty"`
	PageSize *int  `json:"page_size,omitempty"`
	HasMore  *bool `json:"has_more,omitempty"`

	// Write- and destructive-flavor approval tracking.
	ApprovalRequestID string `json:"approval_request_id,omitempty"`
	AuditLogEntry     string `json:"audit_log_entry,omitempty"`

	// Bulk-flavor outcome counts.
	TargetCount  *int `json:"target_count,omitempty"`
	SuccessCount *int `json:"success_count,omitempty"`
	FailureCount *int `json:"failure_count,omitempty"`

	// Async-flavor flag.
	Async bool `json:"async,omitempty"`
}

// PartialResult is the canonical shape of `data` for bulk write envelopes.
// Per-target outcomes are split into successes and failures so an agent can
// branch on meta.failure_count without rescanning data.
type PartialResult struct {
	Successes []any           `json:"successes"`
	Failures  []PartialFailure `json:"failures"`
}

// PartialFailure is a single failing target inside a bulk write envelope.
type PartialFailure struct {
	// Target identifies the failing item; shape is operation-specific
	// (typically an ID like {"DeviceID": 12347}).
	Target map[string]any `json:"-"`
	Error  *Error         `json:"error"`
}

// MarshalJSON flattens Target's keys alongside the error so the result reads
// naturally, matching spec section 5.4: {"DeviceID": 12347, "error": {...}}.
func (f PartialFailure) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(f.Target)+1)
	for k, v := range f.Target {
		out[k] = v
	}
	out["error"] = f.Error
	return json.Marshal(out)
}

// UnmarshalJSON is the inverse of MarshalJSON; it pulls "error" out of the
// flattened object and stuffs the remaining keys into Target.
func (f *PartialFailure) UnmarshalJSON(b []byte) error {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if errRaw, ok := raw["error"]; ok {
		var e Error
		if err := json.Unmarshal(errRaw, &e); err != nil {
			return err
		}
		f.Error = &e
		delete(raw, "error")
	}
	target := make(map[string]any, len(raw))
	for k, v := range raw {
		var x any
		if err := json.Unmarshal(v, &x); err != nil {
			return err
		}
		target[k] = x
	}
	f.Target = target
	return nil
}

// New constructs a base success envelope for the named operation.
// Use the With* builders to layer on flavor-specific fields.
func New(operation string) *Envelope {
	return &Envelope{
		EnvelopeVersion: EnvelopeVersion,
		OK:              true,
		Operation:       operation,
	}
}

// NewError constructs a base error envelope for the named operation.
func NewError(operation string, code, message string) *Envelope {
	return &Envelope{
		EnvelopeVersion: EnvelopeVersion,
		OK:              false,
		Operation:       operation,
		Error:           &Error{Code: code, Message: message},
	}
}

// WithData attaches operation-specific payload to a success envelope.
func (e *Envelope) WithData(data any) *Envelope {
	e.Data = data
	return e
}

// WithDuration records how long the operation took. Sub-millisecond durations
// round to zero, which is intended; sub-millisecond CLI calls are noise.
func (e *Envelope) WithDuration(d time.Duration) *Envelope {
	e.Meta.DurationMs = d.Milliseconds()
	return e
}

// WithVersion stamps the spec and CLI versions into meta. Both are always-on
// for `ws1 --version` and required by spec section 5.1; other envelopes
// commonly omit cli_version (the agent already knows it from --version).
func (e *Envelope) WithVersion(specVersion, cliVersion string) *Envelope {
	e.Meta.SpecVersion = specVersion
	e.Meta.CLIVersion = cliVersion
	return e
}

// WithPagination populates the read-flavor meta fields. Pass them all together
// — partial pagination state would confuse a downstream agent's "more pages?"
// check.
func (e *Envelope) WithPagination(count, page, pageSize int, hasMore bool) *Envelope {
	e.Meta.Count = &count
	e.Meta.Page = &page
	e.Meta.PageSize = &pageSize
	e.Meta.HasMore = &hasMore
	return e
}

// WithBulkCounts populates the partial-success meta block. `failureCount > 0`
// pushes the process exit code from 0 to 1 even though ok stays true.
func (e *Envelope) WithBulkCounts(target, success, failure int) *Envelope {
	e.Meta.TargetCount = &target
	e.Meta.SuccessCount = &success
	e.Meta.FailureCount = &failure
	return e
}

// WithApproval records the request_id that gated this operation.
func (e *Envelope) WithApproval(requestID string) *Envelope {
	e.Meta.ApprovalRequestID = requestID
	return e
}

// WithAudit records the audit log entry that this operation produced.
// Format is "<rfc3339-ts>#<seq>" per spec section 9.
func (e *Envelope) WithAudit(entry string) *Envelope {
	e.Meta.AuditLogEntry = entry
	return e
}

// WithAsync flags this envelope as describing an async job (data should
// contain at least job_id and a status).
func (e *Envelope) WithAsync() *Envelope {
	e.Meta.Async = true
	return e
}

// WithErrorDetails attaches a details map to the envelope's Error. Has no
// effect on success envelopes.
func (e *Envelope) WithErrorDetails(details map[string]any) *Envelope {
	if e.Error == nil {
		return e
	}
	e.Error.Details = details
	return e
}

// IsPartial reports whether this is a bulk envelope with at least one failure.
func (e *Envelope) IsPartial() bool {
	return e.OK && e.Meta.FailureCount != nil && *e.Meta.FailureCount > 0
}

// JSON renders the envelope as a single line of JSON for stdout.
// We use a non-indented form because agents parse line-by-line; humans who
// want a pretty form can pipe through `jq`.
func (e *Envelope) JSON() ([]byte, error) {
	return json.Marshal(e)
}

// MustJSON panics if marshaling fails. Marshaling Envelope can only fail if
// Data contains a non-marshalable value (channels, funcs); CLI code should
// not put such values in Data, so a panic is appropriate.
func (e *Envelope) MustJSON() []byte {
	b, err := e.JSON()
	if err != nil {
		panic("envelope: marshal failed: " + err.Error())
	}
	return b
}
