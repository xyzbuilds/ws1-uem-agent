package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const fakeIndexHTML = `<html>
<body>
<table>
  <tr><td><a href="/api/help/Docs/Explore?urls.primaryName=MAM%20API%20V1">MAM API V1</a></td></tr>
  <tr><td><a href="/api/help/Docs/Explore?urls.primaryName=MDM%20API%20V4">MDM API V4</a></td></tr>
  <tr><td><a href="/api/help/Docs/Explore?urls.primaryName=System%20API%20V2">System API V2</a></td></tr>
</table>
<footer>Workspace ONE UEM API Explorer 2025.11.6.68 - <a>Terms</a></footer>
</body>
</html>`

// TestDiscoverIntegrationAgainstFakeTenant spins an httptest server posing
// as a tenant's /api/help and verifies the slugifier + footer parser
// produce the expected sections + version. This is the safety net for
// docs/spec-acquisition.md changes — if the docs say MDM->mdmv4, the test
// catches a regression.
func TestDiscoverIntegrationAgainstFakeTenant(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/help", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(fakeIndexHTML))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// discoverSections expects an https tenant string; rewire by injecting
	// the test URL via a local helper.
	host := strings.TrimPrefix(srv.URL, "http://")
	d, err := discoverViaURL(srv.URL+"/api/help", host)
	if err != nil {
		t.Fatalf("discoverViaURL: %v", err)
	}
	if d.APIExplorerVersion != "2025.11.6.68" {
		t.Errorf("api_explorer_version = %q", d.APIExplorerVersion)
	}
	want := []string{"mamv1", "mdmv4", "systemv2"}
	got := make([]string, len(d.Sections))
	for i, s := range d.Sections {
		got[i] = s.Slug
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("slugs = %v, want %v", got, want)
	}
}

// discoverViaURL is a test-only helper that performs the parse-and-build
// step using an arbitrary URL (so tests don't depend on https://).
func discoverViaURL(indexURL, tenantHost string) (*DiscoveryResult, error) {
	resp, err := http.Get(indexURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil, err
	}
	sections, footer := walkIndexHTML(doc, tenantHost)
	return &DiscoveryResult{
		Tenant:             tenantHost,
		APIExplorerVersion: extractAPIExplorerVersion(footer),
		Sections:           sections,
	}, nil
}
