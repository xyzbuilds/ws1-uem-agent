// Command ws1-build is the maintainer pipeline that keeps ws1's compiled API
// surface in sync with the WS1 UEM tenant's OpenAPI specs. End-users never
// run ws1-build; only the project maintainer does, and the output (spec/,
// internal/generated/, skills/ws1-uem/reference/) is checked into the repo.
//
// See docs/build-pipeline.md for the full pipeline and docs/spec-acquisition.md
// for the discovery/slugification rules.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "ws1-build",
		Short: "Maintainer pipeline for ws1 (spec sync, code-gen, classification)",
		Long: `ws1-build is the maintainer-side toolchain that pulls WS1 OpenAPI specs,
regenerates Go bindings, regenerates the skill reference, and gates the
build on classification of new operations in operations.policy.yaml.

End-users never run this; only maintainers do, when refreshing specs.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newDiscoverCmd(),
		newFetchCmd(),
		newDiffCmd(),
		newCodegenCLICmd(),
		newCodegenSkillCmd(),
		newClassifyCheckCmd(),
	)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "ws1-build:", err)
		os.Exit(1)
	}
}
