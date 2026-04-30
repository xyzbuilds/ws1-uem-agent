package main

import (
	"strings"

	"github.com/xyzbuilds/ws1-uem-agent/internal/api"
	"github.com/xyzbuilds/ws1-uem-agent/internal/generated"
	"github.com/xyzbuilds/ws1-uem-agent/internal/policy"
)

// dangerousCommandNames are values for the runtime --command / --commandName
// arg that flip an otherwise write-class command-dispatcher op into
// destructive + always-required-approval. Static op-level classification
// can't see this — `mdmv2.commandsv2.execute` is the same op whether you
// pass commandName=Lock (reversible) or commandName=DeviceWipe
// (irreversible).
//
// Conservative list: anything that wipes data, breaks management, or
// resets device state to factory. Adding to this list is a safety
// tightening; removing requires a design discussion.
var dangerousCommandNames = map[string]bool{
	"EnterpriseWipe":  true,
	"DeviceWipe":      true,
	"Wipe":            true,
	"FactoryWipe":     true,
	"BreakMDM":        true,
	"EnterpriseReset": true,
	"FactoryReset":    true,
	"SoftReset":       true,
	"Shutdown":        true,
}

// escalateForDangerousCommand inspects the user's argv against the op's
// path shape and elevates the classification when the runtime command
// is destructive even though the op-level entry is write-class.
//
// Targets two known shapes:
//   - /devices/{deviceUuid}/commands/{commandName}  (single, v2/v3)
//   - /devices/commands/{commandName}               (bulk, v2/v3)
//   - /devices/{id}/commands                        (v1) — `command`
//     is in the body or a query param
//
// The arg name varies (`commandName` in path vs `command` as query/body);
// we check both.
func escalateForDangerousCommand(meta generated.OpMeta, args api.Args, entry policy.Entry) policy.Entry {
	if !looksLikeCommandDispatcher(meta) {
		return entry
	}
	val := commandValueFrom(args)
	if val == "" || !dangerousCommandNames[val] {
		return entry
	}
	entry.Class = policy.ClassDestructive
	entry.Approval = policy.ApprovalAlwaysRequired
	if entry.Reversible == "" || entry.Reversible == policy.ReversibleUnknown {
		entry.Reversible = policy.ReversibleNone
	}
	if entry.Warn == "" {
		entry.Warn = "command name flagged dangerous at runtime; escalated to destructive."
	}
	return entry
}

// looksLikeCommandDispatcher matches the path shapes WS1 uses for
// generic device-command endpoints. We only escalate within these
// patterns so a write-class op that happens to take a `command` arg
// for unrelated reasons isn't accidentally treated as destructive.
func looksLikeCommandDispatcher(meta generated.OpMeta) bool {
	p := meta.PathTemplate
	switch {
	case strings.Contains(p, "/commands/{commandName}"):
		return true
	case strings.Contains(p, "/commands/{command}"):
		return true
	case strings.HasSuffix(p, "/commands"):
		// v1 dispatcher: POST /devices/{id}/commands with command in body.
		return true
	}
	return false
}

// commandValueFrom extracts the command name from args. WS1 uses
// `commandName` for path-templated forms and `command` for query/body
// forms; we accept either.
func commandValueFrom(args api.Args) string {
	for _, key := range []string{"commandName", "command", "Command", "CommandName"} {
		if v, ok := args[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
