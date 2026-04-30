package main

import (
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
	"github.com/xyzbuilds/ws1-uem-agent/internal/generated"
	"github.com/xyzbuilds/ws1-uem-agent/internal/policy"
)

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
			emitAndExit(envelope.New("ws1.ops.list").
				WithData(map[string]any{
					"ops":      rows,
					"sections": generated.Sections(),
					"count":    len(rows),
					"filters": map[string]any{
						"section": section,
						"tag":     tag,
						"class":   classWant,
						"filter":  filterWant,
						"summary": summary,
					},
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
