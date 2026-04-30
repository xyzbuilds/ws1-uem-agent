package envelope

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// reJSON normalises a JSON byte slice by unmarshaling into any and re-marshaling.
// We compare envelope output against golden JSON via reJSON-of-both rather than
// raw string compare, because Go's json package does not guarantee key order.
func reJSON(t *testing.T, b []byte) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("reJSON: unmarshal %q: %v", string(b), err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("reJSON: marshal: %v", err)
	}
	return out
}

func equalJSON(t *testing.T, got, want []byte) {
	t.Helper()
	if !bytes.Equal(reJSON(t, got), reJSON(t, want)) {
		t.Errorf("JSON mismatch\n got: %s\nwant: %s", string(got), string(want))
	}
}

// TestReadSuccessRoundTrip reproduces spec section 5.2 verbatim.
func TestReadSuccessRoundTrip(t *testing.T) {
	want := []byte(`{
	  "envelope_version": 1,
	  "ok": true,
	  "operation": "mdm.devices.search",
	  "data": [
	    {"DeviceID": 12345, "SerialNumber": "ABC123", "FriendlyName": "Alice's iPhone 15", "EnrollmentStatus": "Enrolled"},
	    {"DeviceID": 12346, "SerialNumber": "DEF456", "FriendlyName": "Alice's MacBook Pro", "EnrollmentStatus": "Enrolled"}
	  ],
	  "meta": {
	    "duration_ms": 312,
	    "count": 2,
	    "page": 1,
	    "page_size": 100,
	    "has_more": false
	  }
	}`)

	env := New("mdm.devices.search").
		WithData([]map[string]any{
			{"DeviceID": 12345, "SerialNumber": "ABC123", "FriendlyName": "Alice's iPhone 15", "EnrollmentStatus": "Enrolled"},
			{"DeviceID": 12346, "SerialNumber": "DEF456", "FriendlyName": "Alice's MacBook Pro", "EnrollmentStatus": "Enrolled"},
		}).
		WithDuration(312*time.Millisecond).
		WithPagination(2, 1, 100, false)

	got, err := env.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	equalJSON(t, got, want)

	// Round-trip: parsing the spec's example must yield an envelope with
	// matching observable fields.
	var parsed Envelope
	if err := json.Unmarshal(want, &parsed); err != nil {
		t.Fatalf("Unmarshal spec example: %v", err)
	}
	if parsed.EnvelopeVersion != 1 || !parsed.OK || parsed.Operation != "mdm.devices.search" {
		t.Errorf("parsed top-level wrong: %+v", parsed)
	}
	if parsed.Meta.Count == nil || *parsed.Meta.Count != 2 {
		t.Errorf("parsed count wrong: %+v", parsed.Meta.Count)
	}
	if parsed.Meta.HasMore == nil || *parsed.Meta.HasMore {
		t.Errorf("parsed has_more wrong: %+v", parsed.Meta.HasMore)
	}
	if parsed.ExitCode() != ExitOK {
		t.Errorf("read success ExitCode = %d, want %d", parsed.ExitCode(), ExitOK)
	}
}

