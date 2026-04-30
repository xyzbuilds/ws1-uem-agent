package main

import (
	"strings"
	"testing"
)

// TestRegionsTable pins the canonical region->URL mapping so a future
// edit can't silently change what `--region na` points at. Source:
// docs.omnissa.com Workspace ONE UEM Console Basics, "Datacenter and
// Token URLs for OAuth 2.0 Support".
func TestRegionsTable(t *testing.T) {
	expected := map[string]string{
		"uat":  "https://uat.uemauth.workspaceone.com/connect/token",
		"na":   "https://na.uemauth.workspaceone.com/connect/token",
		"emea": "https://emea.uemauth.workspaceone.com/connect/token",
		"apac": "https://apac.uemauth.workspaceone.com/connect/token",
	}
	if len(Regions) != len(expected) {
		t.Errorf("Regions has %d entries, want %d", len(Regions), len(expected))
	}
	for code, url := range expected {
		got, ok := regionToAuthURL(code)
		if !ok {
			t.Errorf("region %q missing from table", code)
			continue
		}
		if got != url {
			t.Errorf("region %q -> %q, want %q", code, got, url)
		}
	}
}

// TestRegionsAllOnWorkspaceOneDomain ensures we don't accidentally
// regress to the legacy vmwservices.com domain (which Omnissa is
// phasing out).
func TestRegionsAllOnWorkspaceOneDomain(t *testing.T) {
	for _, r := range Regions {
		if !strings.Contains(r.TokenURL, "uemauth.workspaceone.com") {
			t.Errorf("region %q URL %q is not on the workspaceone.com domain (legacy vmwservices.com is deprecated)", r.Code, r.TokenURL)
		}
		if !strings.HasSuffix(r.TokenURL, "/connect/token") {
			t.Errorf("region %q URL %q is not a /connect/token endpoint", r.Code, r.TokenURL)
		}
	}
}

func TestRegionUnknownReturnsFalse(t *testing.T) {
	if _, ok := regionToAuthURL("zzz"); ok {
		t.Error("unknown region should return false")
	}
}

func TestRegionCodesStringIncludesAll(t *testing.T) {
	s := regionCodesString()
	for _, r := range Regions {
		if !strings.Contains(s, r.Code) {
			t.Errorf("regionCodesString %q missing code %q", s, r.Code)
		}
	}
}
