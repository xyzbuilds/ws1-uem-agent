// Package auth implements the OAuth client-credentials flow, OS-keychain
// secret storage, and the three-profile capability model (ro / operator /
// admin) defined in spec section 4.2 and CLAUDE.md locked decision #5.
//
// Profile switching is user-initiated only. The CLI refuses to switch
// profiles via argv when called non-interactively (see SwitchActive).
package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Profile names — three discrete capability tiers per spec section 4.2.
const (
	ProfileRO       = "ro"
	ProfileOperator = "operator"
	ProfileAdmin    = "admin"
)

// ValidProfiles is the closed set of profile names. Adding a fourth would
// be a design change.
var ValidProfiles = []string{ProfileRO, ProfileOperator, ProfileAdmin}

// IsValidProfile reports whether name is one of the canonical profile names.
func IsValidProfile(name string) bool {
	for _, p := range ValidProfiles {
		if p == name {
			return true
		}
	}
	return false
}

// Profile is the configuration for one of the three tiers. The client_secret
// is intentionally NOT in this struct: secrets live in the OS keychain and
// are fetched via keychain.Get(profile.Name).
type Profile struct {
	Name     string `yaml:"name"`
	Tenant   string `yaml:"tenant"`     // tenant hostname, e.g. as1831.awmdm.com
	APIURL   string `yaml:"api_url"`    // base URL for API calls, e.g. https://as1831.awmdm.com
	AuthURL  string `yaml:"auth_url"`   // OAuth token endpoint
	ClientID string `yaml:"client_id"`  // OAuth client_id (not secret)
	OG       string `yaml:"og,omitempty"` // optional default OG, overridable at runtime
}

// Capability returns the operation classes this profile is permitted to
// execute. Destructive ops are always gated by the approval flow regardless
// of profile.
func (p *Profile) Capability() []string {
	switch p.Name {
	case ProfileRO:
		return []string{"read"}
	case ProfileOperator:
		return []string{"read", "write", "destructive"}
	case ProfileAdmin:
		return []string{"read", "write", "destructive"}
	default:
		return nil
	}
}

// Can reports whether this profile may execute an op of the given class.
// Returns false for unknown classes (fail-closed).
func (p *Profile) Can(class string) bool {
	for _, c := range p.Capability() {
		if c == class {
			return true
		}
	}
	return false
}

// configRoot is ~/.config/ws1, mirroring the rest of the CLI.
func configRoot() (string, error) {
	if v := os.Getenv("WS1_CONFIG_DIR"); v != "" {
		return v, nil // test override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ws1"), nil
}

func profilesFilePath() (string, error) {
	root, err := configRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "profiles.yaml"), nil
}

func activeFilePath() (string, error) {
	root, err := configRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "profile"), nil
}

// LoadProfiles reads profiles.yaml. Missing file is not an error; it returns
// an empty slice so first-run UX is sensible.
func LoadProfiles() ([]Profile, error) {
	path, err := profilesFilePath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Version  int       `yaml:"version"`
		Profiles []Profile `yaml:"profiles"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i, p := range doc.Profiles {
		if !IsValidProfile(p.Name) {
			return nil, fmt.Errorf("profile %d: invalid name %q (want one of %v)", i, p.Name, ValidProfiles)
		}
	}
	sort.Slice(doc.Profiles, func(i, j int) bool { return doc.Profiles[i].Name < doc.Profiles[j].Name })
	return doc.Profiles, nil
}

// SaveProfile upserts a profile by name.
func SaveProfile(p Profile) error {
	if !IsValidProfile(p.Name) {
		return fmt.Errorf("invalid profile name %q (want one of %v)", p.Name, ValidProfiles)
	}
	root, err := configRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	profiles, err := LoadProfiles()
	if err != nil {
		return err
	}
	replaced := false
	for i, ex := range profiles {
		if ex.Name == p.Name {
			profiles[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		profiles = append(profiles, p)
	}
	doc := struct {
		Version  int       `yaml:"version"`
		Profiles []Profile `yaml:"profiles"`
	}{Version: 1, Profiles: profiles}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	path, _ := profilesFilePath()
	return os.WriteFile(path, b, 0o600)
}

// FindProfile returns a profile by name, or an error if not configured.
func FindProfile(name string) (*Profile, error) {
	profiles, err := LoadProfiles()
	if err != nil {
		return nil, err
	}
	for _, p := range profiles {
		if p.Name == name {
			return &p, nil
		}
	}
	return nil, fmt.Errorf("profile %q is not configured (run `ws1 profile add %s`)", name, name)
}

// Active returns the name of the currently-active profile. Defaults to "ro"
// (the safest tier per spec section 4.2) when no active profile is set.
func Active() (string, error) {
	path, err := activeFilePath()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ProfileRO, nil
	}
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(b))
	if !IsValidProfile(name) {
		return "", fmt.Errorf("active profile file holds invalid value %q", name)
	}
	return name, nil
}

// SetActive writes the active profile name to disk. Must NOT be called from
// a CLI argv path that originated in non-interactive mode (per CLAUDE.md
// locked decision #5); use SwitchActive for that gate.
func SetActive(name string) error {
	if !IsValidProfile(name) {
		return fmt.Errorf("invalid profile name %q", name)
	}
	root, err := configRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	path, _ := activeFilePath()
	return os.WriteFile(path, []byte(name+"\n"), 0o600)
}

// ErrInteractiveRequired is returned by SwitchActive when the CLI is not
// attached to a terminal.
var ErrInteractiveRequired = errors.New("profile switch requires an interactive terminal")

// SwitchActive is the user-facing entry point for `ws1 profile use <name>`.
// It refuses to switch when called non-interactively, satisfying the
// "agent cannot escalate its own profile" property in CLAUDE.md.
//
// `interactive` should come from auth.IsInteractive(); injected so tests
// can simulate both modes without having to fake a TTY.
func SwitchActive(name string, interactive bool) error {
	if !interactive {
		return fmt.Errorf("%w: switching to %q must be run from a terminal", ErrInteractiveRequired, name)
	}
	if !IsValidProfile(name) {
		return fmt.Errorf("invalid profile name %q (want one of %v)", name, ValidProfiles)
	}
	if _, err := FindProfile(name); err != nil {
		return err
	}
	return SetActive(name)
}
