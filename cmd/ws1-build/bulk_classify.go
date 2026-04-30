package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newBulkClassifyCmd keeps operations.policy.yaml in sync with the ops
// index across spec syncs. Three modes:
//
//	default (merge):  read the existing policy, append heuristic entries
//	                  for ops in the index that aren't already classified,
//	                  leave existing entries (and any manual overrides)
//	                  untouched, and report orphans without removing them.
//	                  This is the right mode for incremental tenant-sync
//	                  PRs — preserves human review work across quarterly
//	                  spec bumps.
//
//	--regenerate:     ignore the existing file; rewrite from scratch via
//	                  heuristic. Right for the very first run or when a
//	                  maintainer explicitly wants to throw away overrides.
//
//	--prune:          additionally drop entries whose ops no longer appear
//	                  in the index. Off by default: orphans usually
//	                  indicate the op was renamed; review before deleting.
func newBulkClassifyCmd() *cobra.Command {
	var indexPath, outPath string
	var regenerate, prune bool
	cmd := &cobra.Command{
		Use:   "bulk-classify",
		Short: "Keep operations.policy.yaml in sync with the ops index",
		Long: `Default behavior is merge: heuristically classify ops that
aren't already in the policy file, leaving existing entries (and any manual
overrides) alone. Use --regenerate to throw away the existing file and
rewrite from scratch (right for the first run, wrong for incremental
syncs that would clobber human review work). Use --prune to also drop
entries whose ops have disappeared from the index.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			full, err := loadFullOpIndex(indexPath)
			if err != nil {
				return err
			}

			// Regenerate mode: behave like a fresh bootstrap.
			if regenerate {
				yaml := buildPolicyYAML(full)
				if err := os.WriteFile(outPath, []byte(yaml), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "bulk-classify (regenerate): wrote %d ops to %s\n", len(full), outPath)
				return nil
			}

			// Merge mode (default): only add what's missing.
			existing, err := os.ReadFile(outPath)
			if os.IsNotExist(err) {
				// First run: no existing file. Treat like regenerate.
				yaml := buildPolicyYAML(full)
				if err := os.WriteFile(outPath, []byte(yaml), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "bulk-classify (initial): wrote %d ops to %s\n", len(full), outPath)
				return nil
			}
			if err != nil {
				return err
			}
			merged, report, err := mergePolicy(existing, full, prune)
			if err != nil {
				return err
			}
			if err := os.WriteFile(outPath, merged, 0o644); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, report.summary())
			return nil
		},
	}
	cmd.Flags().StringVar(&indexPath, "index", "internal/generated/ops_index.json", "ops index sidecar")
	cmd.Flags().StringVar(&outPath, "out", "operations.policy.yaml", "policy file to write")
	cmd.Flags().BoolVar(&regenerate, "regenerate", false, "rewrite the policy from scratch (clobbers manual overrides)")
	cmd.Flags().BoolVar(&prune, "prune", false, "remove entries for ops no longer present in the index")
	return cmd
}

// mergeReport summarizes what the merge did.
type mergeReport struct {
	preserved int
	added     []string
	orphaned  []string
	pruned    []string
}

func (r mergeReport) summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "bulk-classify (merge): %d existing entries preserved, %d added", r.preserved, len(r.added))
	if len(r.pruned) > 0 {
		fmt.Fprintf(&b, ", %d pruned", len(r.pruned))
	}
	fmt.Fprintln(&b)
	if len(r.added) > 0 {
		fmt.Fprintln(&b, "added:")
		shown := r.added
		if len(shown) > 20 {
			shown = shown[:20]
		}
		for _, op := range shown {
			fmt.Fprintf(&b, "  + %s\n", op)
		}
		if len(r.added) > 20 {
			fmt.Fprintf(&b, "  ... and %d more\n", len(r.added)-20)
		}
	}
	if len(r.orphaned) > 0 && len(r.pruned) == 0 {
		fmt.Fprintf(&b, "orphaned (still present in policy, no longer in index): %d\n", len(r.orphaned))
		fmt.Fprintln(&b, "  pass --prune to remove them, or hand-review (renames usually surface here).")
		shown := r.orphaned
		if len(shown) > 10 {
			shown = shown[:10]
		}
		for _, op := range shown {
			fmt.Fprintf(&b, "  ? %s\n", op)
		}
	}
	return b.String()
}

// mergePolicy reads existing YAML, classifies any ops in `full` that
// aren't present, and appends them to the file. Existing entries are
// preserved verbatim — including manual overrides, comments, and field
// choices that diverge from the heuristic. With prune=true, orphan
// entries (in policy but not in index) are removed; otherwise they're
// listed in the report.
func mergePolicy(existing []byte, full []fullOpIndex, prune bool) ([]byte, mergeReport, error) {
	// Build the set of op identifiers currently in the policy file by
	// scanning for top-level YAML keys. We do a lightweight line scan
	// rather than a full YAML parse because we want to preserve the
	// original file's comments, ordering, and formatting on the
	// preserved entries.
	existingOps := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		// A top-level op key is a non-indented line that ends in ':'
		// and isn't a YAML directive or block comment.
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "---") {
			continue
		}
		if i := strings.Index(line, ":"); i > 0 && i == len(strings.TrimRight(line, " "))-1 {
			key := line[:i]
			if key == "version" || key == "__default__" {
				continue
			}
			existingOps[key] = true
		}
	}

	// Build the index set of canonical op identifiers.
	indexOps := map[string]fullOpIndex{}
	for _, o := range full {
		indexOps[o.Op] = o
	}

	// Detect orphans (in policy, not in index).
	report := mergeReport{preserved: len(existingOps)}
	for op := range existingOps {
		if _, ok := indexOps[op]; !ok {
			report.orphaned = append(report.orphaned, op)
		}
	}
	sort.Strings(report.orphaned)

	// Detect ops to add (in index, not in policy).
	var newOps []fullOpIndex
	for _, o := range full {
		if !existingOps[o.Op] {
			newOps = append(newOps, o)
			report.added = append(report.added, o.Op)
		}
	}
	sort.Slice(newOps, func(i, j int) bool { return newOps[i].Op < newOps[j].Op })
	sort.Strings(report.added)

	// Build the appended block.
	out := existing
	if !strings.HasSuffix(string(out), "\n") {
		out = append(out, '\n')
	}
	if len(newOps) > 0 {
		var addBuf strings.Builder
		fmt.Fprintf(&addBuf, "\n# ----- merged %s -----\n", time.Now().UTC().Format("2006-01-02"))
		fmt.Fprintf(&addBuf, "# %d new ops added by bulk-classify merge mode.\n", len(newOps))
		fmt.Fprintf(&addBuf, "# Hand-review and either keep the heuristic, override, or remove.\n\n")
		for _, o := range newOps {
			fmt.Fprint(&addBuf, formatPolicyEntry(o))
		}
		out = append(out, addBuf.String()...)
	}

	// Prune.
	if prune && len(report.orphaned) > 0 {
		report.pruned = report.orphaned
		report.orphaned = nil
		var err error
		out, err = removeEntriesByOp(out, report.pruned)
		if err != nil {
			return nil, report, err
		}
	}

	return out, report, nil
}

// formatPolicyEntry renders one op entry in the YAML format buildPolicyYAML
// uses, so merge-mode and regenerate-mode produce indistinguishable text
// for new ops.
func formatPolicyEntry(o fullOpIndex) string {
	c := classify(o)
	var b strings.Builder
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
	return b.String()
}

// removeEntriesByOp surgically deletes the entries for the named ops from
// the YAML text, preserving everything else (formatting, comments, other
// entries). An entry runs from its top-level key line to the next blank
// line or top-level key.
func removeEntriesByOp(content []byte, ops []string) ([]byte, error) {
	toDrop := map[string]bool{}
	for _, op := range ops {
		toDrop[op] = true
	}
	lines := strings.Split(string(content), "\n")
	var out []string
	skipping := false
	for _, line := range lines {
		// Detect entry start (top-level key).
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") &&
			!strings.HasPrefix(line, "#") && line != "" {
			if i := strings.Index(line, ":"); i > 0 {
				key := strings.TrimRight(line[:i], " ")
				if toDrop[key] {
					skipping = true
					continue
				}
				skipping = false
			}
		}
		if skipping {
			// Skip indented continuation lines until we hit a blank or
			// a new top-level key.
			if line == "" {
				skipping = false
				out = append(out, line)
				continue
			}
			if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				skipping = false
				out = append(out, line)
				continue
			}
			continue
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n")), nil
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
