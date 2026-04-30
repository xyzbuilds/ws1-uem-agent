package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
	"github.com/xyzbuilds/ws1-uem-agent/internal/version"
)

// rootFlags holds persistent flags shared by every subcommand. They're parsed
// onto a process-wide value so command run-funcs can read them without going
// through the cobra/viper machinery on every call.
type rootFlags struct {
	jsonOutput  bool
	og          string
	profile     string
	dryRun      bool
	versionFlag bool
}

var globalFlags rootFlags

// emitAndExit is the canonical exit path for commands that produce envelopes.
// It writes the envelope to stdout (one line, no trailing newline glitching
// with downstream parsers), flushes, and exits with the envelope's process
// exit code. Anything that prints to stdout outside this function is a bug.
func emitAndExit(env *envelope.Envelope) {
	b, err := env.JSON()
	if err != nil {
		// Fall back to a minimal hand-built envelope so we never print
		// nothing on stdout — that would break agents.
		fmt.Fprintln(os.Stderr, "ws1: envelope marshal failed:", err)
		fmt.Fprintln(os.Stdout, `{"envelope_version":1,"ok":false,"operation":"unknown","error":{"code":"INTERNAL_ERROR","message":"envelope marshal failed"},"meta":{"duration_ms":0}}`)
		os.Exit(envelope.ExitInternalError)
	}
	fmt.Fprintln(os.Stdout, string(b))
	os.Exit(env.ExitCode())
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ws1",
		Short: "Operate Omnissa Workspace ONE UEM safely from skill-capable agents",
		Long: `ws1 is a CLI substrate for skill-capable agents to drive Omnissa
Workspace ONE UEM via natural-language goals. Every command emits a
versioned JSON envelope on stdout (see 'ws1 ops describe' for shapes) and
gates destructive operations behind a browser approval flow.`,
		// Disable cobra's auto-generated --version flag — we want to render
		// version through the envelope, not as plain text.
		SilenceUsage:  true,
		SilenceErrors: true,
		// If --version is set on the root command without a subcommand,
		// short-circuit to the version envelope. Cobra's default is to
		// print a hard-coded template, which we don't want.
		RunE: func(cmd *cobra.Command, args []string) error {
			if globalFlags.versionFlag {
				emitAndExit(buildVersionEnvelope())
				return nil
			}
			return cmd.Help()
		},
	}

	cmd.PersistentFlags().BoolVar(&globalFlags.jsonOutput, "json", true,
		"emit JSON envelopes on stdout (true by default; flag is reserved for future human-readable output)")
	cmd.PersistentFlags().StringVar(&globalFlags.og, "og", "",
		"organization group ID to scope this command (overrides 'ws1 og use'); required for most ops")
	cmd.PersistentFlags().StringVar(&globalFlags.profile, "profile", "",
		"auth profile to use for this command (overrides 'ws1 profile use'); ro/operator/admin")
	cmd.PersistentFlags().BoolVar(&globalFlags.dryRun, "dry-run", false,
		"plan and validate but do not call any state-changing API")

	cmd.Flags().BoolVar(&globalFlags.versionFlag, "version", false,
		"print version envelope and exit")

	// Bind to viper so config-file overrides work later. Per CLAUDE.md the
	// CLI must refuse to switch profiles based on its own argv when called
	// non-interactively; --profile is a per-invocation override, not a
	// persistent setting, so this binding is safe.
	_ = viper.BindPFlag("og", cmd.PersistentFlags().Lookup("og"))
	_ = viper.BindPFlag("profile", cmd.PersistentFlags().Lookup("profile"))

	cmd.AddCommand(
		newDoctorCmd(),
		newProfileCmd(),
		newOgCmd(),
		newOpsCmd(),
		newAuditCmd(),
	)
	// Generic auto-registration: every op in internal/generated.Ops
	// becomes a `ws1 <section> <tag> <verb>` subcommand.
	registerSectionCommands(cmd)

	return cmd
}

// buildVersionEnvelope is the structured response for `ws1 --version --json`.
// Section 4.1 of the spec lists this as one of the five always-on
// self-describing commands.
func buildVersionEnvelope() *envelope.Envelope {
	return envelope.New("ws1.version").
		WithData(map[string]any{
			"version":      version.Version,
			"commit":       version.Commit,
			"build_date":   version.BuildDate,
			"spec_version": version.SpecVersion,
		}).
		WithVersion(version.SpecVersion, version.Version)
}
