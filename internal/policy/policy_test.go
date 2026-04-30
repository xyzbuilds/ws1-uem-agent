package policy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEmbeddedDefault(t *testing.T) {
	p, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Default.Class != ClassDestructive {
		t.Errorf("default class = %q, want destructive", p.Default.Class)
	}
	if p.Default.Approval != ApprovalAlwaysRequired {
		t.Errorf("default approval = %q, want always_required", p.Default.Approval)
	}
}

func TestClassifyKnownOp(t *testing.T) {
	p, _ := Load("")
	e := p.Classify("mdmv4.devices.search")
	if e.Synthetic {
		t.Error("known op flagged as synthetic")
	}
	if e.Class != ClassRead {
		t.Errorf("class = %q, want read", e.Class)
	}
}

func TestClassifyUnknownOp_FailClosed(t *testing.T) {
	p, _ := Load("")
	e := p.Classify("totally.fake.op")
	if !e.Synthetic {
		t.Error("unknown op should be flagged synthetic")
	}
	if e.Class != ClassDestructive {
		t.Errorf("unknown op class = %q, want destructive (fail-closed)", e.Class)
	}
	if e.Approval != ApprovalAlwaysRequired {
		t.Errorf("unknown op approval = %q, want always_required", e.Approval)
	}
	if !e.IsDestructive() {
		t.Error("unknown op should be IsDestructive()")
	}
}

func TestRequiresApprovalDestructiveAlways(t *testing.T) {
	e := Entry{Class: ClassDestructive, Approval: ApprovalAlwaysRequired}
	for _, n := range []int{0, 1, 100, 10000} {
		if !e.RequiresApproval(n) {
			t.Errorf("destructive op should require approval at count %d", n)
		}
	}
}

func TestRequiresApprovalThresholdGated(t *testing.T) {
	thresh := 50
	e := Entry{
		Class:                ClassWrite,
		Approval:             ApprovalRequiredIfCountOverThreshold,
		BlastRadiusThreshold: &thresh,
	}
	if e.RequiresApproval(50) {
		t.Error("count == threshold should NOT require approval (>= test)")
	}
	if !e.RequiresApproval(51) {
		t.Error("count > threshold should require approval")
	}
	if e.RequiresApproval(0) {
		t.Error("count under threshold should not require approval")
	}
}

func TestRequiresApprovalImpliedThreshold(t *testing.T) {
	thresh := 50
	// No explicit Approval mode but a blast_radius_threshold; behavior is
	// "blast radius gated" — useful for write-class lock that's safe at
	// small N but needs review at large N.
	e := Entry{Class: ClassWrite, BlastRadiusThreshold: &thresh}
	if !e.RequiresApproval(51) {
		t.Error("threshold-only entry should still gate at >threshold")
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.yaml")
	custom := []byte(`version: 1
__default__:
  class: read
  reversible: full
  approval: ""
  warn: custom default
foo.bar.baz:
  class: write
  reversible: full
  identifier: foo_id
`)
	if err := os.WriteFile(path, custom, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Default.Class != ClassRead {
		t.Errorf("custom default class = %q, want read", p.Default.Class)
	}
	if !p.Has("foo.bar.baz") {
		t.Error("custom op missing")
	}
}

func TestLoadMissingFileFallsBack(t *testing.T) {
	p, err := Load("/nonexistent/path/policy.yaml")
	if err != nil {
		t.Fatalf("Load on missing file should fall back, got error: %v", err)
	}
	// Embedded default has these ops:
	if !p.Has("mdmv4.devices.search") {
		t.Error("embedded fallback should still know about devices.search")
	}
}

func TestEntryIdentifierPreserved(t *testing.T) {
	p, _ := Load("")
	e := p.Classify("mdmv4.devices.lock")
	if e.Identifier != "device_id" {
		t.Errorf("identifier = %q, want device_id", e.Identifier)
	}
}
