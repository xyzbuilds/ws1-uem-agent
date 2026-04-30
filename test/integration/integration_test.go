// Package integration runs the ws1 binary end-to-end against the mock
// tenant. It's the safety net for the demo: any envelope shape regression
// should fail here before it can leak into production.
package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
	"github.com/xyzbuilds/ws1-uem-agent/test/mockws1"
)

type suite struct {
	t       *testing.T
	bin     string
	mock    *mockws1.Server
	mockURL string
	cfgDir  string
}

func setup(t *testing.T) *suite {
	t.Helper()
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "ws1")
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/ws1")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build ws1: %v", err)
	}

	srv := mockws1.New()
	httpSrv := srv.Start()
	t.Cleanup(httpSrv.Close)

	cfgDir := filepath.Join(tmp, ".ws1cfg")
	_ = os.MkdirAll(cfgDir, 0o700)

	return &suite{t: t, bin: bin, mock: srv, mockURL: httpSrv.URL, cfgDir: cfgDir}
}

func (s *suite) cmd(args ...string) *exec.Cmd {
	// Force the operator profile for state-changing commands. In mock
	// mode the profile file isn't loaded; --profile is enough to satisfy
	// the capability gate.
	full := append([]string{"--profile", "operator"}, args...)
	cmd := exec.Command(s.bin, full...)
	cmd.Env = append(os.Environ(),
		"WS1_BASE_URL="+s.mockURL,
		"WS1_MOCK_TOKEN=mock-bearer",
		"WS1_CONFIG_DIR="+s.cfgDir,
		"WS1_NO_BROWSER=1",
		"HOME="+s.cfgDir,
	)
	return cmd
}

func (s *suite) run(args ...string) (envelope.Envelope, int, string) {
	s.t.Helper()
	cmd := s.cmd(args...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	code := 0
	exitErr := &exec.ExitError{}
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	}
	stdout := strings.TrimSpace(out.String())
	if stdout == "" {
		s.t.Fatalf("ws1 produced no stdout for args %v\nstderr: %s", args, errOut.String())
	}
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		s.t.Fatalf("parse envelope: %v\nstdout: %s\nstderr: %s", err, stdout, errOut.String())
	}
	return env, code, errOut.String()
}

