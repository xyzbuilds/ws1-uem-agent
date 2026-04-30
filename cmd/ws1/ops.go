package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
	"github.com/xyzbuilds/ws1-uem-agent/internal/generated"
	"github.com/xyzbuilds/ws1-uem-agent/internal/policy"
)

// opsTableMaxRows caps the TTY-rendered table at a manageable height.
// Rows beyond this trigger a "N more rows. Add --filter ..." footer.
// Pure UX cap — the JSON envelope on stdout still carries every row.
const opsTableMaxRows = 50

// renderOpsTable writes a colored summary table of ops to stderrWriter.
// Used by `ws1 ops list` and `ws1 ops search` whenever stderr is a TTY.
// Agents (no TTY on stderr) get the JSON envelope on stdout unchanged
// — this function never touches stdout.
//
// Layout matches docs/ux-mockups.html frame 5: header line with totals,
// per-class count breakdown, then the table with class color and
// approval/async markers, then a symbol legend.
//
// header — short label for the first line (e.g. "ws1 ops" or
// "ws1 ops search"). Caller decides; this just renders it bold.
// pattern — optional pattern string to mention in the header (search
// uses this; list passes "").
func renderOpsTable(header string, rows []map[string]any, pol *policy.Policy, pattern string, filters map[string]any) {
	if !stderrIsTTY() {
		return
	}
	out := stderrWriter

	// Per-class counts.
	counts := map[string]int{}
	for _, r := range rows {
		c, _ := r["class"].(string)
		counts[c]++
	}

	// Header line.
	fmt.Fprintln(out)
	headerLine := fmt.Sprintf("%s — %d operation(s)", bold(header), len(rows))
	if pattern != "" {
		headerLine += "  " + dim(fmt.Sprintf("matching %q", pattern))
	}
	if filterStr := formatFilterChips(filters); filterStr != "" {
		headerLine += "  " + dim(filterStr)
	}
	fmt.Fprintf(out, "  %s\n", headerLine)

	// Class breakdown (only show present classes).
	if counts["read"]+counts["write"]+counts["destructive"] > 0 {
		fmt.Fprintln(out)
		if n := counts["read"]; n > 0 {
			fmt.Fprintf(out, "    %s          %d\n", green("read"), n)
		}
		if n := counts["write"]; n > 0 {
			fmt.Fprintf(out, "    %s         %d\n", info("write"), n)
		}
		if n := counts["destructive"]; n > 0 {
			fmt.Fprintf(out, "    %s   %d\n", red("destructive"), n)
		}
	}

	// Empty result: skip the table entirely.
	if len(rows) == 0 {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "  %s\n", dim("No matching operations. Loosen filters or check `ws1 ops list --section <slug>`."))
		return
	}

	// Table header.
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %s\n", dim(fmt.Sprintf("%-13s %-3s %s", "CLASS", "", "OP")))

	// Rows.
	cap := opsTableMaxRows
	shown := rows
	if len(rows) > cap {
		shown = rows[:cap]
	}
	for _, r := range shown {
		renderOpsRow(out, r, pol)
	}

	if len(rows) > cap {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "  %s\n", dim(fmt.Sprintf("%d more rows. Add --filter / --section / --tag / --class to narrow.", len(rows)-cap)))
	}

	// Symbol legend — always show so users learn the vocabulary.
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %s  always requires approval    %s  approval at scale    %s  async dispatch\n",
		warn("⚠"), warn("!"), warn("→"))
}

// renderOpsRow writes one row of the ops table, looking up policy
// metadata to decide which approval/async symbols to render.
func renderOpsRow(out io.Writer, r map[string]any, pol *policy.Policy) {
	op, _ := r["op"].(string)
	class, _ := r["class"].(string)
	e := pol.Classify(op)

	// Approval-at-scale marker (write-class with blast-radius threshold,
	// not always-required). Distinct from the always-requires-approval
	// trailing ⚠ on destructive rows.
	marker := " "
	if e.BlastRadiusThreshold != nil && !e.IsDestructive() {
		marker = warn("!")
	}

	classCell := colorByClass(class, padRight(class, 13))

	// Trailing annotations: ⚠ (always requires approval) and → (async).
	annot := ""
	if e.IsDestructive() {
		annot += warn("⚠") + " "
	}
	if e.Sync != nil && !*e.Sync {
		annot += warn("→") + " "
	}

	// Op id with no truncation. Long ids are rare and useful in full;
	// pipe to less if your terminal is narrow.
	fmt.Fprintf(out, "  %s %s  %s   %s\n", classCell, marker, op, strings.TrimSpace(annot))
}

