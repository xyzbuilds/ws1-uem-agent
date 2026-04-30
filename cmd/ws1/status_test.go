package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
)

// realHome captures HOME before any test overrides it so go build does
// not deposit the module cache inside the test's TempDir (which would
// fail cleanup due to read-only module files).
var realHome = os.Getenv("HOME")

// envWithKey returns a copy of base where any existing KEY=… entry is
// dropped and KEY=val is appended. This avoids duplicate-key ambiguity
// when callers use os.Environ() which may already contain the key.
func envWithKey(base []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(base)+1)
	for _, e := range base {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return append(out, key+"="+val)
}

func runWS1Status(t *testing.T, cfgDir string) envelope.Envelope {
	t.Helper()
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "ws1")
	buildCmd := exec.Command("go", "build", "-o", bin, ".")
	// Restore real HOME so the Go tool writes its module cache to the
	// user's actual GOPATH, not into the test's TempDir.
	buildCmd.Env = envWithKey(os.Environ(), "HOME", realHome)
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}
	runCmd := exec.Command(bin, "status")
	wsEnv := envWithKey(os.Environ(), "WS1_CONFIG_DIR", cfgDir)
	wsEnv = envWithKey(wsEnv, "HOME", cfgDir)
	runCmd.Env = wsEnv
	out, err := runCmd.CombinedOutput()
	if err != nil {
		t.Logf("output: %s", out)
	}
	// Filter envelope JSON line.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	last := lines[len(lines)-1]
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(last), &env); err != nil {
		t.Fatalf("parse %q: %v", last, err)
	}
	_ = cfgDir
	return env
}

func TestStatusEmptyConfig(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", cfg)
	t.Setenv("HOME", cfg)
	env := runWS1Status(t, cfg)
	if env.Operation != "ws1.status" {
		t.Errorf("op = %q", env.Operation)
	}
	if !env.OK {
		t.Errorf("ok=false: %+v", env.Error)
	}
	data, _ := env.Data.(map[string]any)
	if data["active_profile"] != "ro" {
		// First-run defaults: active = ro per auth.Active().
		t.Errorf("active_profile = %v, want ro", data["active_profile"])
	}
	cps, _ := data["configured_profiles"].([]any)
	if len(cps) != 0 {
		t.Errorf("configured_profiles = %v, want []", cps)
	}
}

func TestStatusInfersRegion(t *testing.T) {
	if testing.Short() {
		t.Skip("rebuild slow in -short")
	}
	cfg := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", cfg)
	t.Setenv("HOME", cfg)
	// Write a profile with an na auth_url.
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	profilesYAML := `version: 1
profiles:
- name: operator
  tenant: cn1506.awmdm.com
  api_url: https://cn1506.awmdm.com
  auth_url: https://na.uemauth.workspaceone.com/connect/token
  client_id: dummy
`
	if err := os.WriteFile(filepath.Join(cfg, "profiles.yaml"), []byte(profilesYAML), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "profile"), []byte("operator\n"), 0o600); err != nil {
		t.Fatalf("write active: %v", err)
	}
	env := runWS1Status(t, cfg)
	data, _ := env.Data.(map[string]any)
	if data["region_inferred"] != "na" {
		t.Errorf("region_inferred = %v, want na", data["region_inferred"])
	}
	if data["tenant"] != "cn1506.awmdm.com" {
		t.Errorf("tenant = %v", data["tenant"])
	}
}

func TestStatusUnknownRegion(t *testing.T) {
	if testing.Short() {
		t.Skip("rebuild slow in -short")
	}
	cfg := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", cfg)
	t.Setenv("HOME", cfg)
	profilesYAML := `version: 1
profiles:
- name: operator
  tenant: x.example.com
  api_url: https://x.example.com
  auth_url: https://custom-oauth.example.com/connect/token
  client_id: dummy
`
	_ = os.WriteFile(filepath.Join(cfg, "profiles.yaml"), []byte(profilesYAML), 0o600)
	_ = os.WriteFile(filepath.Join(cfg, "profile"), []byte("operator\n"), 0o600)
	env := runWS1Status(t, cfg)
	data, _ := env.Data.(map[string]any)
	if data["region_inferred"] != "unknown" {
		t.Errorf("region_inferred = %v, want unknown", data["region_inferred"])
	}
}