func TestUserSearchByEmail(t *testing.T) {
	s := setup(t)
	env, code, _ := s.run("systemv2", "users", "search", "--email", "alice@example.com")
	if code != envelope.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if env.Operation != "systemv2.users.search" {
		t.Errorf("op = %q", env.Operation)
	}
	rows, _ := env.Data.([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 alice, got %d", len(rows))
	}
}

func TestDeviceSearchByUser(t *testing.T) {
	s := setup(t)
	env, code, _ := s.run("mdmv4", "devices", "search", "--user", "alice@example.com")
	if code != envelope.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	rows, _ := env.Data.([]any)
	if len(rows) != 2 {
		t.Errorf("alice should have 2 devices, got %d", len(rows))
	}
	if env.Meta.Count == nil || *env.Meta.Count != 2 {
		t.Errorf("meta.count = %+v", env.Meta.Count)
	}
}

func TestDeviceGet404(t *testing.T) {
	s := setup(t)
	env, code, _ := s.run("mdmv4", "devices", "get", "--id", "99999")
	if env.OK {
		t.Errorf("ok = true on 404")
	}
	if code != envelope.ExitValidation {
		t.Errorf("exit = %d, want %d", code, envelope.ExitValidation)
	}
	if env.Error == nil || env.Error.Code != envelope.CodeIdentifierNotFound {
		t.Errorf("error code = %v", env.Error)
	}
}

func TestLockSingleNoApproval(t *testing.T) {
	s := setup(t)
	env, code, stderr := s.run("mdmv4", "devices", "lock", "--id", "12345")
	if code != envelope.ExitOK {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if env.Operation != "mdmv4.devices.lock" {
		t.Errorf("op = %q", env.Operation)
	}
	if env.Meta.AuditLogEntry == "" {
		t.Errorf("meta.audit_log_entry empty")
	}
}

func TestLockBulkBelowThresholdSkipsApproval(t *testing.T) {
	s := setup(t)
	env, code, _ := s.run("mdmv4", "devices", "lock", "--ids", "12345,12346")
	if code != envelope.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if env.Meta.SuccessCount == nil || *env.Meta.SuccessCount != 2 {
		t.Errorf("success_count = %+v", env.Meta.SuccessCount)
	}
}

func TestDryRunSkipsExecute(t *testing.T) {
	s := setup(t)
	env, code, _ := s.run("--dry-run", "mdmv4", "devices", "lock", "--id", "12345")
	if code != envelope.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	data, _ := env.Data.(map[string]any)
	if data["dry_run"] != true {
		t.Errorf("dry_run flag missing: %+v", data)
	}
	if got := s.mock.Issued[12345]; len(got) != 0 {
		t.Errorf("dry-run leaked a real command: %v", got)
	}
}

func TestAuditChainIntactAfterLock(t *testing.T) {
	s := setup(t)
	s.run("mdmv4", "devices", "lock", "--id", "12345")
	s.run("mdmv4", "devices", "lock", "--id", "12346")
	env, code, _ := s.run("audit", "verify")
	if code != envelope.ExitOK {
		t.Fatalf("audit verify exit = %d, env = %+v", code, env)
	}
	data, _ := env.Data.(map[string]any)
	if data["ok"] != true {
		t.Errorf("audit not ok: %+v", data)
	}
	if total, _ := data["total"].(float64); int(total) < 2 {
		t.Errorf("audit total = %v, want >= 2", data["total"])
	}
}

func TestWipeRequiresApproval(t *testing.T) {
	s := setup(t)
	cmd := s.cmd("mdmv4", "devices", "wipe", "--id", "12345")
	stderrPipe, _ := cmd.StderrPipe()
	stdoutPipe, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	approvalURL := waitForApprovalURL(t, stderrPipe)
	resp, err := http.Post(approvalURL+"/approve", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST approve: %v", err)
	}
	resp.Body.Close()

	stdoutBytes, _ := io.ReadAll(stdoutPipe)
	_ = cmd.Wait()
	stdoutBytes = bytes.TrimSpace(stdoutBytes)

	var env envelope.Envelope
	if err := json.Unmarshal(stdoutBytes, &env); err != nil {
		t.Fatalf("parse: %v\noutput: %s", err, stdoutBytes)
	}
	if !env.OK {
		t.Errorf("wipe ok=false: %+v", env.Error)
	}
	if env.Operation != "mdmv4.devices.wipe" {
		t.Errorf("op = %q", env.Operation)
	}
	if env.Meta.ApprovalRequestID == "" {
		t.Errorf("approval_request_id missing in meta")
	}
}

func TestWipeDeniedExits2(t *testing.T) {
	s := setup(t)
	cmd := s.cmd("mdmv4", "devices", "wipe", "--id", "12345")
	stderrPipe, _ := cmd.StderrPipe()
	stdoutPipe, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	approvalURL := waitForApprovalURL(t, stderrPipe)
	resp, _ := http.Post(approvalURL+"/deny", "application/x-www-form-urlencoded", nil)
	resp.Body.Close()

	stdoutBytes, _ := io.ReadAll(stdoutPipe)
	err := cmd.Wait()
	exit := 0
	exitErr := &exec.ExitError{}
	if errors.As(err, &exitErr) {
		exit = exitErr.ExitCode()
	}
	if exit != envelope.ExitRecoverable {
		t.Errorf("denied exit = %d, want %d", exit, envelope.ExitRecoverable)
	}
	var env envelope.Envelope
	json.Unmarshal(bytes.TrimSpace(stdoutBytes), &env)
	if env.Error == nil || env.Error.Code != envelope.CodeApprovalDenied {
		t.Errorf("error = %+v", env.Error)
	}
	// Mock must NOT have received a wipe.
	if len(s.mock.Issued[12345]) != 0 {
		t.Errorf("denied approval still issued a command: %v", s.mock.Issued[12345])
	}
}

// waitForApprovalURL reads stderr until it finds the http://127.0.0.1:...
// line printed by approval.Run.
func waitForApprovalURL(t *testing.T, r io.Reader) string {
	t.Helper()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 256)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if i := strings.Index(string(buf), "http://127.0.0.1:"); i >= 0 {
				rest := string(buf[i:])
				end := strings.IndexAny(rest, " \r\n")
				if end > 0 {
					return rest[:end]
				}
			}
		}
		if err != nil {
			t.Fatalf("did not see approval URL on stderr: %s", string(buf))
		}
	}
}
