// Command ws1 is the user-facing CLI that lets skill-capable agents operate
// Omnissa Workspace ONE UEM. See ws1-uem-agent-v0-spec.md for the full
// design and CLAUDE.md for binding conventions.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		// Cobra has already printed its own error message to stderr by the
		// time we reach here; we just need a non-zero exit. The actual exit
		// code for envelope errors is set inside the command run-funcs via
		// os.Exit before Execute returns, so falling through here with code
		// 5 means a bug in argv parsing or a programmer error.
		fmt.Fprintln(os.Stderr, "ws1: command error:", err)
		os.Exit(5)
	}
}
