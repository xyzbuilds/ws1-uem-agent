// Package policy loads operations.policy.yaml and exposes per-operation
// classification (class / reversibility / approval rules / blast-radius
// thresholds) to the rest of the CLI.
//
// Fail-closed semantics per spec section 8: any operation NOT present in
// the loaded policy is treated as destructive + approval-required, with a
// runtime warning. The `__default__` block in the YAML lets a maintainer
// see this in writing.
//
// The policy file is also embedded into the binary via go:embed so end-user
// installs ship with the safety gate already wired even if the user's
// filesystem doesn't have a policy file.
package policy

import (
	_ "embed"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// embeddedPolicy is the operations.policy.yaml contents at build time. The
// runtime loader prefers a file on disk if one exists, but falls back to
// this so a `go install`-from-source binary still has a valid baseline.
//
//go:embed default.policy.yaml
var embeddedPolicy []byte

// Class is the canonical classification: read / write / destructive.
type Class string

const (
	ClassRead        Class = "read"
	ClassWrite       Class = "write"
	ClassDestructive Class = "destructive"
)

// ApprovalMode controls when the approval flow is required.
type ApprovalMode string

const (
	ApprovalNone                       ApprovalMode = ""
	ApprovalAlwaysRequired             ApprovalMode = "always_required"
	ApprovalRequiredIfCountOverThreshold ApprovalMode = "required_if_count_over_threshold"
)

// Reversibility describes how recoverable the op is. Affects how
// confidently the agent should propose it.
type Reversibility string

const (
	ReversibleFull              Reversibility = "full"
	ReversiblePartial           Reversibility = "partial"
	ReversibleNone              Reversibility = "none"
	ReversibleUnknown           Reversibility = "unknown"
	ReversibleDependsOnCommand  Reversibility = "depends_on_command"
)

// Identifier describes the argv shape needed to target the op.
// Free-form string; common values: none / device_id / device_id_array /
// user_id / smart_group_id.
type Identifier string

// Entry is the per-op classification.
type Entry struct {
	Op         string        `yaml:"-"`
	Class      Class         `yaml:"class"`
	Reversible Reversibility `yaml:"reversible"`
	Approval   ApprovalMode  `yaml:"approval"`
	Identifier Identifier    `yaml:"identifier"`
	Sync       *bool         `yaml:"sync"`

	// blast_radius_threshold: ops targeting more than this many entities
	// trigger the approval flow even if Approval is "none".
	BlastRadiusThreshold *int `yaml:"blast_radius_threshold"`

	// warn_if_results_over: read-class only; emit a warning when a search
	// returns more than this many results so the agent can pause.
	WarnIfResultsOver *int `yaml:"warn_if_results_over"`

	// Warn is a human-readable note attached to the entry.
	Warn string `yaml:"warn"`

	// Synthetic indicates this entry was synthesized from __default__,
	// not explicitly present. Used by the runtime to tag UNKNOWN_OPERATION.
	Synthetic bool `yaml:"-"`
}

// IsDestructive returns true if the entry's class or approval mode forces
// the approval flow regardless of count.
func (e *Entry) IsDestructive() bool {
	return e.Class == ClassDestructive || e.Approval == ApprovalAlwaysRequired
}

// RequiresApproval reports whether running this op against `targetCount`
// targets requires browser approval.
func (e *Entry) RequiresApproval(targetCount int) bool {
	if e.IsDestructive() {
		return true
	}
	if e.Approval == ApprovalRequiredIfCountOverThreshold {
		if e.BlastRadiusThreshold != nil && targetCount > *e.BlastRadiusThreshold {
			return true
		}
	}
	if e.BlastRadiusThreshold != nil && targetCount > *e.BlastRadiusThreshold {
		return true
	}
	return false
}

// Policy is the loaded operations.policy.yaml.
type Policy struct {
	Version int
	Default Entry
	Entries map[string]Entry
}

// Default is the spec-mandated fail-closed default for unclassified ops.
// Used both as a sanity floor and when the YAML omits __default__.
func builtinDefault() Entry {
	return Entry{
		Class:      ClassDestructive,
		Reversible: ReversibleUnknown,
		Approval:   ApprovalAlwaysRequired,
		Warn:       "Operation not classified in policy.yaml; treated as destructive (fail-closed).",
	}
}

// Load reads the YAML at path. If path is empty or doesn't resolve, falls
// back to the embedded default policy (so a freshly-installed binary still
// works).
func Load(path string) (*Policy, error) {
	var raw []byte
	if path != "" {
		var err error
		raw, err = readPolicyFile(path)
		if err != nil {
			// Fall back to embedded; a missing user-configured file
			// shouldn't brick the CLI.
			raw = embeddedPolicy
		}
	} else {
		raw = embeddedPolicy
	}
	return parse(raw)
}

func parse(raw []byte) (*Policy, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse policy: %w", err)
	}
	p := &Policy{
		Version: 1,
		Default: builtinDefault(),
		Entries: map[string]Entry{},
	}
	if v, ok := doc["version"]; ok {
		switch n := v.(type) {
		case int:
			p.Version = n
		case float64:
			p.Version = int(n)
		}
	}
	for k, v := range doc {
		if k == "version" {
			continue
		}
		bb, _ := yaml.Marshal(v)
		var e Entry
		if err := yaml.Unmarshal(bb, &e); err != nil {
			return nil, fmt.Errorf("parse policy entry %q: %w", k, err)
		}
		e.Op = k
		if k == "__default__" {
			// Override the built-in default if the YAML supplied one.
			merged := builtinDefault()
			if e.Class != "" {
				merged.Class = e.Class
			}
			if e.Reversible != "" {
				merged.Reversible = e.Reversible
			}
			if e.Approval != "" {
				merged.Approval = e.Approval
			}
			if e.Warn != "" {
				merged.Warn = e.Warn
			}
			p.Default = merged
			continue
		}
		p.Entries[k] = e
	}
	return p, nil
}

// Classify returns the entry for the given canonical op identifier. If the
// op is unclassified, returns a synthetic entry derived from __default__
// with Synthetic=true so the caller can attach UNKNOWN_OPERATION semantics.
func (p *Policy) Classify(op string) Entry {
	if e, ok := p.Entries[op]; ok {
		return e
	}
	def := p.Default
	def.Op = op
	def.Synthetic = true
	return def
}

// Has reports whether the op is explicitly classified.
func (p *Policy) Has(op string) bool {
	_, ok := p.Entries[op]
	return ok
}

// Ops returns all explicitly-classified op identifiers.
func (p *Policy) Ops() []string {
	out := make([]string, 0, len(p.Entries))
	for k := range p.Entries {
		out = append(out, k)
	}
	// stable order
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// readPolicyFile is a tiny wrapper to keep the import block tight; helps
// maintain the "embed as fallback" semantic without an os import here.
func readPolicyFile(path string) ([]byte, error) {
	return readFile(path)
}

// trimYAML strips a leading "---" if present so embedded files behave the
// same whether or not a maintainer added one. Currently unused but kept as
// a safety net for future format tweaks.
func trimYAML(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, "---\n") {
		s = s[4:]
	}
	return []byte(s)
}
