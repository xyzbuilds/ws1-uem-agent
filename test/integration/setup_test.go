package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
	"github.com/xyzbuilds/ws1-uem-agent/test/mockws1"
)

func TestSetupIntegrationAgainstMock(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "ws1")
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/ws1")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}
	srv := mockws1.New().Start()
	defer srv.Close()

	cfg := filepath.Join(tmp, "ws1cfg")
	_ = os.MkdirAll(cfg, 0o700)

	// Drive the binary in non-interactive mode so we don't need a PTY.
	c := exec.Command(bin, "setup",
		"--profile", "operator",
		"--tenant", "demo.awmdm.com",
		"--auth-url", srv.URL+"/oauth",
		"--client-id", "cid",
		"--client-secret", "csec",
		"--og", "4067",
		"--skip-smoke-test",
	)
	c.Env = append(os.Environ(),
		"WS1_BASE_URL="+srv.URL,
		"WS1_CONFIG_DIR="+cfg,
		"WS1_ALLOW_DISK_SECRETS=1",
		"WS1_FORCE_NONINTERACTIVE=1",
		"HOME="+cfg,
	)
	var out, errOut bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errOut
	if err := c.Run(); err != nil {
		t.Fatalf("setup: %v\nstdout: %s\nstderr: %s", err, out.String(), errOut.String())
	}

	// Parse stdout envelope.
	stdout := strings.TrimSpace(out.String())
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout: %s", err, stdout)
	}
	if !env.OK {
		t.Errorf("ok=false: %+v", env.Error)
	}
	if env.Operation != "ws1.setup" {
		t.Errorf("op = %q", env.Operation)
	}

	// Disk side-effects.
	mustHaveProfileFile(t, cfg, "operator")
	if og := mustReadFile(t, filepath.Join(cfg, "og")); og != "4067\n" {
		t.Errorf("og = %q", og)
	}

	// Exit summary should appear in stderr.
	if !strings.Contains(errOut.String(), "Setup complete.") {
		t.Errorf("stderr missing 'Setup complete.'\nstderr: %s", errOut.String())
	}
}

// TestSetupIntegrationOAuthRoundTrip drives an interactive-style wizard
// via stdin pipe. Exercises the OAuth round-trip (no --skip-validate)
// and the OG list fetch.
func TestSetupIntegrationOAuthRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "ws1")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/ws1")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}
	srv := mockws1.New().Start()
	defer srv.Close()

	cfg := filepath.Join(tmp, "ws1cfg")
	_ = os.MkdirAll(cfg, 0o700)

	// --auth-url is supplied so the wizard skips the region-pick prompt.
	// Prompts exercised (in order): tenant, client id, client secret, OG pick.
	// Mock OG list returns 3 entries (Global/1, EMEA/2042, EMEA-Pilot/4067);
	// we pick the third (EMEA-Pilot, id=4067).
	c := exec.Command(bin, "setup",
		"--profile", "operator",
		"--auth-url", srv.URL+"/oauth",
		"--skip-smoke-test",
	)
	c.Env = append(os.Environ(),
		"WS1_BASE_URL="+srv.URL,
		"WS1_CONFIG_DIR="+cfg,
		"WS1_ALLOW_DISK_SECRETS=1",
		"WS1_FORCE_INTERACTIVE=1",
		"HOME="+cfg,
	)
	stdin, _ := c.StdinPipe()
	var out, errOut bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errOut
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wizard prompts: tenant, client id, client secret, og pick.
	// (Region is skipped because --auth-url is provided.)
	_, _ = io.WriteString(stdin, "demo.awmdm.com\n")
	_, _ = io.WriteString(stdin, "cid\n")
	_, _ = io.WriteString(stdin, "csec\n") // tests AskSecret on pipe
	_, _ = io.WriteString(stdin, "3\n")    // OG: EMEA-Pilot
	_ = stdin.Close()

	if err := c.Wait(); err != nil {
		t.Fatalf("setup failed: %v\nstdout: %s\nstderr: %s", err, out.String(), errOut.String())
	}
	if og := strings.TrimSpace(mustReadFile(t, filepath.Join(cfg, "og"))); og != "4067" {
		t.Errorf("og = %q, want 4067 (EMEA-Pilot)", og)
	}

	// Exit summary should appear in stderr.
	if !strings.Contains(errOut.String(), "Setup complete.") {
		t.Errorf("stderr missing 'Setup complete.'\nstderr: %s", errOut.String())
	}
}

func mustHaveProfileFile(t *testing.T, cfg, name string) {
	t.Helper()
	b := mustReadFile(t, filepath.Join(cfg, "profiles.yaml"))
	if !strings.Contains(b, "name: "+name) {
		t.Errorf("profiles.yaml missing %q\nbody: %s", name, b)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
