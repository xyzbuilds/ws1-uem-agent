package main

import (
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/zhangxuyang/ws1-uem-agent/internal/auth"
	"github.com/zhangxuyang/ws1-uem-agent/internal/envelope"
)

func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage auth profiles (ro / operator / admin)",
		Long: `Profiles control which class of operations the CLI can execute.
ro is read-only; operator can write and (with approval) destructive;
admin is the same as operator with elevated tenant capabilities.

Switching profile is user-only: ` + "`ws1 profile use`" + ` refuses to run when
the CLI is not attached to a terminal, so an agent cannot escalate its own
permissions via argv.`,
	}
	cmd.AddCommand(
		newProfileCurrentCmd(),
		newProfileListCmd(),
		newProfileUseCmd(),
		newProfileAddCmd(),
	)
	return cmd
}

func newProfileCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Print the active profile name",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			active, err := auth.Active()
			if err != nil {
				emitAndExit(envelope.NewError("ws1.profile.current",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			emitAndExit(envelope.New("ws1.profile.current").
				WithData(map[string]any{"active": active}).
				WithDuration(time.Since(start)))
		},
	}
}

func newProfileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured profiles",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			profiles, err := auth.LoadProfiles()
			if err != nil {
				emitAndExit(envelope.NewError("ws1.profile.list",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			active, _ := auth.Active()
			rows := make([]map[string]any, 0, len(profiles))
			for _, p := range profiles {
				rows = append(rows, map[string]any{
					"name":      p.Name,
					"tenant":    p.Tenant,
					"client_id": p.ClientID,
					"active":    p.Name == active,
				})
			}
			emitAndExit(envelope.New("ws1.profile.list").
				WithData(map[string]any{
					"profiles": rows,
					"active":   active,
				}).
				WithDuration(time.Since(start)))
		},
	}
}

func newProfileUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Switch the active profile (terminal only)",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			err := auth.SwitchActive(args[0], auth.IsInteractive())
			if errors.Is(err, auth.ErrInteractiveRequired) {
				emitAndExit(envelope.NewError("ws1.profile.use",
					envelope.CodeAuthInsufficientForOp,
					"Profile switch requires an interactive terminal; the CLI refuses to escalate via agent argv.").
					WithErrorDetails(map[string]any{
						"requested_profile": args[0],
						"reason":            "non-interactive caller (CLAUDE.md locked decision #5)",
					}).
					WithDuration(time.Since(start)))
				return
			}
			if err != nil {
				emitAndExit(envelope.NewError("ws1.profile.use",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			emitAndExit(envelope.New("ws1.profile.use").
				WithData(map[string]any{"active": args[0]}).
				WithDuration(time.Since(start)))
		},
	}
}

func newProfileAddCmd() *cobra.Command {
	var (
		tenant, apiURL, authURL, clientID, clientSecret string
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Configure a profile (writes secret to OS keychain)",
		Long: `Configure a profile by name (ro / operator / admin). Stores tenant +
client_id in ~/.config/ws1/profiles.yaml and the client_secret in the OS
keychain (macOS Keychain, Windows wincred, Linux secret-service).`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			name := args[0]
			if !auth.IsValidProfile(name) {
				emitAndExit(envelope.NewError("ws1.profile.add",
					envelope.CodeIdentifierAmbiguous,
					"profile name must be one of ro / operator / admin").
					WithDuration(time.Since(start)))
				return
			}
			if !auth.IsInteractive() {
				emitAndExit(envelope.NewError("ws1.profile.add",
					envelope.CodeAuthInsufficientForOp,
					"profile add requires an interactive terminal").
					WithDuration(time.Since(start)))
				return
			}
			if tenant == "" || clientID == "" || clientSecret == "" {
				emitAndExit(envelope.NewError("ws1.profile.add",
					envelope.CodeIdentifierAmbiguous,
					"--tenant, --client-id, --client-secret are all required").
					WithDuration(time.Since(start)))
				return
			}
			if apiURL == "" {
				apiURL = "https://" + tenant
			}
			if authURL == "" {
				authURL = "https://" + tenant + "/AirWatch/oauth2/token"
			}
			p := auth.Profile{
				Name: name, Tenant: tenant, APIURL: apiURL,
				AuthURL: authURL, ClientID: clientID,
			}
			if err := auth.SaveProfile(p); err != nil {
				emitAndExit(envelope.NewError("ws1.profile.add",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			if err := auth.SaveClientSecret(name, clientID, clientSecret); err != nil {
				emitAndExit(envelope.NewError("ws1.profile.add",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			emitAndExit(envelope.New("ws1.profile.add").
				WithData(map[string]any{
					"name":      name,
					"tenant":    tenant,
					"client_id": clientID,
					"secret":    "stored in OS keychain",
				}).
				WithDuration(time.Since(start)))
		},
	}
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant hostname (e.g. as1831.awmdm.com)")
	cmd.Flags().StringVar(&apiURL, "api-url", "", "API base URL (default https://<tenant>)")
	cmd.Flags().StringVar(&authURL, "auth-url", "", "OAuth token endpoint (default https://<tenant>/AirWatch/oauth2/token)")
	cmd.Flags().StringVar(&clientID, "client-id", "", "OAuth client_id")
	cmd.Flags().StringVar(&clientSecret, "client-secret", "", "OAuth client_secret (stored in OS keychain)")
	return cmd
}
