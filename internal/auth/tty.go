package auth

import (
	"os"

	"golang.org/x/term"
)

// IsInteractive reports whether the CLI is attached to a real terminal on
// stdin AND stdout. Agents pipe both, so they get false. This is the gate
// for SwitchActive — see CLAUDE.md locked decision #5.
//
// Test override: WS1_FORCE_INTERACTIVE=1 makes this return true regardless,
// for unit tests that exercise the interactive path on CI runners.
func IsInteractive() bool {
	if os.Getenv("WS1_FORCE_INTERACTIVE") != "" {
		return true
	}
	if os.Getenv("WS1_FORCE_NONINTERACTIVE") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}
