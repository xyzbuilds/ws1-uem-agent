package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/xyzbuilds/ws1-uem-agent/internal/auth"
	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
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
		newProfileRegionsCmd(),
	)
	return cmd
}

// newProfileRegionsCmd emits the canonical region/data-center/token-URL
// table as an envelope so an agent can discover valid --region values
// without having to read the docs. Output is the cmd/ws1/regions.go
// Regions slice — same source of truth `profile add` uses.
func newProfileRegionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "regions",
		Short: "List supported OAuth regions and their token URLs",
		Long: `Print the canonical mapping of region code -> data center -> customer
geos -> token URL, sourced from the Omnissa Workspace ONE UEM docs.
Use the 'code' field with 'ws1 profile add --region <code>'.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			rows := make([]map[string]any, 0, len(Regions))
			for _, r := range Regions {
				rows = append(rows, map[string]any{
					"code":        r.Code,
					"data_center": r.DataCenter,
					"customers":   r.Customers,
					"token_url":   r.TokenURL,
				})
			}
			emitAndExit(envelope.New("ws1.profile.regions").
				WithData(map[string]any{
					"regions": rows,
					"source":  "https://docs.omnissa.com/bundle/WorkspaceONE-UEM-Console-BasicsVSaaS/page/UsingUEMFunctionalityWithRESTAPI.html",
					"note":    "legacy URLs on uemauth.vmwservices.com still work but are deprecated; new code should use the workspaceone.com domain shown here",
				}).
				WithDuration(time.Since(start)))
		},
	}
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
			// Privilege heads-up on TTY: when switching to a write-capable
			// profile, remind the user that active-profile state is
			// OS-user-scoped (not shell-scoped) so any process running as
			// them inherits the new privilege. See SECURITY.md.
			if stderrIsTTY() && (args[0] == "operator" || args[0] == "admin") {
				printPrivilegeHeadsUp(args[0])
			}
			emitAndExit(envelope.New("ws1.profile.use").
				WithData(map[string]any{"active": args[0]}).
				WithDuration(time.Since(start)))
		},
	}
}

// printPrivilegeHeadsUp warns the user after switching to a
// write-capable profile. State lives in ~/.config/ws1/profile, which
// is OS-user-scoped: every process running as that user picks up the
// new profile on its next CLI call. See SECURITY.md for v2 mitigations.
func printPrivilegeHeadsUp(name string) {
	fmt.Fprintln(stderrWriter)
	fmt.Fprintf(stderrWriter, "%s  Active profile: %s  %s\n",
		green("✓"), bold(name),
		dim("(write-capable; destructive ops still require browser approval)"))
	fmt.Fprintln(stderrWriter)
	fmt.Fprintln(stderrWriter, dim("  Heads-up: any process running as your OS user (other terminals,"))
	fmt.Fprintln(stderrWriter, dim("  background agents, cron jobs) can now make write calls."))
	fmt.Fprintln(stderrWriter, dim("  Switch back when you're done:  ws1 profile use ro"))
}

func newProfileAddCmd() *cobra.Command {
	var (
		tenant, apiURL, authURL, clientID, clientSecret, region string
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Configure a profile (writes secret to OS keychain)",
		Long: `Configure a profile by name (ro / operator / admin). Stores tenant +
client_id in ~/.config/ws1/profiles.yaml; client_secret goes to the OS
keychain.

Required flags:
  --tenant         tenant hostname (e.g. cn1506.awmdm.com)
  --client-id      OAuth client ID from Groups & Settings > Configurations >
                   OAuth Client Management
  --client-secret  OAuth client secret (stored in keychain)
  --region         ` + regionCodesString() + ` (selects the region-scoped token URL)
                   OR --auth-url to specify it directly

Run ` + "`ws1 profile regions`" + ` for the full data center / customer-geo /
token-URL table.

Note: aw-tenant-code is NOT required for OAuth. The bearer is
sufficient identity at both the gateway and the app layer.`,
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
			if authURL == "" && region == "" {
				emitAndExit(envelope.NewError("ws1.profile.add",
					envelope.CodeIdentifierAmbiguous,
					"either --region ("+regionCodesString()+") or --auth-url must be set; the OAuth token URL is region-scoped, not on the tenant").
					WithDuration(time.Since(start)))
				return
			}
			if authURL == "" {
				resolved, ok := regionToAuthURL(region)
				if !ok {
					emitAndExit(envelope.NewError("ws1.profile.add",
						envelope.CodeIdentifierAmbiguous,
						"unknown --region "+region+"; want one of "+regionCodesString()+", or pass --auth-url directly").
						WithErrorDetails(map[string]any{
							"valid_regions": regionCodes(),
							"hint":          "run `ws1 profile regions` for the full data-center / customer-geo / URL table",
						}).
						WithDuration(time.Since(start)))
					return
				}
				authURL = resolved
			}
			if apiURL == "" {
				apiURL = "https://" + tenant
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
					"api_url":   apiURL,
					"auth_url":  authURL,
					"client_id": clientID,
					"secret":    "stored in OS keychain",
				}).
				WithDuration(time.Since(start)))
		},
	}
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant hostname (e.g. cn1506.awmdm.com)")
	cmd.Flags().StringVar(&apiURL, "api-url", "", "API base URL (default https://<tenant>)")
	cmd.Flags().StringVar(&authURL, "auth-url", "", "OAuth token endpoint (overrides --region)")
	cmd.Flags().StringVar(&region, "region", "", "OAuth region: "+regionCodesString())
	cmd.Flags().StringVar(&clientID, "client-id", "", "OAuth client_id")
	cmd.Flags().StringVar(&clientSecret, "client-secret", "", "OAuth client_secret (stored in OS keychain)")
	return cmd
}