// padRight pads s with spaces on the right to n cols. Used for table
// columns where ANSI color escapes around the padded value would
// confuse a printf width specifier.
func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// formatFilterChips turns the filter map into a compact "(section=X,
// tag=Y, class=Z)" suffix for the table header. Returns "" when no
// filters are set.
func formatFilterChips(filters map[string]any) string {
	if filters == nil {
		return ""
	}
	parts := []string{}
	for _, k := range []string{"section", "tag", "class", "filter"} {
		v, _ := filters[k].(string)
		if v != "" {
			parts = append(parts, fmt.Sprintf("%s=%s", k, v))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// loadActivePolicy resolves the operations.policy.yaml in this precedence:
//  1. WS1_POLICY_FILE (test override)
//  2. operations.policy.yaml in cwd (so a project that ships its own can
//     override the embedded default)
//  3. embedded default from internal/policy
//
// The result is memoised in cachedPolicy because the generic command
// auto-registration calls Classify() once per op (980x); the YAML parse
// is ~25ms so without the cache `ws1 --help` takes 25 seconds.
var cachedPolicy *policy.Policy

func loadActivePolicy() *policy.Policy {
	if cachedPolicy != nil {
		return cachedPolicy
	}
	candidates := []string{}
	if v := envOr("WS1_POLICY_FILE", ""); v != "" {
		candidates = append(candidates, v)
	}
	candidates = append(candidates, "operations.policy.yaml")
	for _, c := range candidates {
		if p, err := policy.Load(c); err == nil {
			cachedPolicy = p
			return cachedPolicy
		}
	}
	cachedPolicy, _ = policy.Load("") // embedded fallback
	return cachedPolicy
}

func newOpsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ops",
		Short: "Inspect the catalog of operations the CLI knows about",
		Long: `Operations are derived from the compiled-in spec at build time. Each op
carries a class (read/write/destructive), reversibility, and approval rules
from operations.policy.yaml. Use 'ops list' for a top-level catalog and
'ops describe <op>' for a single op's full schema.`,
	}
	cmd.AddCommand(newOpsListCmd(), newOpsDescribeCmd(), newOpsSearchCmd())
	return cmd
}

func newOpsListCmd() *cobra.Command {
	var (
		section string
		tag     string
		class   string
		filter  string
		summary bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List operations (filterable by section / tag / class / substring)",
		Long: `List operations from the compiled-in catalog. Without filters this
returns the full set (~980 ops on a typical sync). Use the flags
to narrow:

  --section <slug>    e.g. mdmv1, systemv2, mcmv1, mamv2
  --tag <name>        e.g. devices, users, organizationgroups
  --class <class>     read | write | destructive
  --filter <text>     substring match on the op id (case-insensitive)
  --summary           compact output: only op + class + summary

Examples:
  ws1 ops list --section mdmv1 --tag devices --summary
  ws1 ops list --class destructive --summary
  ws1 ops list --filter wipe --summary
`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			pol := loadActivePolicy()

			ids := make([]string, 0, len(generated.Ops))
			for id := range generated.Ops {
				ids = append(ids, id)
			}
			sort.Strings(ids)

			classWant := strings.ToLower(strings.TrimSpace(class))
			filterWant := strings.ToLower(strings.TrimSpace(filter))

			rows := make([]map[string]any, 0, len(ids))
			for _, id := range ids {
				meta := generated.Ops[id]
				e := pol.Classify(id)
				if section != "" && meta.Section != section {
					continue
				}
				if tag != "" && !strings.EqualFold(meta.Tag, tag) {
					continue
				}
				if classWant != "" && string(e.Class) != classWant {
					continue
				}
				if filterWant != "" && !strings.Contains(strings.ToLower(id), filterWant) {
					continue
				}
				if summary {
					rows = append(rows, map[string]any{
						"op":      id,
						"class":   string(e.Class),
						"summary": meta.Summary,
					})
					continue
				}
				rows = append(rows, map[string]any{
					"op":           id,
					"section":      meta.Section,
					"tag":          meta.Tag,
					"verb":         meta.Verb,
					"http_method":  meta.HTTPMethod,
					"path":         meta.PathTemplate,
					"summary":      meta.Summary,
					"class":        string(e.Class),
					"reversible":   string(e.Reversible),
					"identifier":   string(e.Identifier),
					"unclassified": e.Synthetic,
				})
			}
			filters := map[string]any{
				"section": section,
				"tag":     tag,
				"class":   classWant,
				"filter":  filterWant,
				"summary": summary,
			}
			renderOpsTable("ws1 ops", rows, pol, "", filters)
			emitAndExit(envelope.New("ws1.ops.list").
				WithData(map[string]any{
					"ops":      rows,
					"sections": generated.Sections(),
					"count":    len(rows),
					"filters":  filters,
				}).
				WithDuration(time.Since(start)))
		},
	}
	cmd.Flags().StringVar(&section, "section", "", "filter by section slug (e.g. mdmv1, systemv2)")
	cmd.Flags().StringVar(&tag, "tag", "", "filter by tag (e.g. devices, users)")
	cmd.Flags().StringVar(&class, "class", "", "filter by class (read | write | destructive)")
	cmd.Flags().StringVar(&filter, "filter", "", "substring match on op id (case-insensitive)")
	cmd.Flags().BoolVar(&summary, "summary", false, "compact output: only op + class + summary")
	return cmd
}

