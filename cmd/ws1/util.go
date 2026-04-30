package main

import (
	"io"
	"os"
)

// getenv is a tiny wrapper so cmd/ws1 doesn't sprinkle os.Getenv everywhere
// and so tests can stub it out if needed.
func getenv(key string) string { return os.Getenv(key) }

// stderrWriter is the swap-point for diagnostic output. Most code writes
// directly to os.Stderr; this exists so tests and the generic command
// dispatch can swap the destination if needed.
var stderrWriter io.Writer = os.Stderr
