package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xyzbuilds/ws1-uem-agent/internal/auth"
)

// setupTestServer spins a small mock that responds to OAuth + an
// OG-list call. Returned closer must be invoked.
func setupTestServer(t *testing.T) (string, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("client_id") == "" {
			http.Error(w, "missing client_id", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
	})
	mux.HandleFunc("/api/system/groups/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"LocationGroups":[
			{"Id":1,"Uuid":"u1","Name":"Global"},
			{"Id":4067,"Uuid":"u4067","Name":"EMEA-Pilot"}
		],"Total":2}`))
	})
	mux.HandleFunc("/api/mdm/devices/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Devices":[{"DeviceID":1}],"Total":1}`))
	})
	srv := httptest.NewServer(mux)
	return srv.URL, srv.Close
}

func TestSetupQuickStartHappyPath(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", cfg)
	t.Setenv("HOME", cfg)
	t.Setenv("WS1_ALLOW_DISK_SECRETS", "1")
	t.Setenv("WS1_FORCE_INTERACTIVE", "1")

	url, closer := setupTestServer(t)
	defer closer()
	t.Setenv("WS1_BASE_URL", url)

	stub := &StubPrompter{
		AskAnswers: map[string]string{
			"Tenant hostname": "cn1506.awmdm.com",
			"Client ID":       "cid",
		},
		SecretAnswers: []string{"csec"},
		PickIndex: []int{
			2, // region: na
			2, // OG: EMEA-Pilot (second entry)
		},
	}

	opts := SetupOptions{
		Profile:   "operator",
		AuthURL:   url + "/oauth",
		Quick:     true,
		SkipSmoke: true, // smoke uses mdmv1.devices.search; mock provides
	}

	err := RunSetup(context.Background(), opts, stub)
	if err != nil {
		t.Fatalf("RunSetup: %v", err)
	}

	// Profile written?
	profiles, perr := auth.LoadProfiles()
	if perr != nil {
		t.Fatalf("LoadProfiles: %v", perr)
	}
	if len(profiles) != 1 || profiles[0].Name != "operator" {
		t.Fatalf("profiles = %+v", profiles)
	}
	if profiles[0].Tenant != "cn1506.awmdm.com" {
		t.Errorf("tenant = %q", profiles[0].Tenant)
	}
	if profiles[0].AuthURL != url+"/oauth" {
		t.Errorf("auth_url = %q", profiles[0].AuthURL)
	}
	// Active profile set to operator (only one configured).
	active, _ := auth.Active()
	if active != "operator" {
		t.Errorf("active = %q, want operator", active)
	}
	// OG context set.
	og, _ := auth.CurrentOG()
	if og != "4067" {
		t.Errorf("og = %q, want 4067", og)
	}
	// Spinner messages emitted.
	if !containsLabel(stub.Spins, "Validating") {
		t.Errorf("expected Validating spinner; got %v", stub.Spins)
	}
	if !containsLabel(stub.Spins, "Fetching organization groups") {
		t.Errorf("expected OG fetch spinner; got %v", stub.Spins)
	}
}

func containsLabel(labels []string, prefix string) bool {
	for _, l := range labels {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func TestSetupV2OGResponseShape(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", cfg)
	t.Setenv("HOME", cfg)
	t.Setenv("WS1_ALLOW_DISK_SECRETS", "1")
	t.Setenv("WS1_FORCE_INTERACTIVE", "1")

	// Mock that emits ONLY the v2 key (OrganizationGroups), not the v1
	// alias. Mirrors what a real WS1 tenant returns from the v2 op.
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
	})
	mux.HandleFunc("/api/system/groups/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"OrganizationGroups":[
			{"Id":1,"Uuid":"u1","Name":"Global"},
			{"Id":4067,"Uuid":"u4067","Name":"EMEA-Pilot"}
		],"TotalResults":2}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("WS1_BASE_URL", srv.URL)

	stub := &StubPrompter{
		AskAnswers: map[string]string{
			"Tenant hostname": "cn1506.awmdm.com",
			"Client ID":       "cid",
		},
		SecretAnswers: []string{"csec"},
		PickIndex: []int{
			2, // OG: EMEA-Pilot (second entry; only one Pick — region was supplied via AuthURL)
		},
	}

	opts := SetupOptions{
		Profile:   "operator",
		AuthURL:   srv.URL + "/oauth",
		Quick:     true,
		SkipSmoke: true,
	}

	if err := RunSetup(context.Background(), opts, stub); err != nil {
		t.Fatalf("RunSetup with v2 response: %v", err)
	}

	og, _ := auth.CurrentOG()
	if og != "4067" {
		t.Errorf("og = %q (v2 OrganizationGroups response should populate picker); want 4067", og)
	}
}
