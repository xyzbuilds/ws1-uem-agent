package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
)

// buildOnce builds the ws1 binary into a temp dir for this test process.
// We exec the real binary rather than calling cmd functions in-process
// because the production exit path goes through os.Exit and is awkward to
// stub.
func buildOnce(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "ws1")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Stdout = io.Discard
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build: %v\nstderr: %s", err, stderr.String())
	}
	return bin
}

func runCmd(t *testing.T, bin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := cmd.Output()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("exec %s %v: %v", bin, args, err)
	}
	return string(out), code
}

// TestVersionEnvelope: the --version flag must emit a parseable envelope
// with the well-known operation "ws1.version" and exit 0.
func TestVersionEnvelope(t *testing.T) {
	bin := buildOnce(t)
	out, code := runCmd(t, bin, "--version", "--json")
	if code != envelope.ExitOK {
		t.Fatalf("--version exit = %d, want %d", code, envelope.ExitOK)
	}
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("Unmarshal: %v\noutput: %s", err, out)
	}
	if env.Operation != "ws1.version" {
		t.Errorf("operation = %q, want ws1.version", env.Operation)
	}
	if !env.OK {
		t.Errorf("ok = false, want true")
	}
}

// TestDoctorEnvelope: doctor must emit a checks array, summary counts,
// and exit 0 in the v0 stub state (everything passes or skips).
func TestDoctorEnvelope(t *testing.T) {
	bin := buildOnce(t)
	out, code := runCmd(t, bin, "doctor")
	if code != envelope.ExitOK {
		t.Fatalf("doctor exit = %d, want %d", code, envelope.ExitOK)
	}
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("Unmarshal: %v\noutput: %s", err, out)
	}
	if env.Operation != "ws1.doctor" {
		t.Errorf("operation = %q", env.Operation)
	}
	data, _ := env.Data.(map[string]any)
	if data == nil {
		t.Fatalf("doctor data missing or wrong type: %#v", env.Data)
	}
	checks, _ := data["checks"].([]any)
	if len(checks) == 0 {
		t.Errorf("doctor checks empty")
	}
}
