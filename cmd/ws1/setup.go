package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/xyzbuilds/ws1-uem-agent/internal/api"
	"github.com/xyzbuilds/ws1-uem-agent/internal/audit"
	"github.com/xyzbuilds/ws1-uem-agent/internal/auth"
	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
	"github.com/xyzbuilds/ws1-uem-agent/internal/generated"
)

// errAuthExhausted signals the OAuth retry loop ran out of attempts.
// The cobra wrapper maps it to CodeAuthInsufficientForOp.
var errAuthExhausted = errors.New("auth: max retries exhausted")

// errNonInteractiveMissingFlag signals a flag is required because the
// caller is non-interactive and the wizard cannot prompt. Maps to
// CodeIdentifierAmbiguous in the cobra wrapper.
var errNonInteractiveMissingFlag = errors.New("non-interactive: missing required flag")

// SetupOptions holds the values that may come from flags. The wizard
// fills in any unset string from prompts (in interactive mode) or
// errors out (in non-interactive mode).
type SetupOptions struct {
	Profile      string // ro / operator / admin (default operator)
	Tenant       string
	Region       string // resolves AuthURL via regionToAuthURL
	AuthURL      string // overrides Region if set
	ClientID     string
	ClientSecret string
	OG           string

	Quick        bool // skip multi-profile picker (default true; --advanced flips it)
	SkipValidate bool // skip OAuth round-trip
	SkipSmoke    bool // skip the final smoke test
}

