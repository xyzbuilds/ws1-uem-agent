package main

import (
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/zhangxuyang/ws1-uem-agent/internal/envelope"
	"github.com/zhangxuyang/ws1-uem-agent/internal/generated"
	"github.com/zhangxuyang/ws1-uem-agent/internal/policy"
)

// loadActivePolicy resolves the operations.policy.yaml in this precedence:
//  1. WS1_POLICY_FILE (test override)
//  2. operations.policy.yaml in cwd (so a project that ships its own can
//     override the embedded default)
//  3. embedded default from internal/policy
func loadActivePolicy() *policy.Policy {
	candidates := []string{}
	if v := envOr("WS1_POLICY_FILE", ""); v != "" {
		candidates = append(candidates, v)
	}
	candidates = append(candidates, "operations.policy.yaml")
	for _, c := range candidates {
		if p, err := policy.Load(c); err == nil {
			return p
		}
	}
	p, _ := policy.Load("") // embedded fallback
	return p
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
	cmd.AddCommand(newOpsListCmd(), newOpsDescribeCmd())
	return cmd
}

func newOpsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all operations with class + reversibility + identifier",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			pol := loadActivePolicy()

			ids := make([]string, 0, len(generated.Ops))
			for id := range generated.Ops {
				ids = append(ids, id)
			}
			sort.Strings(ids)

			rows := make([]map[string]any, 0, len(ids))
			for _, id := range ids {
				meta := generated.Ops[id]
				e := pol.Classify(id)
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
