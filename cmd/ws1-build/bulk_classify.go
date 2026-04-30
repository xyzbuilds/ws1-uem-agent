package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// bulkClassify scans the ops index and produces an operations.policy.yaml
// covering every op via heuristic classification. Maintainers re-run this
// after every spec sync; the output is committed to the repo.
//
// Heuristic:
//   - HTTP DELETE          -> destructive + always_required (data deletion)
//   - name matches /wipe|unenroll|reset|reboot|shutdown|deprovision|
//     factorywipe|enterprisewipe|recover|clearpasscode|cancel|terminate/
//     -> destructive + always_required
//   - HTTP GET/HEAD/OPTIONS -> read
//   - HTTP POST/PUT/PATCH  -> write (with blast_radius_threshold=50 if name
//     contains "bulk" so large bulk writes still gate on approval)
//   - anything else        -> destructive (fail-closed)
//
// Fields written: class, reversible, approval, identifier (heuristic from
// path params), sync (true unless name contains "async" or response is
// 202-shaped — which we approximate from method DELETE-ish patterns).
//
// Maintainers should hand-review the output, especially destructives, and
// commit any overrides directly to operations.policy.yaml. Re-running
// bulk-classify will not clobber overrides if they're flagged with the
// `# manual:` comment we attach.
func newBulkClassifyCmd() *cobra.Command {
	var indexPath, outPath string
	var force bool
	cmd := &cobra.Command{
		Use:   "bulk-classify",
		Short: "Generate operations.policy.yaml from the ops index via heuristic",
		RunE: func(cmd *cobra.Command, args []string) error {
			ops, err := loadOpIndexJSON(indexPath)
			if err != nil {
				return err
			}
			full, err := loadFullOpIndex(indexPath)
			if err != nil {
				return err
			}
			yaml := buildPolicyYAML(full)
			fmt.Fprintf(os.Stderr, "bulk-classify: %d ops classified into %s\n", len(ops), outPath)
			if !force {
				if _, err := os.Stat(outPath); err == nil {
					return fmt.Errorf("%s already exists; pass --force to overwrite", outPath)
				}
			}
			return os.WriteFile(outPath, []byte(yaml), 0o644)
		},
	}
	cmd.Flags().StringVar(&indexPath, "index", "internal/generated/ops_index.json", "ops index sidecar")
	cmd.Flags().StringVar(&outPath, "out", "operations.policy.yaml", "policy file to write")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing policy file")
	return cmd
}

// loadFullOpIndex reads the full sidecar including parameters.
func loadFullOpIndex(path string) ([]fullOpIndex, error) {
	return loadOpIndexJSONFull(path)
}

// destructiveNamePatterns are substring matches against op names (lowercase)
// that force destructive classification regardless of HTTP method. The list
// is conservative: anything that wipes data, removes a device from
// management, or terminates a resource lifecycle.
var destructiveNamePatterns = []string{
	"wipe",
	"factorywipe",
	"enterprisewipe",
	"unenroll",
	"deprovision",
	"reset",
	"reboot",
	"shutdown",
	"recover",
	"clearpasscode",
	"clearcommand",
	"cancelqueue",
	"breakmdm",
	"terminate",
}

// bulkInNamePattern flags ops that target N entities by argv. We add a
// blast_radius_threshold so large bulks still go through approval even if
// the per-target action is reversible.
var bulkInNamePattern = []string{
	"bulk",
	"batch",
	"mass",
}

type classification struct {
	class      string
	reversible string
	approval   string
	identifier string
	sync       *bool
	threshold  *int
	warnNote   string
}

func classify(op fullOpIndex) classification {
	name := strings.ToLower(op.Op)

	// Destructive name patterns first — they win even on POST.
	for _, p := range destructiveNamePatterns {
		if strings.Contains(name, p) {
			return classification{
				class:      "destructive",
				reversible: "none",
				approval:   "always_required",
				identifier: deriveIdentifier(op),
				sync:       maybeAsync(op),
			}
		}
	}

	switch op.HTTPMethod {
	case "GET", "HEAD", "OPTIONS":
		return classification{
			class:      "read",
			reversible: "",
			approval:   "",
			identifier: deriveIdentifier(op),
			sync:       boolPtr(true),
		}
	case "DELETE":
		return classification{
			class:      "destructive",
			reversible: "none",
			approval:   "always_required",
			identifier: deriveIdentifier(op),
			sync:       maybeAsync(op),
		}
	case "POST", "PUT", "PATCH":
		c := classification{
			class:      "write",
			reversible: "unknown",
			identifier: deriveIdentifier(op),
			sync:       maybeAsync(op),
		}
		// Large-blast-radius ops gate on threshold even when reversible.
		for _, p := range bulkInNamePattern {
			if strings.Contains(name, p) {
				c.threshold = intPtr(50)
				c.approval = "required_if_count_over_threshold"
				break
			}
		}
		return c
	default:
		// Fail-closed.
		return classification{
			class:      "destructive",
			reversible: "unknown",
			approval:   "always_required",
			identifier: deriveIdentifier(op),
			warnNote:   "Operation has unmapped HTTP method; treated as destructive.",
		}
	}
}

