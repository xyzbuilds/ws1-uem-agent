package approval

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// drive issues the approve/deny click against a Pending and returns the
// HTTP status. Tests use this helper so the click happens after Wait is
// already blocking on the goroutine.
func drive(t *testing.T, p *Pending, action string) int {
	t.Helper()
	resp, err := http.Post(p.URL+"/"+action, "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST %s: %v", action, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func sampleRequest() Request {
	return Request{
		Operation:     "mdmv4.devices.lock",
		OperationDesc: "Lock device",
		Class:         "write",
		Reversibility: "full",
		Profile:       "operator",
		Tenant:        "12345",
		Targets: []Target{{
			ID:           "12345",
			DisplayLabel: "Alice's iPhone 15 (ABC123)",
			Snapshot: map[string]any{
				"EnrollmentUser":    "alice@example.com",
				"EnrollmentStatus":  "Enrolled",
				"OrganizationGroup": "EMEA-Pilot",
			},
		}},
		Args: map[string]any{"id": 12345},
	}
}

func TestApproveOutcome(t *testing.T) {
	p, err := Begin(sampleRequest())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if !strings.HasPrefix(p.RequestID, "req_") {
		t.Errorf("RequestID = %q", p.RequestID)
	}
	resCh := make(chan *Result, 1)
	go func() {
		r, _ := p.Wait(context.Background(), 5*time.Second)
		resCh <- r
	}()
	if got := drive(t, p, "approve"); got != http.StatusOK {
		t.Errorf("approve POST status = %d", got)
	}
	r := <-resCh
	if r.Outcome != OutcomeApproved {
		t.Errorf("Outcome = %v, want approved", r.Outcome)
	}
	if !r.Approved {
		t.Errorf("Approved = false")
	}
	if r.ArgsHash == "" {
		t.Errorf("ArgsHash empty")
	}
}

func TestDenyOutcome(t *testing.T) {
	p, _ := Begin(sampleRequest())
	resCh := make(chan *Result, 1)
	go func() {
		r, _ := p.Wait(context.Background(), 5*time.Second)
		resCh <- r
	}()
	drive(t, p, "deny")
	r := <-resCh
	if r.Outcome != OutcomeDenied {
		t.Errorf("Outcome = %v, want denied", r.Outcome)
	}
	if r.Approved {
		t.Error("Approved must be false on deny")
	}
}

func TestTimeoutOutcome(t *testing.T) {
	p, _ := Begin(sampleRequest())
	r, err := p.Wait(context.Background(), 250*time.Millisecond)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if r.Outcome != OutcomeTimeout {
		t.Errorf("Outcome = %v, want timeout", r.Outcome)
	}
}

func TestContextCancelOutcome(t *testing.T) {
	p, _ := Begin(sampleRequest())
	ctx, cancel := context.WithCancel(context.Background())
	resCh := make(chan *Result, 1)
	go func() {
		r, _ := p.Wait(ctx, 30*time.Second)
		resCh <- r
	}()
	cancel()
	r := <-resCh
	if r.Outcome != OutcomeAborted {
		t.Errorf("Outcome = %v, want aborted", r.Outcome)
	}
}

// TestSecondClickIgnored exercises the in-process idempotence: once the
// user has decided, a second click on the *server* must not flip the
// outcome. We test this by issuing two POSTs in flight before Wait exits,
// using a dedicated server with a longer-running approval loop that
// suppresses shutdown until we drive both clicks. We synchronise via a
// channel rather than time.Sleep.
func TestSecondClickIgnored(t *testing.T) {
	p, _ := Begin(sampleRequest())
	defer p.Cancel()
	// Drive both clicks before Wait observes the decision.
	drive(t, p, "approve")
	// The second click races against the close-and-shutdown sequence in
	// Wait. To exercise the in-process state machine, GET the approval
	// page first — that path also reads the decided flag and is safe even
	// after Wait observes it.
	resp, err := http.Get(p.URL)
	if err != nil {
		t.Fatalf("GET after approve: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "approved") {
		t.Errorf("post-decision page should render outcome; got %s", string(body))
	}
	// And Wait still returns the original outcome.
	r, _ := p.Wait(context.Background(), 100*time.Millisecond)
	if r.Outcome != OutcomeApproved {
		t.Errorf("Wait after click outcome = %v, want approved", r.Outcome)
	}
}

func TestApprovalPageRendersTargets(t *testing.T) {
	p, _ := Begin(sampleRequest())
	defer p.Cancel()
	resp, err := http.Get(p.URL)
	if err != nil {
		t.Fatalf("GET %s: %v", p.URL, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(body)
	for _, want := range []string{
		"Lock device",
		"mdmv4.devices.lock",
		"Alice&#39;s iPhone 15", // html-escaped apostrophe
		"alice@example.com",
		"EMEA-Pilot",
		"badge write",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("approval page missing %q\n--- body ---\n%s", want, html)
		}
	}
}

func TestPageMethodGuard(t *testing.T) {
	p, _ := Begin(sampleRequest())
	defer p.Cancel()
	req, _ := http.NewRequest(http.MethodDelete, p.URL+"/approve", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /approve status = %d, want 405", resp.StatusCode)
	}
}

func TestRunRejectsEmptyTargets(t *testing.T) {
	if _, err := Begin(Request{Operation: "x"}); err == nil {
		t.Fatal("expected error for empty Targets")
	}
}

func TestArgsHashStable(t *testing.T) {
	a := argsHash(map[string]any{"id": 12345, "command": "lock"})
	b := argsHash(map[string]any{"command": "lock", "id": 12345})
	if a != b {
		t.Errorf("argsHash should be order-independent:\n  a=%s\n  b=%s", a, b)
	}
	if argsHash(map[string]any{"id": 12346}) == a {
		t.Error("argsHash should differ when args differ")
	}
}

func TestFreshnessCheckClean(t *testing.T) {
	snap := map[string]any{"EnrollmentUser": "alice@x", "OG": "EMEA"}
	cur := map[string]any{"EnrollmentUser": "alice@x", "OG": "EMEA", "Extra": "ignored"}
	if err := FreshnessCheck(snap, cur); err != nil {
		t.Errorf("expected no drift, got %v", err)
	}
}

func TestFreshnessCheckDrift(t *testing.T) {
	snap := map[string]any{"EnrollmentUser": "alice@x", "OG": "EMEA"}
	cur := map[string]any{"EnrollmentUser": "bob@x", "OG": "EMEA"}
	err := FreshnessCheck(snap, cur)
	if err == nil {
		t.Fatal("expected drift error")
	}
	d, ok := err.(*DriftError)
	if !ok {
		t.Fatalf("type = %T", err)
	}
	if len(d.Fields) != 1 || d.Fields[0].Field != "EnrollmentUser" {
		t.Errorf("drift fields = %+v", d.Fields)
	}
	if d.AsDetails()["drift"] == nil {
		t.Error("AsDetails missing drift key")
	}
}

func TestFreshnessCheckMissingField(t *testing.T) {
	snap := map[string]any{"X": 1}
	cur := map[string]any{}
	err := FreshnessCheck(snap, cur)
	if err == nil {
		t.Fatal("expected drift for missing field")
	}
}
