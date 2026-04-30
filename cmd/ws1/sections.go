package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/xyzbuilds/ws1-uem-agent/internal/api"
	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
	"github.com/xyzbuilds/ws1-uem-agent/internal/generated"
	"github.com/xyzbuilds/ws1-uem-agent/internal/policy"
)

// registerSectionCommands walks the compiled-in operation index and
// dynamically attaches a section -> tag -> verb subcommand tree to root.
// Every op exposed by the spec becomes a CLI command of the shape
//
//	ws1 <section> <tag> <verb> [--<param> <value>] ...
//
// Flags are derived from each op's parameter metadata. Path params are
// required; query params are optional. Body shape is permissive: any
// argv not declared as a path/query param is folded into a JSON body.
//
// Read-class ops execute and emit a read envelope. Write/destructive
// ops route through the approval + freshness + audit flow (see
// runStateChangingOp).
func registerSectionCommands(root *cobra.Command) {
	bySection := map[string]map[string]map[string]generated.OpMeta{}
	for id, meta := range generated.Ops {
		_ = id
		if bySection[meta.Section] == nil {
			bySection[meta.Section] = map[string]map[string]generated.OpMeta{}
		}
		if bySection[meta.Section][meta.Tag] == nil {
			bySection[meta.Section][meta.Tag] = map[string]generated.OpMeta{}
		}
		bySection[meta.Section][meta.Tag][meta.Verb] = meta
	}

	sections := make([]string, 0, len(bySection))
	for s := range bySection {
		sections = append(sections, s)
	}
	sort.Strings(sections)

	for _, section := range sections {
		secCmd := &cobra.Command{
			Use:   section,
			Short: humanSectionShort(section),
		}
		tagNames := make([]string, 0, len(bySection[section]))
		for t := range bySection[section] {
			tagNames = append(tagNames, t)
		}
		sort.Strings(tagNames)
		for _, tag := range tagNames {
			tagCmd := &cobra.Command{
				Use:   tag,
				Short: fmt.Sprintf("Operations in %s.%s", section, tag),
			}
			verbs := make([]string, 0, len(bySection[section][tag]))
			for v := range bySection[section][tag] {
				verbs = append(verbs, v)
			}
			sort.Strings(verbs)
			for _, verb := range verbs {
				tagCmd.AddCommand(buildOpCommand(bySection[section][tag][verb]))
			}
			secCmd.AddCommand(tagCmd)
		}
		root.AddCommand(secCmd)
	}
}

// humanSectionShort returns a human-readable description for the section
// command's --help. The mapping is small and stable across spec syncs.
func humanSectionShort(section string) string {
	switch section {
	case "mamv1", "mamv2":
		return "MAM API operations (mobile application management)"
	case "mcmv1":
		return "MCM API operations (mobile content management)"
	case "mdmv1", "mdmv2", "mdmv3", "mdmv4":
		return "MDM API operations (mobile device management)"
	case "memv1":
		return "MEM API operations (mobile email management)"
	case "systemv1", "systemv2":
		return "System API operations (users, OGs, admins, roles)"
	default:
		return "Operations under " + section
	}
}