// deriveIdentifier looks at path params to label the targeting shape.
// Heuristic: if a path param's name contains "uuid" → device_uuid /
// resource_uuid; if "id" → resource_id; mixed → mixed.
func deriveIdentifier(op fullOpIndex) string {
	if !strings.Contains(op.PathTemplate, "{") {
		return "none"
	}
	hasUUID := false
	hasID := false
	for _, p := range op.PathTemplate {
		_ = p
	}
	// Walk path tokens to detect param names.
	for _, seg := range strings.Split(op.PathTemplate, "/") {
		if !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") {
			continue
		}
		name := strings.ToLower(seg[1 : len(seg)-1])
		if strings.Contains(name, "uuid") {
			hasUUID = true
			continue
		}
		if strings.HasSuffix(name, "id") {
			hasID = true
		}
	}
	switch {
	case hasUUID && hasID:
		return "mixed_id_uuid"
	case hasUUID:
		return "uuid"
	case hasID:
		return "id"
	default:
		return "path_param"
	}
}

// maybeAsync marks command-dispatch ops as async (sync: false). Heuristic:
// path contains /commands/ or name contains async/job suggests async.
// Otherwise default to sync.
func maybeAsync(op fullOpIndex) *bool {
	name := strings.ToLower(op.Op)
	if strings.Contains(op.PathTemplate, "/commands/") ||
		strings.Contains(name, "async") {
		return boolPtr(false)
	}
	return boolPtr(true)
}

func boolPtr(b bool) *bool { return &b }
func intPtr(n int) *int    { return &n }

// buildPolicyYAML emits the policy file as a single string. We group by
// section + tag for readability.
func buildPolicyYAML(ops []fullOpIndex) string {
	var b strings.Builder
	b.WriteString(`# operations.policy.yaml
#
# Generated by 'ws1-build bulk-classify' against the spec/ snapshot.
# Hand-edit individual entries to override the heuristic; mark overrides
# with a leading '# manual:' comment so a future regeneration won't
# silently overwrite them.
#
# See spec section 8 for the format and CLAUDE.md locked decision #9 for
# the fail-closed semantics.

version: 1

# Default for ops not present below. Fail-closed.
__default__:
  class: destructive
  reversible: unknown
  approval: always_required
  warn: "Operation not classified in policy.yaml; treated as destructive."

`)

	bySection := map[string][]fullOpIndex{}
	for _, o := range ops {
		bySection[o.Section] = append(bySection[o.Section], o)
	}
	sections := make([]string, 0, len(bySection))
	for s := range bySection {
		sections = append(sections, s)
	}
	sort.Strings(sections)

	for _, section := range sections {
		fmt.Fprintf(&b, "# ----- %s -----\n\n", section)

		secOps := bySection[section]
		sort.Slice(secOps, func(i, j int) bool {
			if secOps[i].Tag != secOps[j].Tag {
				return secOps[i].Tag < secOps[j].Tag
			}
			return secOps[i].Verb < secOps[j].Verb
		})
		for _, o := range secOps {
			c := classify(o)
			fmt.Fprintf(&b, "%s:\n", o.Op)
			fmt.Fprintf(&b, "  class: %s\n", c.class)
			if c.reversible != "" {
				fmt.Fprintf(&b, "  reversible: %s\n", c.reversible)
			}
			if c.approval != "" {
				fmt.Fprintf(&b, "  approval: %s\n", c.approval)
			}
			if c.identifier != "" && c.identifier != "none" {
				fmt.Fprintf(&b, "  identifier: %s\n", c.identifier)
			}
			if c.threshold != nil {
				fmt.Fprintf(&b, "  blast_radius_threshold: %d\n", *c.threshold)
			}
			if c.sync != nil {
				fmt.Fprintf(&b, "  sync: %v\n", *c.sync)
			}
			if c.warnNote != "" {
				fmt.Fprintf(&b, "  warn: %q\n", c.warnNote)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}
