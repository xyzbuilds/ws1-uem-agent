package auth

import (
	"testing"
)

func TestIsValidProfile(t *testing.T) {
	for _, name := range []string{"ro", "operator", "admin"} {
		if !IsValidProfile(name) {
			t.Errorf("IsValidProfile(%q) = false", name)
		}
	}
	for _, name := range []string{"", "ROOT", "user", "superadmin"} {
		if IsValidProfile(name) {
			t.Errorf("IsValidProfile(%q) = true (should be false)", name)
		}
	}
}

func TestProfileCapability(t *testing.T) {
	cases := []struct {
		profile string
		canRead bool
		canWrite bool
		canDestroy bool
	}{
		{ProfileRO, true, false, false},
		{ProfileOperator, true, true, true},
		{ProfileAdmin, true, true, true},
	}
	for _, tc := range cases {
		p := &Profile{Name: tc.profile}
		if got := p.Can("read"); got != tc.canRead {
			t.Errorf("%s.Can(read) = %v, want %v", tc.profile, got, tc.canRead)
		}
		if got := p.Can("write"); got != tc.canWrite {
			t.Errorf("%s.Can(write) = %v, want %v", tc.profile, got, tc.canWrite)
		}
		if got := p.Can("destructive"); got != tc.canDestroy {
			t.Errorf("%s.Can(destructive) = %v, want %v", tc.profile, got, tc.canDestroy)
		}
	}
}

func TestProfileCantUnknownClass(t *testing.T) {
	p := &Profile{Name: ProfileAdmin}
	if p.Can("magic") {
		t.Error("admin should fail-closed on unknown class")
	}
}

// TestSwitchActiveRefusesNonInteractive guards CLAUDE.md locked decision #5:
// the CLI must refuse to switch profiles via argv when called
// non-interactively. This is the safety property; if it ever flips, agents
// could escalate themselves silently.
func TestSwitchActiveRefusesNonInteractive(t *testing.T) {
	t.Setenv("WS1_CONFIG_DIR", t.TempDir())
	err := SwitchActive(ProfileOperator, false /* not interactive */)
	if err == nil {
		t.Fatal("SwitchActive(_, false) returned nil; should return ErrInteractiveRequired")
	}
	if !contains(err.Error(), "interactive") {
		t.Errorf("error message missing 'interactive': %v", err)
	}
}

func TestSwitchActiveAcceptsInteractive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", dir)
	if err := SaveProfile(Profile{
		Name: ProfileOperator, Tenant: "test.tenant",
		ClientID: "cid", APIURL: "https://x", AuthURL: "https://x/oauth",
	}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := SwitchActive(ProfileOperator, true); err != nil {
		t.Fatalf("SwitchActive(_, true): %v", err)
	}
	got, err := Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if got != ProfileOperator {
		t.Errorf("Active = %q, want operator", got)
	}
}

func TestSwitchActiveRejectsUnknownName(t *testing.T) {
	t.Setenv("WS1_CONFIG_DIR", t.TempDir())
	err := SwitchActive("superadmin", true)
	if err == nil {
		t.Fatal("SwitchActive expected to reject unknown profile name")
	}
}

func TestSwitchActiveRejectsUnconfiguredProfile(t *testing.T) {
	t.Setenv("WS1_CONFIG_DIR", t.TempDir())
	err := SwitchActive(ProfileAdmin, true)
	if err == nil {
		t.Fatal("SwitchActive should reject a profile that isn't in profiles.yaml")
	}
}

func TestActiveDefaultsToRO(t *testing.T) {
	t.Setenv("WS1_CONFIG_DIR", t.TempDir())
	got, err := Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if got != ProfileRO {
		t.Errorf("Active default = %q, want ro", got)
	}
}

func TestSetOGAndCurrent(t *testing.T) {
	t.Setenv("WS1_CONFIG_DIR", t.TempDir())
	if err := SetOG("12345"); err != nil {
		t.Fatalf("SetOG: %v", err)
	}
	got, _ := CurrentOG()
	if got != "12345" {
		t.Errorf("CurrentOG = %q", got)
	}
	if err := SetOG(""); err != nil {
		t.Fatalf("SetOG empty: %v", err)
	}
	got, _ = CurrentOG()
	if got != "" {
		t.Errorf("CurrentOG after clear = %q", got)
	}
}

func TestResolveOGPrecedence(t *testing.T) {
	t.Setenv("WS1_CONFIG_DIR", t.TempDir())
	t.Setenv("WS1_OG", "")

	// 1. flag wins
	t.Setenv("WS1_OG", "from-env")
	got, _ := ResolveOG("from-flag")
	if got != "from-flag" {
		t.Errorf("flag should win: got %q", got)
	}

	// 2. env wins over disk
	_ = SetOG("from-disk")
	got, _ = ResolveOG("")
	if got != "from-env" {
		t.Errorf("env should win over disk: got %q", got)
	}

	// 3. disk fallback
	t.Setenv("WS1_OG", "")
	got, _ = ResolveOG("")
	if got != "from-disk" {
		t.Errorf("disk should be the last resort: got %q", got)
	}
}

// contains is a small helper to avoid pulling strings into every test.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