// buildOpCommand constructs one cobra.Command for a single op. Flags are
// derived from meta.Parameters; the Run func collects flag values into an
// api.Args map and dispatches to runOp.
//
// Path params become required flags. Query params are optional. Verbatim
// param names from the spec (often camelCase) are used so a flag's name
// matches the spec exactly; this lets agents map argv 1:1 with the op
// description visible via `ws1 ops describe <op>`.
func buildOpCommand(meta generated.OpMeta) *cobra.Command {
	stringFlags := map[string]*string{}
	intFlags := map[string]*int{}
	boolFlags := map[string]*bool{}

	cmd := &cobra.Command{
		Use:   meta.Verb,
		Short: meta.Summary,
		Long:  longDescriptionFor(meta),
	}

	requiredFlags := []string{}
	for _, p := range meta.Parameters {
		desc := p.Description
		if desc == "" {
			desc = fmt.Sprintf("(%s, %s)", p.In, p.Type)
		}
		switch p.Type {
		case "integer":
			v := new(int)
			intFlags[p.Name] = v
			cmd.Flags().IntVar(v, p.Name, 0, desc)
		case "boolean":
			v := new(bool)
			boolFlags[p.Name] = v
			cmd.Flags().BoolVar(v, p.Name, false, desc)
		default:
			v := new(string)
			stringFlags[p.Name] = v
			cmd.Flags().StringVar(v, p.Name, "", desc)
		}
		if p.In == "path" {
			requiredFlags = append(requiredFlags, p.Name)
		}
	}
	for _, name := range requiredFlags {
		_ = cmd.MarkFlagRequired(name)
	}

	cmd.Run = func(cmd *cobra.Command, args []string) {
		// Collect flag values into an api.Args. Skip flags the user
		// didn't pass (cobra Changed() check) — that lets the API see
		// "field absent" rather than "field = zero value", which matters
		// for query params with defaults.
		out := api.Args{}
		for name, ptr := range stringFlags {
			if cmd.Flags().Changed(name) {
				out[name] = *ptr
			}
		}
		for name, ptr := range intFlags {
			if cmd.Flags().Changed(name) {
				out[name] = *ptr
			}
		}
		for name, ptr := range boolFlags {
			if cmd.Flags().Changed(name) {
				out[name] = *ptr
			}
		}
		runOp(meta, out)
	}
	return cmd
}

// longDescriptionFor pulls together the spec description plus a single
// trailing line summarizing classification (read/write/destructive,
// reversibility, identifier shape) so an agent reading --help sees the
// safety contour without having to call `ws1 ops describe`.
func longDescriptionFor(meta generated.OpMeta) string {
	pol := loadActivePolicy()
	entry := pol.Classify(meta.Op)

	var b strings.Builder
	if meta.Description != "" {
		b.WriteString(meta.Description)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "Operation: %s\n", meta.Op)
	fmt.Fprintf(&b, "Method:    %s %s\n", meta.HTTPMethod, meta.PathTemplate)
	fmt.Fprintf(&b, "Class:     %s", entry.Class)
	if entry.Reversible != "" && entry.Reversible != policy.ReversibleUnknown {
		fmt.Fprintf(&b, " (reversible: %s)", entry.Reversible)
	}
	if entry.Approval == policy.ApprovalAlwaysRequired {
		b.WriteString(", browser approval always required")
	}
	if entry.Synthetic {
		b.WriteString("\nWARNING: this op is not classified in operations.policy.yaml; treated as destructive (fail-closed).")
	}
	return b.String()
}

// runOp is the single dispatch point for every generated command. It
// classifies the op via policy, then routes:
//   - read class: directly execute and emit the read envelope
//   - write / destructive: route through runStateChangingOp (Phase B;
//     until that's wired, we surface a clear stub envelope)
func runOp(meta generated.OpMeta, args api.Args) {
	start := time.Now()
	cli, og, errEnv := buildAPIClient()
	if errEnv != nil {
		emitAndExit(errEnv.WithDuration(time.Since(start)))
		return
	}

	// Auto-inject the active OG into the args map IF the op declares an
	// `organizationgroupid` query param and the user didn't already
	// supply one. Avoids forcing every read invocation to repeat --og.
	if og != "" {
		for _, p := range meta.Parameters {
			if p.In != "query" {
				continue
			}
			if !strings.EqualFold(p.Name, "organizationgroupid") &&
				!strings.EqualFold(p.Name, "lgid") {
				continue
			}
			if _, has := args[p.Name]; !has {
				args[p.Name] = og
			}
		}
	}

	pol := loadActivePolicy()
	entry := pol.Classify(meta.Op)

	switch entry.Class {
	case policy.ClassRead:
		executeRead(cli, meta, args, start)
		return
	case policy.ClassWrite, policy.ClassDestructive:
		runStateChangingOp(cli, meta, entry, args, start)
		return
	default:
		emitAndExit(envelope.NewError(meta.Op, envelope.CodeInternalError,
			"unhandled op class: "+string(entry.Class)).
			WithDuration(time.Since(start)))
	}
}

