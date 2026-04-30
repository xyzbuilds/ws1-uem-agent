package main

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/xyzbuilds/ws1-uem-agent/internal/audit"
	"github.com/xyzbuilds/ws1-uem-agent/internal/auth"
	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
)

// newStatusCmd is `ws1 status`: a single-envelope snapshot of current
// configuration. Reads from disk only — no API call. Replaces the
// three-call sequence (profile current + og current + audit tail
// --last 1) for agent introspection.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current configuration snapshot (profile, OG, region, recent audit)",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			env := envelope.New("ws1.status").WithData(buildStatusData()).WithDuration(time.Since(start))
			emitAndExit(env)
		},
	}
}

// buildStatusData composes the status envelope data. Pure function of
// disk state; never errors out (missing files become empty fields).
func buildStatusData() map[string]any {
	out := map[string]any{
		"active_profile":      "",
		"configured_profiles": []string{},
		"tenant":              "",
		"region_inferred":     "unknown",
		"og":                  nil,
		"audit_seq":           nil,
		"last_op_at":          nil,
	}

	if active, err := auth.Active(); err == nil {
		out["active_profile"] = active
	}

	profiles, _ := auth.LoadProfiles()
	names := make([]string, 0, len(profiles))
	var activeProfile *auth.Profile
	activeName, _ := out["active_profile"].(string)
	for i := range profiles {
		names = append(names, profiles[i].Name)
		if profiles[i].Name == activeName {
			activeProfile = &profiles[i]
		}
	}
	out["configured_profiles"] = names

	if activeProfile != nil {
		out["tenant"] = activeProfile.Tenant
		out["region_inferred"] = inferRegionFromAuthURL(activeProfile.AuthURL)
	}

	if og, err := auth.CurrentOG(); err == nil && og != "" {
		out["og"] = map[string]any{"id": og}
	}

	// Tail of the audit log: latest entry's seq + ts.
	if path, err := audit.DefaultPath(); err == nil {
		if l, lerr := audit.New(path); lerr == nil {
			if entries, terr := l.Tail(1); terr == nil && len(entries) > 0 {
				e := entries[len(entries)-1]
				out["audit_seq"] = e.Seq
				out["last_op_at"] = e.Ts
			}
		}
	}

	return out
}

// inferRegionFromAuthURL reverse-maps an auth_url to a region code.
// Returns "unknown" if no region in cmd/ws1/regions.go matches.
func inferRegionFromAuthURL(authURL string) string {
	for _, r := range Regions {
		if r.TokenURL == authURL {
			return r.Code
		}
	}
	return "unknown"
}
