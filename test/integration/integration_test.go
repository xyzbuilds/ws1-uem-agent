// Package integration runs the ws1 binary end-to-end against the mock
// tenant. Drives real-spec ops via the generic command tree to confirm
// the envelope contract, approval flow, and audit chain stay correct.
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
	if err != nil {
		e := &exec.ExitError{}
		if errors.As(err, &e) {
			code = e.ExitCode()
		} else {
			s.t.Fatalf("exec: %v\nstderr: %s", err, errOut.String())
		}
	}
	stdout := strings.TrimSpace(out.String())
	if stdout == "" {
		s.t.Fatalf("ws1 produced no stdout for args %v\nstderr: %s", args, errOut.String())
	}
	var env envelope.Envelope
	if uerr := json.Unmarshal([]byte(stdout), &env); uerr != nil {
		s.t.Fatalf("parse envelope: %v\nstdout: %s\nstderr: %s", uerr, stdout, errOut.String())
	}
	return env, code, errOut.String()
}

func TestReadUserSearchByEmail(t *testing.T) {
	s := setup(t)
	env, code, _ := s.run("systemv1", "user", "search", "--email", "alice@example.com")
	if code != envelope.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if env.Operation != "systemv1.user.search" {
		t.Errorf("op = %q", env.Operation)
	}
	data, _ := env.Data.(map[string]any)
	users, _ := data["Users"].([]any)
	if len(users) != 1 {
		t.Fatalf("expected 1 alice, got %d (data=%+v)", len(users), data)
	}
}

func TestReadDevicesSearchByUser(t *testing.T) {
	s := setup(t)
	env, code, _ := s.run("mdmv1", "devices", "search", "--user", "alice@example.com")
	if code != envelope.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	data, _ := env.Data.(map[string]any)
	devices, _ := data["Devices"].([]any)
	if len(devices) != 2 {
		t.Errorf("alice should have 2 devices, got %d", len(devices))
	}
	if env.Meta.Count == nil || *env.Meta.Count != 2 {
		t.Errorf("meta.count = %+v, want 2", env.Meta.Count)
	}
}

func TestReadDeviceByUuid(t *testing.T) {
	s := setup(t)
	env, code, _ := s.run("mdmv2", "devicesv2", "getbyuuid",
		"--uuid", "ip15-uuid-0000-0000-000000000001")
	if code != envelope.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	data, _ := env.Data.(map[string]any)
	if data["FriendlyName"] != "Alice's iPhone 15" {
		t.Errorf("unexpected FriendlyName: %v", data["FriendlyName"])
	}
}

func TestReadDeviceByUuid_NotFound(t *testing.T) {
	s := setup(t)
	env, code, _ := s.run("mdmv2", "devicesv2", "getbyuuid",
		"--uuid", "no-such-uuid")
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

func TestWriteSingleCommandLock(t *testing.T) {
	s := setup(t)
	env, code, stderr := s.run("mdmv2", "commandsv2", "execute",
		"--deviceUuid", "ip15-uuid-0000-0000-000000000001",
		"--commandName", "Lock")
	if code != envelope.ExitOK {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if env.Operation != "mdmv2.commandsv2.execute" {
		t.Errorf("op = %q", env.Operation)
	}
	if env.Meta.AuditLogEntry == "" {
		t.Errorf("meta.audit_log_entry empty")
	}
	issued := s.mock.Issued["ip15-uuid-0000-0000-000000000001"]
	if len(issued) != 1 {
		t.Errorf("issued = %v", issued)
	}
}

// findApprovalURL streams stderr until it finds the http://127.0.0.1:.../r/req_<id>
// line printed by approval.Run when the server starts.
func findApprovalURL(t *testing.T, r io.Reader) string {
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
			t.Fatalf("did not see approval URL on stderr; got: %s", string(buf))
		}
	}
}

func TestDestructiveDeviceWipeWithApproval(t *testing.T) {
	s := setup(t)
	cmd := s.cmd("mdmv2", "commandsv2", "execute",
		"--deviceUuid", "ip15-uuid-0000-0000-000000000001",
		"--commandName", "DeviceWipe")
	stderrPipe, _ := cmd.StderrPipe()
	stdoutPipe, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	approvalURL := findApprovalURL(t, stderrPipe)
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
	if env.Meta.ApprovalRequestID == "" {
		t.Errorf("approval_request_id missing in meta")
	}
	issued := s.mock.Issued["ip15-uuid-0000-0000-000000000001"]
	if len(issued) != 1 {
		t.Errorf("destructive wipe didn't reach the mock: %v", issued)
	}
}

func TestDestructiveDeniedExits2(t *testing.T) {
	s := setup(t)
	cmd := s.cmd("mdmv2", "commandsv2", "execute",
		"--deviceUuid", "ip15-uuid-0000-0000-000000000001",
		"--commandName", "DeviceWipe")
	stderrPipe, _ := cmd.StderrPipe()
	stdoutPipe, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	approvalURL := findApprovalURL(t, stderrPipe)
	resp, _ := http.Post(approvalURL+"/deny", "application/x-www-form-urlencoded", nil)
	resp.Body.Close()

	stdoutBytes, _ := io.ReadAll(stdoutPipe)
	err := cmd.Wait()
	exit := 0
	e := &exec.ExitError{}
	if errors.As(err, &e) {
		exit = e.ExitCode()
	}
	if exit != envelope.ExitRecoverable {
		t.Errorf("denied exit = %d, want %d", exit, envelope.ExitRecoverable)
	}
	var env envelope.Envelope
	_ = json.Unmarshal(bytes.TrimSpace(stdoutBytes), &env)
	if env.Error == nil || env.Error.Code != envelope.CodeApprovalDenied {
		t.Errorf("error = %+v", env.Error)
	}
	if len(s.mock.Issued["ip15-uuid-0000-0000-000000000001"]) != 0 {
		t.Errorf("denied approval still issued a command: %v", s.mock.Issued)
	}
}

func TestAuditChainIntactAfterMultipleOps(t *testing.T) {
	s := setup(t)
	s.run("mdmv2", "commandsv2", "execute",
		"--deviceUuid", "ip15-uuid-0000-0000-000000000001",
		"--commandName", "Lock")
	s.run("mdmv2", "commandsv2", "execute",
		"--deviceUuid", "mbp-uuid-0000-0000-000000000001",
		"--commandName", "Lock")
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

// TestUuidPipeline: search alice by email, then use her returned Uuid
// directly with the v2 read endpoint. Exercises the user's "prefer
// UUID" principle end-to-end.
func TestUuidPipeline(t *testing.T) {
	s := setup(t)
	env, code, _ := s.run("systemv1", "user", "search", "--email", "alice@example.com")
	if code != envelope.ExitOK {
		t.Fatalf("user search: exit %d", code)
	}
	data, _ := env.Data.(map[string]any)
	users, _ := data["Users"].([]any)
	first, _ := users[0].(map[string]any)
	uuid, _ := first["Uuid"].(string)
	if uuid == "" {
		t.Fatalf("no Uuid in user record: %+v", first)
	}
	env2, code2, _ := s.run("systemv2", "usersv2", "read", "--uuid", uuid)
	if code2 != envelope.ExitOK {
		t.Fatalf("user read: exit %d, env=%+v", code2, env2)
	}
	d2, _ := env2.Data.(map[string]any)
	if d2["emailAddress"] != "alice@example.com" {
		t.Errorf("v2 read returned wrong user: %+v", d2)
	}
}