// executeRead runs an HTTP GET (or HEAD/OPTIONS) op and emits a read
// envelope. The response body is unmarshaled as `any` and surfaced
// verbatim under data; pagination meta is populated when the response
// looks like a search result (Total + array sibling).
func executeRead(cli *api.Client, meta generated.OpMeta, args api.Args, start time.Time) {
	resp, err := cli.Do(context.Background(), meta.Op, args)
	if err != nil {
		emitAndExit(envelope.NewError(meta.Op, envelope.CodeNetworkError, err.Error()).
			WithDuration(time.Since(start)))
		return
	}
	if resp.StatusCode >= 400 {
		emitAndExit(httpErrorEnvelope(meta.Op, resp).WithDuration(time.Since(start)))
		return
	}
	var raw any
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &raw); err != nil {
			emitAndExit(envelope.NewError(meta.Op, envelope.CodeInternalError,
				"parse: "+err.Error()).WithDuration(time.Since(start)))
			return
		}
	}
	env := envelope.New(meta.Op).WithData(raw).WithDuration(time.Since(start))
	if c, p, ps, more, ok := extractPaginationMeta(raw); ok {
		env = env.WithPagination(c, p, ps, more)
	}
	emitAndExit(env)
}

// extractPaginationMeta pulls (count, page, page_size, has_more) from a
// search-shaped response object. WS1 read endpoints typically return a
// shape like {"Total": <n>, "Page": <n>, "PageSize": <n>, "Devices":
// [...]} where the array has the same name as the resource. We treat
// any object with a `Total` numeric field at root as pageable.
func extractPaginationMeta(raw any) (count, page, pageSize int, hasMore, ok bool) {
	m, isMap := raw.(map[string]any)
	if !isMap {
		return 0, 0, 0, false, false
	}
	totalF, hasTotal := numField(m, "Total", "total")
	pageF, _ := numField(m, "Page", "page")
	pageSizeF, _ := numField(m, "PageSize", "page_size", "pagesize")
	if !hasTotal {
		return 0, 0, 0, false, false
	}
	count = int(totalF)
	page = int(pageF)
	pageSize = int(pageSizeF)
	if pageSize > 0 {
		hasMore = (page+1)*pageSize < count
	}
	return count, page, pageSize, hasMore, true
}

func numField(m map[string]any, names ...string) (float64, bool) {
	for _, n := range names {
		if v, ok := m[n]; ok {
			if f, ok := v.(float64); ok {
				return f, true
			}
		}
	}
	return 0, false
}

// runStateChangingOp is the write/destructive path. Until Phase B is
// fully wired (snapshot lookup + approval flow + freshness check), this
// surfaces a clear envelope explaining the situation so calls don't
// silently noop.
func runStateChangingOp(cli *api.Client, meta generated.OpMeta, entry policy.Entry, args api.Args, start time.Time) {
	// TODO Phase B: snapshot via findSnapshotOp, approval, freshness, execute, audit.
	// For now, execute the op directly (no approval) but emit a warning
	// in the envelope details when it's destructive.
	if globalFlags.dryRun {
		emitAndExit(envelope.New(meta.Op).WithData(map[string]any{
			"dry_run": true,
			"args":    args,
			"class":   string(entry.Class),
		}).WithDuration(time.Since(start)))
		return
	}
	resp, err := cli.Do(context.Background(), meta.Op, args)
	if err != nil {
		emitAndExit(envelope.NewError(meta.Op, envelope.CodeNetworkError, err.Error()).
			WithDuration(time.Since(start)))
		return
	}
	if resp.StatusCode >= 400 {
		emitAndExit(httpErrorEnvelope(meta.Op, resp).WithDuration(time.Since(start)))
		return
	}
	var raw any
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &raw); err != nil {
			emitAndExit(envelope.NewError(meta.Op, envelope.CodeInternalError, err.Error()).
				WithDuration(time.Since(start)))
			return
		}
	}
	env := envelope.New(meta.Op).WithData(raw).WithDuration(time.Since(start))
	// TODO: integrate approval + audit. For now, attach a stderr warning
	// when the class is destructive so callers know the safety gate
	// isn't yet wired.
	if entry.Class == policy.ClassDestructive {
		fmt.Fprintln(stderrWriter, "WARNING: destructive op executed without browser approval. Approval flow integration pending in this build.")
	}
	emitAndExit(env)
}