// TestWriteSuccessRoundTrip reproduces spec section 5.3.
func TestWriteSuccessRoundTrip(t *testing.T) {
	want := []byte(`{
	  "envelope_version": 1,
	  "ok": true,
	  "operation": "mdm.devices.lock",
	  "data": {
	    "DeviceID": 12345,
	    "command_uuid": "cmd_a1b2c3",
	    "status": "Queued"
	  },
	  "meta": {
	    "duration_ms": 412,
	    "approval_request_id": "req_a1b2c3",
	    "audit_log_entry": "2026-04-30T14:00:00Z#117"
	  }
	}`)

	env := New("mdm.devices.lock").
		WithData(map[string]any{
			"DeviceID":     12345,
			"command_uuid": "cmd_a1b2c3",
			"status":       "Queued",
		}).
		WithDuration(412 * time.Millisecond).
		WithApproval("req_a1b2c3").
		WithAudit("2026-04-30T14:00:00Z#117")

	got, _ := env.JSON()
	equalJSON(t, got, want)

	var parsed Envelope
	if err := json.Unmarshal(want, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if parsed.Meta.ApprovalRequestID != "req_a1b2c3" {
		t.Errorf("approval_request_id mismatch: %q", parsed.Meta.ApprovalRequestID)
	}
	if parsed.ExitCode() != ExitOK {
		t.Errorf("write success ExitCode = %d, want %d", parsed.ExitCode(), ExitOK)
	}
}

// TestPartialSuccessRoundTrip reproduces spec section 5.4.
func TestPartialSuccessRoundTrip(t *testing.T) {
	want := []byte(`{
	  "envelope_version": 1,
	  "ok": true,
	  "operation": "mdm.devices.commands.bulk",
	  "data": {
	    "successes": [
	      {"DeviceID": 12345, "command_uuid": "cmd_a1"},
	      {"DeviceID": 12346, "command_uuid": "cmd_a2"}
	    ],
	    "failures": [
	      {"DeviceID": 12347, "error": {"code": "STALE_RESOURCE", "message": "Device unenrolled since lookup."}}
	    ]
	  },
	  "meta": {
	    "duration_ms": 2104,
	    "target_count": 3,
	    "success_count": 2,
	    "failure_count": 1,
	    "approval_request_id": "req_b2c3d4"
	  }
	}`)

	env := New("mdm.devices.commands.bulk").
		WithData(PartialResult{
			Successes: []any{
				map[string]any{"DeviceID": 12345, "command_uuid": "cmd_a1"},
				map[string]any{"DeviceID": 12346, "command_uuid": "cmd_a2"},
			},
			Failures: []PartialFailure{{
				Target: map[string]any{"DeviceID": 12347},
				Error:  &Error{Code: CodeStaleResource, Message: "Device unenrolled since lookup."},
			}},
		}).
		WithDuration(2104*time.Millisecond).
		WithBulkCounts(3, 2, 1).
		WithApproval("req_b2c3d4")

	got, _ := env.JSON()
	equalJSON(t, got, want)

	if !env.IsPartial() {
		t.Error("IsPartial() should be true with failure_count = 1")
	}
	if env.ExitCode() != ExitPartial {
		t.Errorf("partial ExitCode = %d, want %d", env.ExitCode(), ExitPartial)
	}

	// Round-trip the failure shape: the merged-key JSON must unmarshal back
	// into a PartialFailure with both Target and Error populated. Data is
	// `any`, so we can't directly target it — re-parse via a typed wrap.
	type partialDataShape struct {
		Successes []map[string]any `json:"successes"`
		Failures  []PartialFailure `json:"failures"`
	}
	type wrap struct {
		EnvelopeVersion int              `json:"envelope_version"`
		OK              bool             `json:"ok"`
		Operation       string           `json:"operation"`
		Data            partialDataShape `json:"data"`
		Meta            Meta             `json:"meta"`
	}
	var w wrap
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("Unmarshal partial: %v", err)
	}
	if len(w.Data.Failures) != 1 {
		t.Fatalf("failures len = %d, want 1", len(w.Data.Failures))
	}
	f := w.Data.Failures[0]
	if f.Error == nil || f.Error.Code != CodeStaleResource {
		t.Errorf("failure error mismatch: %+v", f.Error)
	}
	if v, ok := f.Target["DeviceID"]; !ok || v.(float64) != 12347 {
		t.Errorf("failure target mismatch: %+v", f.Target)
	}
}

// TestAsyncRoundTrip reproduces spec section 5.5.
func TestAsyncRoundTrip(t *testing.T) {
	want := []byte(`{
	  "envelope_version": 1,
	  "ok": true,
	  "operation": "mcm.profiles.publish",
	  "data": {
	    "job_id": "job_x1y2z3",
	    "status": "Pending",
	    "poll_url": "ws1://jobs/job_x1y2z3"
	  },
	  "meta": {
	    "duration_ms": 218,
	    "async": true,
	    "approval_request_id": "req_c3d4e5"
	  }
	}`)

	env := New("mcm.profiles.publish").
		WithData(map[string]any{
			"job_id":   "job_x1y2z3",
			"status":   "Pending",
			"poll_url": "ws1://jobs/job_x1y2z3",
		}).
		WithDuration(218 * time.Millisecond).
		WithAsync().
		WithApproval("req_c3d4e5")

	got, _ := env.JSON()
	equalJSON(t, got, want)
	if env.ExitCode() != ExitOK {
		t.Errorf("async ExitCode = %d, want %d", env.ExitCode(), ExitOK)
	}
}

// TestErrorRoundTrip reproduces spec section 5.6.
func TestErrorRoundTrip(t *testing.T) {
	want := []byte(`{
	  "envelope_version": 1,
	  "ok": false,
	  "operation": "mdm.devices.wipe",
	  "error": {
	    "code": "AUTH_INSUFFICIENT_FOR_OP",
	    "message": "Current profile 'ro' cannot perform destructive ops. Switch to 'operator' or 'admin'.",
	    "details": {
	      "active_profile": "ro",
	      "required_profile_minimum": "operator",
	      "operation_class": "destructive"
	    }
	  },
	  "meta": {
	    "duration_ms": 12,
	    "cli_version": "0.1.0"
	  }
	}`)

	env := NewError("mdm.devices.wipe", CodeAuthInsufficientForOp,
		"Current profile 'ro' cannot perform destructive ops. Switch to 'operator' or 'admin'.").
		WithErrorDetails(map[string]any{
			"active_profile":           "ro",
			"required_profile_minimum": "operator",
			"operation_class":          "destructive",
		}).
		WithDuration(12*time.Millisecond).
		WithVersion("", "0.1.0")

	got, _ := env.JSON()
	equalJSON(t, got, want)
	if env.ExitCode() != ExitConfigAuth {
		t.Errorf("auth-error ExitCode = %d, want %d", env.ExitCode(), ExitConfigAuth)
	}
}

