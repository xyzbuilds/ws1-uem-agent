package auth

import (
	"errors"
	"fmt"
	"os"

	"github.com/zalando/go-keyring"
)

// keychainService is the constant identifier under which all ws1 secrets
// are stored. Per-profile secrets are keyed by Username.
const keychainService = "ws1-uem-agent"

// keychainAccount returns the OS-keychain account name for a given profile.
// The format is `<profile>:<client_id>` so a profile reconfiguration with a
// new client_id does not collide with the old secret.
func keychainAccount(profile, clientID string) string {
	return profile + ":" + clientID
}

// SaveClientSecret writes the OAuth client_secret for a profile to the OS
// keychain (macOS Keychain, Windows DPAPI/wincred, Linux secret-service).
//
// If WS1_ALLOW_DISK_SECRETS is set, the secret is written directly to
// ~/.config/ws1/secrets.<profile>.<client_id>.plain with mode 0600,
// bypassing the OS keychain entirely. This is the open-question-#1
// default per spec section 13 — and is required for tests, since the
// macOS Keychain `security` CLI blocks waiting for an interactive
// permission prompt rather than failing fast when invoked from a
// non-foreground binary like `go test`.
//
// If the keychain is unavailable (e.g. headless Linux without
// secret-service) AND WS1_ALLOW_DISK_SECRETS is unset, the call
// returns an error directing the user to set the env var.
func SaveClientSecret(profile, clientID, secret string) error {
	if os.Getenv("WS1_ALLOW_DISK_SECRETS") != "" {
		return saveDiskSecret(profile, clientID, secret)
	}
	if err := keyring.Set(keychainService, keychainAccount(profile, clientID), secret); err != nil {
		return fmt.Errorf("keyring set: %w (set WS1_ALLOW_DISK_SECRETS=1 to permit encrypted-at-rest fallback)", err)
	}
	return nil
}

// GetClientSecret reads the client_secret for a profile. When
// WS1_ALLOW_DISK_SECRETS is set, the disk-stored secret is preferred
// (mirroring SaveClientSecret); otherwise the OS keychain is consulted
// first and disk is the fallback for keyring.ErrNotFound.
func GetClientSecret(profile, clientID string) (string, error) {
	if os.Getenv("WS1_ALLOW_DISK_SECRETS") != "" {
		if v, derr := loadDiskSecret(profile, clientID); derr == nil {
			return v, nil
		}
	}
	v, err := keyring.Get(keychainService, keychainAccount(profile, clientID))
	if errors.Is(err, keyring.ErrNotFound) {
		// Try disk fallback if explicitly allowed.
		if os.Getenv("WS1_ALLOW_DISK_SECRETS") != "" {
			if v, derr := loadDiskSecret(profile, clientID); derr == nil {
				return v, nil
			}
		}
		return "", fmt.Errorf("no client_secret stored for profile %q (run `ws1 profile add %s ...`)", profile, profile)
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// DeleteClientSecret removes the secret for a profile. Best-effort.
func DeleteClientSecret(profile, clientID string) error {
	_ = keyring.Delete(keychainService, keychainAccount(profile, clientID))
	return removeDiskSecret(profile, clientID)
}

// --- disk fallback (only used when WS1_ALLOW_DISK_SECRETS is set) ---------
//
// We deliberately do NOT encrypt at rest here in v0; the gate is "user has
// explicitly accepted that secret is on disk". A future v0.5 hardens this
// with a passphrase-derived AES wrap.

func diskSecretPath(profile, clientID string) (string, error) {
	root, err := configRoot()
	if err != nil {
		return "", err
	}
	return root + "/secrets." + profile + "." + clientID + ".plain", nil
}

func saveDiskSecret(profile, clientID, secret string) error {
	root, err := configRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	path, _ := diskSecretPath(profile, clientID)
	return os.WriteFile(path, []byte(secret), 0o600)
}

func loadDiskSecret(profile, clientID string) (string, error) {
	path, err := diskSecretPath(profile, clientID)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	return string(b), err
}

func removeDiskSecret(profile, clientID string) error {
	path, err := diskSecretPath(profile, clientID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return os.Remove(path)
}
