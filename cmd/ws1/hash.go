package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// shortHash returns a short SHA-256 of s, prefixed "sha256:". Used for the
// audit entry's args_hash field where we want a readable but stable
// fingerprint of the command + targets.
func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(h[:])[:32]
}

// stderrWriter is a tiny indirection so tests can swap the destination if
// they ever need to assert on stderr-going content. Most code writes
// directly to os.Stderr; this exists for cases (like audit append errors
// in lock.go) where we want to make the dependency explicit.
var stderrWriter io.Writer = os.Stderr
