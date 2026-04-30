package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

// classifyCheck is the build-time gate that ensures every operation in
// internal/generated/ops_index.json is classified in operations.policy.yaml.
// New ops must be hand-classified before the build can succeed; this is the
// non-negotiable safety gate per spec section 8.2.
func newClassifyCheckCmd() *cobra.Command {
	var indexPath, policyPath string
	cmd := &cobra.Command{
		Use:   "classify-check",
		Short: "Verify all generated ops are classified in operations.policy.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			ops, err := loadOpIndexJSON(indexPath)
			if err != nil {
				return err
			}
			classified, err := loadPolicyKeys(policyPath)
			if err != nil {
				return err
			}

			generated := map[string]opIndexEntry{}
			for _, o := range ops {
				generated[o.Op] = o
			}

			var unclassified, orphaned []string
			for op := range generated {
				if _, ok := classified[op]; !ok {
					unclassified = append(unclassified, op)
				}
			}
			for op := range classified {
				if op == "__default__" {
					continue
				}
				if _, ok := generated[op]; !ok {
					orphaned = append(orphaned, op)
				}
			}
			sort.Strings(unclassified)
			sort.Strings(orphaned)

			fmt.Printf("Sync specs report\n=================\n\n")
			fmt.Printf("Spec ops: %d\nClassified: %d\n\n", len(generated), len(classified)-1) // minus __default__

			if len(unclassified) == 0 && len(orphaned) == 0 {
				fmt.Println("All operations classified. ✓")
				return nil
			}

			if len(unclassified) > 0 {
				fmt.Printf("Unclassified operations (%d):\n", len(unclassified))
				for _, op := range unclassified {
					meta := generated[op]
					suggested := suggestClass(meta.HTTPMethod)
					fmt.Printf("  - %s\n    Heuristic: class=%s (%s)\n    Action: add to %s\n\n", op, suggested, meta.HTTPMethod, policyPath)
				}
			}
			if len(orphaned) > 0 {
				fmt.Printf("Orphaned operations (%d):\n", len(orphaned))
				for _, op := range orphaned {
					fmt.Printf("  - %s\n", op)
				}
				fmt.Printf("Action: clean up %s\n\n", policyPath)
			}

			if len(unclassified) > 0 {
				fmt.Printf("Build aborted: %d unclassified operations.\nSee docs/build-pipeline.md for the classification policy.\n", len(unclassified))
				return fmt.Errorf("classify-check: %d unclassified operations", len(unclassified))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&indexPath, "index", "internal/generated/ops_index.json", "path to generated ops index")
	cmd.Flags().StringVar(&policyPath, "policy", "operations.policy.yaml", "path to operations policy file")
	return cmd
}

type opIndexEntry struct {
	Op         string `json:"op"`
	HTTPMethod string `json:"http_method"`
	Section    string `json:"section"`
	Tag        string `json:"tag"`
	Verb       string `json:"verb"`
	Summary    string `json:"summary"`
}

func loadOpIndexJSON(path string) ([]opIndexEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var ops []opIndexEntry
	if err := json.Unmarshal(b, &ops); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return ops, nil
}

// loadPolicyKeys reads the YAML and returns the set of top-level keys.
// We deliberately don't unmarshal into a typed schema here; the runtime
// policy loader does that. classify-check only cares about presence.
func loadPolicyKeys(path string) (map[string]struct{}, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := map[string]struct{}{}
	for k := range doc {
		if k == "version" {
			continue
		}
		out[k] = struct{}{}
	}
	return out, nil
}

// suggestClass mirrors the heuristic from docs/build-pipeline.md stage 7:
// GET -> read, POST/PUT/PATCH -> write, DELETE -> destructive. Suggestion
// is informational; classify-check never auto-applies it.
func suggestClass(method string) string {
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "OPTIONS":
		return "read"
	case "POST", "PUT", "PATCH":
		return "write"
	case "DELETE":
		return "destructive"
	default:
		return "destructive"
	}
}