// newOpsSearchCmd is sugar for `ops list --filter <pattern> --summary`.
// Most users reach for this when they remember a verb or a fragment
// of a path and want to find the matching op.
func newOpsSearchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search <pattern>",
		Short: "Search operations by substring (sugar for `list --filter <pattern> --summary`)",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			pol := loadActivePolicy()
			needle := strings.ToLower(args[0])

			ids := make([]string, 0, len(generated.Ops))
			for id := range generated.Ops {
				ids = append(ids, id)
			}
			sort.Strings(ids)

			rows := make([]map[string]any, 0)
			for _, id := range ids {
				if !strings.Contains(strings.ToLower(id), needle) &&
					!strings.Contains(strings.ToLower(generated.Ops[id].Summary), needle) {
					continue
				}
				meta := generated.Ops[id]
				e := pol.Classify(id)
				rows = append(rows, map[string]any{
					"op":      id,
					"class":   string(e.Class),
					"summary": meta.Summary,
				})
			}
			renderOpsTable("ws1 ops search", rows, pol, args[0], nil)
			emitAndExit(envelope.New("ws1.ops.search").
				WithData(map[string]any{
					"matches": rows,
					"count":   len(rows),
					"pattern": args[0],
				}).
				WithDuration(time.Since(start)))
		},
	}
}

func newOpsDescribeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "describe <op>",
		Short: "Print full metadata for a single operation",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			id := args[0]
			meta, ok := generated.Ops[id]
			if !ok {
				emitAndExit(envelope.NewError("ws1.ops.describe",
					envelope.CodeIdentifierNotFound,
					"no operation matches "+id).
					WithErrorDetails(map[string]any{
						"requested": id,
						"hint":      "run `ws1 ops list` to see available ops",
					}).
					WithDuration(time.Since(start)))
				return
			}
			pol := loadActivePolicy()
			e := pol.Classify(id)

			params := make([]map[string]any, 0, len(meta.Parameters))
			for _, p := range meta.Parameters {
				params = append(params, map[string]any{
					"name":        p.Name,
					"in":          p.In,
					"required":    p.Required,
					"type":        p.Type,
					"description": p.Description,
				})
			}
			emitAndExit(envelope.New("ws1.ops.describe").
				WithData(map[string]any{
					"op":           meta.Op,
					"section":      meta.Section,
					"tag":          meta.Tag,
					"verb":         meta.Verb,
					"http_method":  meta.HTTPMethod,
					"path":         meta.PathTemplate,
					"summary":      meta.Summary,
					"description":  meta.Description,
					"parameters":   params,
					"has_body":     meta.HasRequestBody,
					"class":        string(e.Class),
					"reversible":   string(e.Reversible),
					"approval":     string(e.Approval),
					"identifier":   string(e.Identifier),
					"unclassified": e.Synthetic,
					"warn":         e.Warn,
				}).
				WithDuration(time.Since(start)))
		},
	}
}

// envOr is a tiny helper to keep getenv chains readable.
func envOr(key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}
