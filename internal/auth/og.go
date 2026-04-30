package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// OG (organization group) context per spec section 4.3. Every state-changing
// command requires --og or a default set via `ws1 og use`. Missing OG
// returns TENANT_REQUIRED.
//
// The active OG ID is stored in ~/.config/ws1/og as a single line.

func ogFilePath() (string, error) {
	root, err := configRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "og"), nil
}

// CurrentOG returns the active OG ID, or empty string if none is set.
// Honors --og override at the command layer; this only reads the persisted
// default.
func CurrentOG() (string, error) {
	path, err := ogFilePath()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// SetOG persists the active OG ID. Pass empty string to clear.
func SetOG(id string) error {
	root, err := configRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	path, _ := ogFilePath()
	if id == "" {
		_ = os.Remove(path)
		return nil
	}
	return os.WriteFile(path, []byte(id+"\n"), 0o600)
}

// ResolveOG returns the OG ID to use for this command, in precedence order:
//  1. --og flag (passed in from the command layer)
//  2. WS1_OG env var (test convenience)
//  3. persisted default
//
// Returns empty string when none is set; callers should emit TENANT_REQUIRED.
func ResolveOG(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if v := os.Getenv("WS1_OG"); v != "" {
		return v, nil
	}
	return CurrentOG()
}
