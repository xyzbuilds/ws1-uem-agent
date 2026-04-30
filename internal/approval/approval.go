// Package approval implements the ephemeral browser approval server that
// gates destructive operations per spec section 7.
//
// Per CLAUDE.md locked decision #7: destructive ops always require browser
// approval. The approval surface is an ephemeral HTTP server bound to
// 127.0.0.1:<random-port> for the lifetime of a single CLI invocation. The
// agent makes one blocking call. The agent never handles approval tokens.
//
// Per CLAUDE.md locked decision #8: at execute time, callers MUST re-fetch
// the target and compare against the snapshot taken at approval time. Drift
// triggers STALE_RESOURCE; approval is NOT consumed. That freshness check
// is the caller's responsibility — this package provides FreshnessCheck as
// a helper but doesn't enforce it.
package approval

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"time"
)

// DefaultTimeout is the maximum time we'll wait for the user to interact
// with the browser before failing with APPROVAL_TIMEOUT (spec section 7.1).
const DefaultTimeout = 5 * time.Minute

// Outcome is the user's verdict on an approval request.
type Outcome int

const (
	// OutcomeApproved — user clicked Approve in the browser.
	OutcomeApproved Outcome = iota
	// OutcomeDenied — user clicked Deny.
	OutcomeDenied
	// OutcomeTimeout — DefaultTimeout elapsed before any interaction.
	OutcomeTimeout
	// OutcomeAborted — caller canceled the context.
	OutcomeAborted
)

func (o Outcome) String() string {
	switch o {
	case OutcomeApproved:
		return "approved"
	case OutcomeDenied:
		return "denied"
	case OutcomeTimeout:
		return "timeout"
	case OutcomeAborted:
		return "aborted"
	default:
		return "unknown"
	}
}

// Target describes one of the entities the approval covers. The Snapshot
// captures the relevant state (owner, OG, enrollment status, ...) at the
// time of approval; FreshnessCheck compares it to a re-fetch at execute time.
type Target struct {
	ID           string
	DisplayLabel string         // e.g. "Alice's iPhone 15 (Serial ABC123)"
	Snapshot     map[string]any // owner / OG / enrollment_status / ...
}

// Request is everything the user needs to make an informed decision plus
// everything the freshness check needs at execute time.
type Request struct {
	Operation     string // canonical op identifier, e.g. mdmv4.devices.lock
	OperationDesc string // human description ("Lock device")
	Class         string // "destructive" | "write" | "read"
	Reversibility string // "full" | "partial" | "none" | "unknown"
	Profile       string // active profile at submission
	Tenant        string // OG context
	Targets       []Target

	// Args holds the raw argv (post-binding) so we can hash it for
	// audit-log reproducibility. Not displayed; only used to fingerprint.
	Args map[string]any

	// Timeout overrides DefaultTimeout for tests.
	Timeout time.Duration
}

// Result is what the server returns after the user decides (or timeout).
type Result struct {
	RequestID  string
	Outcome    Outcome
	Approved   bool
	ApprovedAt time.Time
	ArgsHash   string // hex sha256 of normalised args
	Targets    []Target
}

// argsHash deterministically hashes a request's argv for audit + freshness.
// Not cryptographic privacy — just a stable fingerprint so two equivalent
// invocations produce the same hash. Map keys are sorted before hashing.
func argsHash(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte{0x1f}) // unit separator
		_, _ = fmt.Fprintf(h, "%v", args[k])
		_, _ = h.Write([]byte{0x1e}) // record separator
	}
	return hex.EncodeToString(h.Sum(nil))
}

// newRequestID returns "req_" + 16 random bytes hex (spec section 7.1 step 2).
func newRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is exceptional; fall back to time-derived.
		now := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(now >> (i % 8 * 8))
		}
	}
	return "req_" + hex.EncodeToString(b)
}

// FreshnessCheck compares the snapshot captured at approval time to a
// freshly-fetched current state. Returns nil if the relevant fields match;
// returns a *DriftError otherwise with which fields drifted. Callers
// translate the error to envelope.CodeStaleResource and DO NOT consume the
// approval (the user can re-approve after re-evaluation).
//
// "Relevant fields" are the keys present in `snapshot`. Extra keys in
// `current` are ignored — the API may return more fields than we recorded.
func FreshnessCheck(snapshot, current map[string]any) error {
	var drifted []DriftField
	for k, snapVal := range snapshot {
		curVal, ok := current[k]
		if !ok {
			drifted = append(drifted, DriftField{Field: k, Was: snapVal, Now: nil})
			continue
		}
		if !reflect.DeepEqual(snapVal, curVal) {
			drifted = append(drifted, DriftField{Field: k, Was: snapVal, Now: curVal})
		}
	}
	if len(drifted) > 0 {
		return &DriftError{Fields: drifted}
	}
	return nil
}

// DriftError describes one or more snapshot drifts.
type DriftError struct {
	Fields []DriftField
}

// DriftField records a single key whose value changed between approval and
// execute time.
type DriftField struct {
	Field string
	Was   any
	Now   any
}

// Error formats a one-line summary suitable for CLI stderr.
func (e *DriftError) Error() string {
	parts := []string{}
	for _, f := range e.Fields {
		parts = append(parts, fmt.Sprintf("%s: %v -> %v", f.Field, f.Was, f.Now))
	}
	return "STALE_RESOURCE: " + joinComma(parts)
}

// AsDetails builds an envelope-ready details map for a STALE_RESOURCE error.
func (e *DriftError) AsDetails() map[string]any {
	out := make([]map[string]any, 0, len(e.Fields))
	for _, f := range e.Fields {
		out = append(out, map[string]any{"field": f.Field, "was": f.Was, "now": f.Now})
	}
	return map[string]any{"drift": out}
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
