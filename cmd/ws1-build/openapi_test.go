package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestExtractOpsFromMinimalSpec(t *testing.T) {
	specs, err := loadSpecs("../../spec")
	if err != nil {
		t.Fatalf("loadSpecs: %v", err)
	}
	if len(specs) < 2 {
		t.Fatalf("expected >= 2 specs, got %d", len(specs))
	}

	wantOps := map[string]string{
		"mdmv4.devices.search":      "GET",
		"mdmv4.devices.get":         "GET",
		"mdmv4.devices.lock":        "POST",
		"mdmv4.devices.wipe":        "POST",
		"mdmv4.devices.bulkcommand": "POST",
		"systemv2.users.search":     "GET",
		"systemv2.users.get":        "GET",
	}
	got := map[string]string{}
	for _, s := range specs {
		rows, _, err := extractOps(s)
		if err != nil {
			t.Fatalf("extractOps: %v", err)
		}
		for _, r := range rows {
			got[r.Op] = r.HTTPMethod
		}
	}

	for op, method := range wantOps {
		if got[op] != method {
			t.Errorf("op %q: HTTP method = %q, want %q", op, got[op], method)
		}
	}
	if len(got) != len(wantOps) {
		extras := []string{}
		for op := range got {
			if _, want := wantOps[op]; !want {
				extras = append(extras, op)
			}
		}
		sort.Strings(extras)
		t.Errorf("unexpected ops: %v", extras)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"MAM API V1":     "mamv1",
		"MAM API V2":     "mamv2",
		"MCM API":        "mcmv1",
		"MDM API V4":     "mdmv4",
		"MEM API":        "memv1",
		"System API V1":  "systemv1",
		"System API V2":  "systemv2",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripHostFromServerURL(t *testing.T) {
	cases := map[string]string{
		"https://as1831.awmdm.com/api/mcm": "/api/mcm",
		"https://example.com/api/mdm/v4":   "/api/mdm/v4",
		"/api/mdm":                         "/api/mdm",
		"":                                 "",
	}
	for in, want := range cases {
		if got := stripHostFromServerURL(in); got != want {
			t.Errorf("stripHostFromServerURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePrimaryName(t *testing.T) {
	cases := map[string]struct {
		name string
		ok   bool
	}{
		"/api/help/Docs/Explore?urls.primaryName=MAM%20API%20V1": {"MAM API V1", true},
		"/api/help/Docs/Explore?urls.primaryName=System%20API":    {"System API", true},
		"/something/else?foo=bar":                                 {"", false},
		"":                                                        {"", false},
	}
	for href, want := range cases {
		got, ok := parsePrimaryName(href)
		if ok != want.ok || got != want.name {
			t.Errorf("parsePrimaryName(%q) = (%q, %v), want (%q, %v)", href, got, ok, want.name, want.ok)
		}
	}
}

func TestExtractAPIExplorerVersion(t *testing.T) {
	cases := map[string]string{
		"Workspace ONE UEM API Explorer 2025.11.6.68 - Terms": "2025.11.6.68",
		"API Explorer  9.9.9":                                 "9.9.9",
		"no version here":                                     "unknown",
	}
	for in, want := range cases {
		if got := extractAPIExplorerVersion(in); got != want {
			t.Errorf("extractAPIExplorerVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCodegenRoundTrip: codegen the minimal spec into a temp dir and verify
// the resulting ops_index.go compiles by writing it under a temp module.
// That guarantees the template produces valid Go source.
func TestCodegenRoundTrip(t *testing.T) {
	specs, _ := loadSpecs("../../spec")
	var allOps []opRow
	for _, s := range specs {
		rows, _, err := extractOps(s)
		if err != nil {
			t.Fatalf("extractOps: %v", err)
		}
		allOps = append(allOps, rows...)
	}
	tmp := t.TempDir()
	if err := emitOpsIndexGo(filepath.Join(tmp, "ops_index.go"), allOps); err != nil {
		t.Fatalf("emitOpsIndexGo: %v", err)
	}
	if err := emitOpsIndexJSON(filepath.Join(tmp, "ops_index.json"), allOps); err != nil {
		t.Fatalf("emitOpsIndexJSON: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "ops_index.go")); err != nil {
		t.Fatalf("missing ops_index.go: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "ops_index.json")); err != nil {
		t.Fatalf("missing ops_index.json: %v", err)
	}
}