// TestExitCodeMapping checks every error code's category at the public API.
// If a future code is added without a category, ExitInternalError is the
// safe-by-default fallback.
func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		name string
		code string
		want int
	}{
		{"rate-limited", CodeRateLimited, ExitRecoverable},
		{"approval-timeout", CodeApprovalTimeout, ExitRecoverable},
		{"approval-denied", CodeApprovalDenied, ExitRecoverable},
		{"stale-resource", CodeStaleResource, ExitRecoverable},
		{"network-error", CodeNetworkError, ExitRecoverable},
		{"async-pending", CodeAsyncJobPending, ExitRecoverable},

		{"auth-insufficient", CodeAuthInsufficientForOp, ExitConfigAuth},
		{"tenant-required", CodeTenantRequired, ExitConfigAuth},
		{"approval-required", CodeApprovalRequired, ExitConfigAuth},
		{"spec-version-mismatch", CodeSpecVersionMismatch, ExitConfigAuth},

		{"identifier-ambiguous", CodeIdentifierAmbiguous, ExitValidation},
		{"identifier-not-found", CodeIdentifierNotFound, ExitValidation},
		{"unknown-operation", CodeUnknownOperation, ExitValidation},

		{"internal-error", CodeInternalError, ExitInternalError},
		{"unmapped-future-code", "FUTURE_UNKNOWN_CODE", ExitInternalError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := NewError("test.op", tc.code, "msg")
			if got := env.ExitCode(); got != tc.want {
				t.Errorf("ExitCode(%s) = %d, want %d", tc.code, got, tc.want)
			}
		})
	}
}

// TestExitCodePartialBranch covers the partial-success exit code path
// (ok: true with failure_count > 0).
func TestExitCodePartialBranch(t *testing.T) {
	zero := New("test.op").WithBulkCounts(3, 3, 0)
	if zero.IsPartial() {
		t.Error("IsPartial false-positive when failure_count == 0")
	}
	if zero.ExitCode() != ExitOK {
		t.Errorf("zero-failure ExitCode = %d, want %d", zero.ExitCode(), ExitOK)
	}

	mixed := New("test.op").WithBulkCounts(3, 2, 1)
	if !mixed.IsPartial() {
		t.Error("IsPartial false-negative when failure_count > 0")
	}
	if mixed.ExitCode() != ExitPartial {
		t.Errorf("mixed ExitCode = %d, want %d", mixed.ExitCode(), ExitPartial)
	}
}

// TestErrorWithoutErrorObject covers the defensive path where ok: false
// reaches ExitCode without an Error set. This shouldn't happen in practice
// (the constructor always sets it) but the helper must not panic.
func TestErrorWithoutErrorObject(t *testing.T) {
	env := &Envelope{EnvelopeVersion: 1, OK: false, Operation: "test.op"}
	if got := env.ExitCode(); got != ExitInternalError {
		t.Errorf("ExitCode no-error = %d, want %d", got, ExitInternalError)
	}
}

// TestDataOmittedOnError ensures error envelopes don't accidentally carry a
// `"data": null` — the spec's section 5.6 example has no data field at all.
func TestDataOmittedOnError(t *testing.T) {
	env := NewError("test.op", CodeInternalError, "boom").
		WithDuration(1 * time.Millisecond)
	b, _ := env.JSON()
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, has := generic["data"]; has {
		t.Errorf("error envelope leaked a data key: %s", string(b))
	}
}

// TestErrorOmittedOnSuccess ensures success envelopes don't carry an error.
func TestErrorOmittedOnSuccess(t *testing.T) {
	env := New("test.op").WithDuration(1 * time.Millisecond)
	b, _ := env.JSON()
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, has := generic["error"]; has {
		t.Errorf("success envelope leaked an error key: %s", string(b))
	}
}

// TestPartialFailureRoundTrip exercises the merged-key marshal/unmarshal
// pair on PartialFailure independently of the full envelope.
func TestPartialFailureRoundTrip(t *testing.T) {
	original := PartialFailure{
		Target: map[string]any{"DeviceID": float64(99)},
		Error:  &Error{Code: CodeStaleResource, Message: "drift"},
	}
	b, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got PartialFailure
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got.Target, original.Target) {
		t.Errorf("Target mismatch: got %+v want %+v", got.Target, original.Target)
	}
	if got.Error == nil || got.Error.Code != original.Error.Code {
		t.Errorf("Error mismatch: %+v", got.Error)
	}
}

// TestErrorImplementsErrorInterface lets envelope.Error flow through
// idiomatic Go error-handling.
func TestErrorImplementsErrorInterface(t *testing.T) {
	var err error = &Error{Code: CodeInternalError, Message: "bad"}
	if err.Error() != "INTERNAL_ERROR: bad" {
		t.Errorf("Error() = %q", err.Error())
	}
}
