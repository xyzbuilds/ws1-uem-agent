package main

import "os"

// getenv is a tiny wrapper so cmd/ws1 doesn't sprinkle os.Getenv everywhere
// and so tests can stub it out if needed.
func getenv(key string) string { return os.Getenv(key) }
