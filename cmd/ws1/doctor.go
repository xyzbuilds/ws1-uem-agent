package main

import (
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/zhangxuyang/ws1-uem-agent/internal/envelope"
	"github.com/zhangxuyang/ws1-uem-agent/internal/version"
)

// doctorCheck is one row in the doctor envelope's data.checks array.
type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // pass | fail | skip
	Message string `json:"message"`
}

// configDir returns ~/.config/ws1, mirroring the rest of the CLI's
// convention. It does NOT mkdir the path; doctor reports presence.
func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ws1"), nil
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Validate environment, config, and tenant connectivity",
		Long: `doctor runs a series of checks and emits a structured pass/fail
envelope on stdout. v0 implements only the local-only checks (config dir,
binary identity, runtime); auth and tenant connectivity checks land in
later sessions.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			checks := runDoctorChecks()

			ok := true
			for _, c := range checks {
				if c.Status == "fail" {
					ok = false
					break
				}
			}

			env := envelope.New("ws1.doctor").
				WithData(map[string]any{
					"checks":  checks,
					"summary": summariseChecks(checks),
				}).
				WithDuration(time.Since(start)).
				WithVersion(version.SpecVersion, version.Version)
			if !ok {
				env = envelope.NewError("ws1.doctor",
					envelope.CodeInternalError,
					"one or more doctor checks failed").
					WithErrorDetails(map[string]any{
						"checks": checks,
					}).
					WithDuration(time.Since(start)).
					WithVersion(version.SpecVersion, version.Version)
			}
			emitAndExit(env)
		},
	}
}

// runDoctorChecks runs every implemented check and returns the rows in a
// stable order. Adding a check should mean a new check function and a row in
// this slice — keep ordering stable for readers.
func runDoctorChecks() []doctorCheck {
	return []doctorCheck{
		checkBinaryIdentity(),
		checkRuntime(),
		checkConfigDir(),
		// Stubs for auth + tenant connectivity, intentionally returning skip
		// so tests can assert their presence without v0 plumbing.
		{Name: "auth_profile_loaded", Status: "skip", Message: "v0 stub: profile model lands in a later session"},
		{Name: "tenant_reachable", Status: "skip", Message: "v0 stub: connectivity check lands in a later session"},
		{Name: "policy_yaml_loaded", Status: "skip", Message: "v0 stub: policy loader lands in a later session"},
	}
}

func checkBinaryIdentity() doctorCheck {
	if version.Version == "0.0.0-dev" {
		return doctorCheck{
			Name:    "binary_identity",
			Status:  "pass",
			Message: "running unstamped dev build " + version.Version,
		}
	}
	return doctorCheck{
		Name:    "binary_identity",
		Status:  "pass",
		Message: "version=" + version.Version + " commit=" + version.Commit,
	}
}

func checkRuntime() doctorCheck {
	return doctorCheck{
		Name:    "runtime",
		Status:  "pass",
		Message: runtime.Version() + " on " + runtime.GOOS + "/" + runtime.GOARCH,
	}
}

func checkConfigDir() doctorCheck {
	dir, err := configDir()
	if err != nil {
		return doctorCheck{Name: "config_dir", Status: "fail", Message: "cannot resolve $HOME: " + err.Error()}
	}
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		// Not a failure on first run — the dir is created lazily by
		// commands that write to it. Doctor just notes the state.
		return doctorCheck{Name: "config_dir", Status: "pass", Message: dir + " (not yet created; will be on first write)"}
	}
	if err != nil {
		return doctorCheck{Name: "config_dir", Status: "fail", Message: "stat " + dir + ": " + err.Error()}
	}
	if !info.IsDir() {
		return doctorCheck{Name: "config_dir", Status: "fail", Message: dir + " exists but is not a directory"}
	}
	return doctorCheck{Name: "config_dir", Status: "pass", Message: dir + " exists"}
}

// summariseChecks counts each status so an agent can branch quickly.
func summariseChecks(checks []doctorCheck) map[string]int {
	out := map[string]int{"pass": 0, "fail": 0, "skip": 0}
	for _, c := range checks {
		out[c.Status]++
	}
	return out
}
