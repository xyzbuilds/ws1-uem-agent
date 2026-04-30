package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"
)

// diff is informational: it compares the previously-committed specs (from
// git) against the just-fetched specs and reports added/removed/changed
// operations. Failure here doesn't fail the pipeline; classify-check is the
// gate.
//
// We shell out to git rather than vendoring a git library because the
// pipeline only ever runs on a maintainer's machine inside a clone.
func newDiffCmd() *cobra.Command {
	var baseline, newDir, outPath string
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Diff committed specs against fetched specs (informational)",
		RunE: func(cmd *cobra.Command, args []string) error {
			oldSpecs, err := loadSpecsFromGit(baseline, newDir)
			if err != nil {
				return err
			}
			newSpecs, err := loadSpecs(newDir)
			if err != nil {
				return err
			}
			report := diffSpecs(oldSpecs, newSpecs)
			b, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			b = append(b, '\n')
			if outPath == "-" || outPath == "" {
				_, err = os.Stdout.Write(b)
				return err
			}
			return os.WriteFile(outPath, b, 0o644)
		},
	}
	cmd.Flags().StringVar(&baseline, "baseline", "HEAD", "git ref to read previous specs from")
	cmd.Flags().StringVar(&newDir, "new", "spec/", "directory of just-fetched specs")
	cmd.Flags().StringVar(&outPath, "out", "-", "report path; '-' for stdout")
	return cmd
}

type diffReport struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Changed []string `json:"changed"`
}

func diffSpecs(oldSpecs, newSpecs []loadedSpec) diffReport {
	oldOps := map[string]struct{}{}
	for _, s := range oldSpecs {
		rows, _, _ := extractOps(s)
		for _, r := range rows {
			oldOps[r.Op] = struct{}{}
		}
	}
	newOps := map[string]struct{}{}
	for _, s := range newSpecs {
		rows, _, _ := extractOps(s)
		for _, r := range rows {
			newOps[r.Op] = struct{}{}
		}
	}
	var report diffReport
	for op := range newOps {
		if _, had := oldOps[op]; !had {
			report.Added = append(report.Added, op)
		}
	}
	for op := range oldOps {
		if _, has := newOps[op]; !has {
			report.Removed = append(report.Removed, op)
		}
	}
	sort.Strings(report.Added)
	sort.Strings(report.Removed)
	// Changed-detection is signature drift; left as a TODO for v0.5+.
	return report
}

// loadSpecsFromGit reads <dir>/*.json at the given git ref into a temp dir
// and parses them. If the baseline doesn't have spec/ yet (first run),
// returns an empty slice rather than an error.
func loadSpecsFromGit(ref, dir string) ([]loadedSpec, error) {
	tmp, err := os.MkdirTemp("", "ws1-build-baseline-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	out, err := exec.Command("git", "ls-tree", "--name-only", "-r", ref, "--", dir).Output()
	if err != nil {
		// No git history yet; treat as empty baseline.
		return nil, nil
	}
	var specs []loadedSpec
	for _, line := range splitLines(string(out)) {
		if line == "" {
			continue
		}
		blob, err := exec.Command("git", "show", ref+":"+line).Output()
		if err != nil {
			continue
		}
		dst := filepath.Join(tmp, filepath.Base(line))
		if err := os.WriteFile(dst, blob, 0o644); err != nil {
			continue
		}
		doc, err := loadSpec(dst)
		if err != nil {
			fmt.Fprintln(os.Stderr, "diff: skipping unparsable baseline:", line, err)
			continue
		}
		slug := stripJSONExt(filepath.Base(line))
		specs = append(specs, loadedSpec{Slug: slug, Doc: doc})
	}
	return specs, nil
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func stripJSONExt(name string) string {
	if len(name) > 5 && name[len(name)-5:] == ".json" {
		return name[:len(name)-5]
	}
	return name
}