func newSetupCmd() *cobra.Command {
	var opts SetupOptions
	var advanced bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Connect ws1 to your Workspace ONE UEM tenant (interactive wizard)",
		Long: `ws1 setup walks through the first-run configuration: tenant URL,
region, OAuth credentials, and a default Org Group context. Re-running
setup detects existing config and offers each value as a default.

Quick mode (the default) configures one profile (operator). Use
--advanced to pick which profiles to configure (ro / operator / admin).

Non-interactive mode: supply --tenant + --region + --client-id +
--client-secret + --og to bootstrap from CI without prompts. Setting
the active profile from CI is intentionally refused; run
'ws1 profile use <name>' from a terminal afterward.

The OAuth client you provision in the WS1 console MUST have a role
matching the chosen profile. The CLI's class gate is belt-and-braces;
the OAuth role is the API-side enforcer.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			opts.Quick = !advanced
			prompter := Prompter(NewTTYPrompter())
			if !auth.IsInteractive() {
				// Non-interactive: every required flag must be set;
				// otherwise IDENTIFIER_AMBIGUOUS.
				if missing := missingRequiredFlags(opts); len(missing) > 0 {
					emitAndExit(envelope.NewError("ws1.setup",
						envelope.CodeIdentifierAmbiguous,
						"non-interactive setup requires all configuration via flags").
						WithErrorDetails(map[string]any{"missing": missing}).
						WithDuration(time.Since(start)))
					return
				}
			}
			if err := RunSetup(context.Background(), opts, prompter); err != nil {
				code := envelope.CodeInternalError
				switch {
				case errors.Is(err, errAuthExhausted):
					code = envelope.CodeAuthInsufficientForOp
				case errors.Is(err, errNonInteractiveMissingFlag):
					code = envelope.CodeIdentifierAmbiguous
				}
				emitAndExit(envelope.NewError("ws1.setup", code, err.Error()).
					WithDuration(time.Since(start)))
				return
			}
			emitAndExit(envelope.New("ws1.setup").
				WithData(map[string]any{"complete": true}).
				WithDuration(time.Since(start)))
		},
	}
	cmd.Flags().BoolVar(&advanced, "advanced", false, "configure multiple profiles (ro/operator/admin)")
	cmd.Flags().StringVar(&opts.Profile, "profile", "operator", "profile name (ro|operator|admin)")
	cmd.Flags().StringVar(&opts.Tenant, "tenant", "", "tenant hostname")
	cmd.Flags().StringVar(&opts.Region, "region", "", "OAuth region: "+regionCodesString())
	cmd.Flags().StringVar(&opts.AuthURL, "auth-url", "", "OAuth token endpoint (overrides --region)")
	cmd.Flags().StringVar(&opts.ClientID, "client-id", "", "OAuth client_id")
	cmd.Flags().StringVar(&opts.ClientSecret, "client-secret", "", "OAuth client_secret")
	cmd.Flags().StringVar(&opts.OG, "og", "", "default OG ID")
	cmd.Flags().BoolVar(&opts.SkipValidate, "skip-validate", false, "skip OAuth round-trip")
	cmd.Flags().BoolVar(&opts.SkipSmoke, "skip-smoke-test", false, "skip the final smoke test")
	return cmd
}

func missingRequiredFlags(o SetupOptions) []string {
	var miss []string
	if o.Tenant == "" {
		miss = append(miss, "--tenant")
	}
	if o.Region == "" && o.AuthURL == "" {
		miss = append(miss, "--region (or --auth-url)")
	}
	if o.ClientID == "" {
		miss = append(miss, "--client-id")
	}
	if o.ClientSecret == "" {
		miss = append(miss, "--client-secret")
	}
	if o.OG == "" {
		miss = append(miss, "--og")
	}
	return miss
}

// RunSetup orchestrates the wizard. Pure function of options +
// prompter; emits to stderr via the prompter's Spinner / TTYPrompter.
// Returns nil on success, error if any step fatally fails.
func RunSetup(ctx context.Context, opts SetupOptions, p Prompter) error {
	// Pre-fill from existing config (if any) so re-running setup
	// becomes the reconfigure path. Existing values take precedence
	// only when opts is empty for that field.
	preFillFromExisting(&opts)

	// Step 1: Tenant. Prompt with existing value as default; in
	// non-interactive mode keep whatever was passed via flag (the
	// cobra Run handler already validated the flag set).
	if opts.Tenant != "" && !auth.IsInteractive() {
		// Non-interactive: keep the value as-is, no prompt.
	} else {
		tenant, err := p.Ask("Tenant hostname", opts.Tenant)
		if err != nil {
			return err
		}
		opts.Tenant = tenant
	}

	// Step 2: Region (only prompt interactively; non-interactive must
	// supply --region or --auth-url).
	if opts.AuthURL == "" {
		if opts.Region == "" {
			if !auth.IsInteractive() {
				return fmt.Errorf("%w: --region or --auth-url", errNonInteractiveMissingFlag)
			}
			region, err := pickRegion(p)
			if err != nil {
				return err
			}
			opts.Region = region
		}
		url, ok := regionToAuthURL(opts.Region)
		if !ok {
			return fmt.Errorf("unknown region %q", opts.Region)
		}
		opts.AuthURL = url
	}

	// Step 3: Profiles to configure.
	profileNames, err := selectProfilesToConfigure(p, opts)
	if err != nil {
		return err
	}

	// Step 4: For each profile, prompt for credentials and validate.
	configured := []auth.Profile{}
	for _, name := range profileNames {
		prof, err := configureOneProfile(ctx, p, opts, name)
		if err != nil {
			return err
		}
		configured = append(configured, prof)
	}

	// Step 5: OG selection. Use the most-privileged configured profile
	// for the OG fetch (operator > admin > ro), per spec §4.2.
	pickerProf := selectOGFetchProfile(configured)
	og, err := pickOG(ctx, p, &pickerProf, opts.OG)
	if err != nil {
		return err
	}
	if err := auth.SetOG(og); err != nil {
		return fmt.Errorf("save OG: %w", err)
	}

	// Step 6: Active profile. Prefer ro for safety; else operator;
	// else first configured (matches spec §4.2 + SKILL.md principle).
	if auth.IsInteractive() {
		active := selectActiveProfile(profileNames)
		if err := auth.SwitchActive(active, true); err != nil {
			return fmt.Errorf("set active: %w", err)
		}
	}

	// Step 7: Smoke test using the most-privileged profile.
	if !opts.SkipSmoke {
		runSmokeTest(ctx, p, &pickerProf)
	}

	printExitSummary(profileNames, configured, og)
	writeSetupAudit(profileNames, configured, og)
	return nil
}

// selectProfilesToConfigure returns the list of profile names to
// configure. Quick mode returns just opts.Profile (default operator).
// Advanced mode prompts for a comma-separated list.
func selectProfilesToConfigure(p Prompter, opts SetupOptions) ([]string, error) {
	if opts.Quick {
		name := opts.Profile
		if name == "" {
			name = "operator"
		}
		return []string{name}, nil
	}
	answer, err := p.Ask("Profiles to configure", "operator")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(answer, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if !auth.IsValidProfile(name) {
			return nil, fmt.Errorf("unknown profile %q (want one of ro/operator/admin)", name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		out = []string{"operator"}
	}
	return out, nil
}

// configureOneProfile prompts for one profile's credentials, validates
// them, persists them, and returns the resulting Profile. On
// validation failure, retries up to 3 times.
func configureOneProfile(ctx context.Context, p Prompter, opts SetupOptions, name string) (auth.Profile, error) {
	clientIDLabel := "Client ID"
	clientSecretLabel := "Client Secret"
	if !opts.Quick {
		clientIDLabel = "Client ID for " + name
		clientSecretLabel = "Client Secret for " + name
	}

	clientID := opts.ClientID
	clientSecret := opts.ClientSecret
	if !opts.Quick {
		// Advanced mode always prompts per-profile; do not reuse opts creds.
		clientID = ""
		clientSecret = ""
	}
	if clientID == "" {
		var err error
		clientID, err = p.Ask(clientIDLabel, "")
		if err != nil {
			return auth.Profile{}, err
		}
	}
	if clientSecret == "" {
		var err error
		clientSecret, err = p.AskSecret(clientSecretLabel)
		if err != nil {
			return auth.Profile{}, err
		}
	}

	prof := auth.Profile{
		Name: name, Tenant: opts.Tenant,
		APIURL: "https://" + opts.Tenant, AuthURL: opts.AuthURL,
		ClientID: clientID,
	}
	if err := auth.SaveProfile(prof); err != nil {
		return auth.Profile{}, err
	}
	if err := auth.SaveClientSecret(name, clientID, clientSecret); err != nil {
		return auth.Profile{}, err
	}

	if opts.SkipValidate {
		return prof, nil
	}
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		spin := p.Spinner(fmt.Sprintf("Validating %s against %s...", name, opts.AuthURL))
		client := api.New(auth.NewOAuthClient(&prof))
		_, err := client.Source.Token(ctx)
		if err == nil {
			spin.Done(true, "Token issued for "+name)
			return prof, nil
		}
		lastErr = err
		spin.Done(false, fmt.Sprintf("Auth failed (attempt %d/%d): %v", attempt, maxAttempts, err))
		if attempt == maxAttempts {
			break
		}
		newID, perr := p.Ask(clientIDLabel+" (retry)", clientID)
		if perr != nil {
			return auth.Profile{}, perr
		}
		newSec, perr := p.AskSecret(clientSecretLabel + " (retry)")
		if perr != nil {
			return auth.Profile{}, perr
		}
		clientID = newID
		clientSecret = newSec
		prof.ClientID = clientID
		if err := auth.SaveProfile(prof); err != nil {
			return auth.Profile{}, fmt.Errorf("save profile on retry: %w", err)
		}
		if err := auth.SaveClientSecret(name, clientID, clientSecret); err != nil {
			return auth.Profile{}, fmt.Errorf("save secret on retry: %w", err)
		}
	}
	return auth.Profile{}, fmt.Errorf("%w (profile %s, %d attempts): %w", errAuthExhausted, name, maxAttempts, lastErr)
}

// selectOGFetchProfile picks operator > admin > ro from the configured
// list. Falls back to the first profile if none of those names is
// present (shouldn't happen — selectProfilesToConfigure validates).
func selectOGFetchProfile(profiles []auth.Profile) auth.Profile {
	for _, want := range []string{"operator", "admin", "ro"} {
		for _, p := range profiles {
			if p.Name == want {
				return p
			}
		}
	}
	return profiles[0]
}

// selectActiveProfile prefers ro (safer default; matches SKILL.md
// principle stack) then operator, else falls back to the first
// configured.
func selectActiveProfile(names []string) string {
	for _, want := range []string{"ro", "operator"} {
		for _, n := range names {
			if n == want {
				return n
			}
		}
	}
	return names[0]
}

// printExitSummary writes the final "Setup complete" block to stderr.
// Stays out of stdout so emitAndExit's envelope remains the only line
// on stdout for downstream parsers.
func printExitSummary(profileNames []string, configured []auth.Profile, og string) {
	if len(configured) == 0 {
		return
	}
	tenant := configured[0].Tenant
	active, _ := auth.Active()
	fmt.Fprintln(stderrWriter, summarySeparator())
	fmt.Fprintln(stderrWriter, "Setup complete.")
	fmt.Fprintln(stderrWriter)
	fmt.Fprintf(stderrWriter, "  Profiles configured: %s\n", strings.Join(profileNames, ", "))
	fmt.Fprintf(stderrWriter, "  Active profile:      %s\n", active)
	fmt.Fprintf(stderrWriter, "  Tenant:              %s\n", tenant)
	if og != "" {
		fmt.Fprintf(stderrWriter, "  OG context:          %s\n", og)
	}
	fmt.Fprintln(stderrWriter)
	fmt.Fprintln(stderrWriter, "Try:")
	fmt.Fprintln(stderrWriter, "  ws1 doctor")
	fmt.Fprintln(stderrWriter, "  ws1 ops list | jq '.data.count'")
	fmt.Fprintln(stderrWriter, "  ws1 mdmv1 devices search --pagesize 5")
	fmt.Fprintln(stderrWriter, summarySeparator())
}

// summarySeparator returns the horizontal-rule line for the exit
// summary block. Uses a Unicode rule when the locale advertises
// UTF-8 (matches the spinner's glyph choice); falls back to ASCII
// hyphens otherwise.
func summarySeparator() string {
	if isUTF8Locale() {
		return strings.Repeat("─", 50)
	}
	return strings.Repeat("-", 50)
}

// writeSetupAudit appends a single ws1.setup row to the audit log.
// Best-effort: a failure here doesn't fail the wizard (the setup is
// already done; the user shouldn't see a misleading error).
func writeSetupAudit(profileNames []string, configured []auth.Profile, og string) {
	path, err := audit.DefaultPath()
	if err != nil {
		return
	}
	logger, err := audit.New(path)
	if err != nil {
		return
	}
	tenant := ""
	if len(configured) > 0 {
		tenant = configured[0].Tenant
	}
	hashSeed := strings.Join(profileNames, ",") + "|" + og + "|" + tenant
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(hashSeed)))
	_, _ = logger.Append(audit.Entry{
		Caller:    "ws1.setup",
		Operation: "ws1.setup",
		ArgsHash:  hash[:16],
		Class:     "configuration",
		Profile:   strings.Join(profileNames, ","),
		Tenant:    tenant,
		Result:    "ok",
	})
}

func pickRegion(p Prompter) (string, error) {
	options := []PickItem{}
	for _, r := range Regions {
		options = append(options, PickItem{Label: r.Code, Hint: r.DataCenter, Value: r.Code})
	}
	pick, err := p.Pick("Region", options)
	if err != nil {
		return "", err
	}
	return pick.Value, nil
}

// pickOG fetches the OG list from the tenant and lets the user pick
// from a numbered menu. If the OG-list call fails (network or 4xx),
// falls back to a freeform "OG ID:" prompt.
func pickOG(ctx context.Context, p Prompter, prof *auth.Profile, prefilled string) (string, error) {
	if prefilled != "" {
		return prefilled, nil
	}
	spin := p.Spinner("Fetching organization groups...")
	ogs, err := fetchOGList(ctx, prof)
	if err != nil || len(ogs) == 0 {
		spin.Done(false, "Could not list OGs; enter ID manually")
		return p.Ask("OG ID", "")
	}
	spin.Done(true, fmt.Sprintf("Found %d OGs", len(ogs)))
	options := make([]PickItem, 0, len(ogs))
	for _, og := range ogs {
		options = append(options, PickItem{
			Label: og.Name,
			Hint:  fmt.Sprintf("(id %d)", og.ID),
			Value: strconv.Itoa(og.ID),
		})
	}
	pick, err := p.Pick("Organization group", options)
	if err != nil {
		return "", err
	}
	return pick.Value, nil
}

// ogRow is the parsed row of the OG-list response.
type ogRow struct {
	ID   int    `json:"Id"`
	UUID string `json:"Uuid"`
	Name string `json:"Name"`
}

// fetchOGList calls the v2 (or v1 fallback) org-group search op.
// Sub-decision pinned: prefer systemv2.organizationgroups.organizationgroupsearch;
// fall back to systemv1.organizationgroups.locationgroupsearch.
func fetchOGList(ctx context.Context, prof *auth.Profile) ([]ogRow, error) {
	client := api.New(auth.NewOAuthClient(prof))
	for _, op := range []string{
		"systemv2.organizationgroups.organizationgroupsearch",
		"systemv1.organizationgroups.locationgroupsearch",
	} {
		if _, ok := generated.Ops[op]; !ok {
			continue
		}
		resp, err := client.Do(ctx, op, api.Args{})
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == 403 {
			return nil, fmt.Errorf("403 forbidden")
		}
		if resp.StatusCode >= 400 {
			continue
		}
		var body struct {
			LocationGroups     []ogRow `json:"LocationGroups"`     // v1 key
			OrganizationGroups []ogRow `json:"OrganizationGroups"` // v2 key
		}
		if err := resp.JSON(&body); err != nil {
			return nil, err
		}
		if len(body.OrganizationGroups) > 0 {
			return body.OrganizationGroups, nil
		}
		return body.LocationGroups, nil
	}
	return nil, errors.New("no OG-search op found in compiled index")
}

// preFillFromExisting reads the existing profile (if any) named
// opts.Profile (default "operator") and copies its tenant + auth_url
// + client_id into opts when those fields are empty. Acts as the
// reconfigure-friendly default-seeding step.
func preFillFromExisting(opts *SetupOptions) {
	name := opts.Profile
	if name == "" {
		name = "operator"
	}
	prof, err := auth.FindProfile(name)
	if err != nil || prof == nil {
		return
	}
	if opts.Tenant == "" {
		opts.Tenant = prof.Tenant
	}
	if opts.AuthURL == "" {
		opts.AuthURL = prof.AuthURL
	}
	if opts.ClientID == "" {
		opts.ClientID = prof.ClientID
	}
}

// runSmokeTest emits a spinner + final result. Failures are
// informational; setup is still considered successful.
func runSmokeTest(ctx context.Context, p Prompter, prof *auth.Profile) {
	spin := p.Spinner("Smoke test: ws1 mdmv1 devices search --pagesize 1")
	client := api.New(auth.NewOAuthClient(prof))
	resp, err := client.Do(ctx, "mdmv1.devices.search", api.Args{"pagesize": 1})
	if err != nil {
		spin.Done(false, "smoke test error: "+err.Error())
		return
	}
	if resp.StatusCode >= 400 {
		spin.Done(false, fmt.Sprintf("smoke test API %d", resp.StatusCode))
		return
	}
	spin.Done(true, "Received response")
}
