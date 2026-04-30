package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xyzbuilds/ws1-uem-agent/internal/auth"
	"github.com/xyzbuilds/ws1-uem-agent/internal/generated"
)

// Real op identifiers from the spec snapshot in spec/. Tests pin against
// these so a future spec sync that renames them surfaces here first.
const (
	opDevicesSearch       = "mdmv1.devices.search"         // GET /devices/search
	opUserGetByUuid       = "systemv2.usersv2.read"        // GET /users/{uuid}
	opCommandsBulkExecute = "mdmv1.commandsv1.bulkexecute" // POST /devices/commands/bulk
)

func TestBuildURLPathAndQuery(t *testing.T) {
	meta := generated.Ops[opDevicesSearch]
	if meta.Op == "" {
		t.Fatalf("op %q not in compiled index", opDevicesSearch)
	}
	got, err := buildURL("https://example.awmdm.com", meta, Args{
		"user": "alice@example.com",
	})
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}
	if !strings.Contains(got, "/devices/search?") {
		t.Errorf("URL missing path/query separator: %s", got)
	}
	if !strings.Contains(got, "user=alice%40example.com") {
		t.Errorf("URL missing escaped query: %s", got)
	}
}

func TestBuildURLPathParam(t *testing.T) {
	meta := generated.Ops[opUserGetByUuid]
	if meta.Op == "" {
		t.Fatalf("op %q not in compiled index", opUserGetByUuid)
	}
	got, err := buildURL("https://example.awmdm.com", meta, Args{
		"uuid": "f3d4e5f6-1234-5678-9abc-def012345678",
	})
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}
	if !strings.HasSuffix(got, "/f3d4e5f6-1234-5678-9abc-def012345678") {
		t.Errorf("URL did not interpolate path uuid: %s", got)
	}
}

func TestBuildURLMissingPathParamErrors(t *testing.T) {
	meta := generated.Ops[opUserGetByUuid]
	if _, err := buildURL("https://example.com", meta, Args{}); err == nil {
		t.Fatal("expected error for missing path param")
	}
}

// TestDoSetsBearerAuthAndV1Accept: mdmv1 ops must send plain
// `Accept: application/json` — WS1's edge gateway 503s on `;version=1`.
func TestDoSetsBearerAuthAndV1Accept(t *testing.T) {
	meta := generated.Ops[opDevicesSearch]
	if meta.AcceptVersion != "1" {
		t.Fatalf("test premise: %q expected to be a v1 op (AcceptVersion=1), got %q", opDevicesSearch, meta.AcceptVersion)
	}
	mux := http.NewServeMux()
	var sawAuth, sawAccept string
	mux.HandleFunc(meta.BasePath+"/devices/search", func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Devices":[],"Total":0}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{
		Source: &auth.MockTokenSource{BaseURLValue: srv.URL, TokenValue: "abc"},
		HTTP:   srv.Client(),
	}
	resp, err := c.Do(context.Background(), opDevicesSearch, Args{
		"user": "alice@example.com",
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if sawAuth != "Bearer abc" {
		t.Errorf("Authorization = %q", sawAuth)
	}
	if sawAccept != "application/json" {
		t.Errorf("v1 op Accept = %q, want plain application/json (no ;version=1)", sawAccept)
	}
}

// TestDoSetsV2Accept: v2+ ops require ;version=N in the Accept header.
// systemv2.usersv2.read is the v2 op we pin against.
func TestDoSetsV2Accept(t *testing.T) {
	meta := generated.Ops[opUserGetByUuid]
	if meta.AcceptVersion != "2" {
		t.Fatalf("test premise: %q expected to be a v2 op (AcceptVersion=2), got %q", opUserGetByUuid, meta.AcceptVersion)
	}
	mux := http.NewServeMux()
	var sawAccept string
	mux.HandleFunc(meta.BasePath+"/users/", func(w http.ResponseWriter, r *http.Request) {
		sawAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(&auth.MockTokenSource{BaseURLValue: srv.URL, TokenValue: "x"})
	c.HTTP = srv.Client()
	_, err := c.Do(context.Background(), opUserGetByUuid, Args{
		"uuid": "f3d4e5f6-1234-5678-9abc-def012345678",
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if sawAccept != "application/json;version=2" {
		t.Errorf("v2 op Accept = %q, want application/json;version=2", sawAccept)
	}
}

func TestDoSendsJSONBodyWhenDeclared(t *testing.T) {
	meta := generated.Ops[opCommandsBulkExecute]
	if !meta.HasRequestBody {
		t.Skipf("op %q in current spec doesn't declare a request body; test is informational", opCommandsBulkExecute)
	}
	mux := http.NewServeMux()
	var sawBody map[string]any
	var sawQuery string
	mux.HandleFunc(meta.BasePath+"/devices/commands/bulk", func(w http.ResponseWriter, r *http.Request) {
		sawQuery = r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&sawBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{
		Source: &auth.MockTokenSource{BaseURLValue: srv.URL, TokenValue: "x"},
		HTTP:   srv.Client(),
	}
	_, err := c.Do(context.Background(), opCommandsBulkExecute, Args{
		// Declared params go to query.
		"command":  "LockDevice",
		"searchby": "Udid",
		// Undeclared args land in JSON body.
		"BulkValues": map[string]any{
			"Value": []string{"f3d4e5f6-1234-5678-9abc-000000000001"},
		},
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !strings.Contains(sawQuery, "command=LockDevice") {
		t.Errorf("query missing command param: %q", sawQuery)
	}
	if !strings.Contains(sawQuery, "searchby=Udid") {
		t.Errorf("query missing searchby param: %q", sawQuery)
	}
	if sawBody["BulkValues"] == nil {
		t.Errorf("body BulkValues missing: %+v", sawBody)
	}
}

func TestDoSurfacesHTTPErrors(t *testing.T) {
	meta := generated.Ops[opUserGetByUuid]
	mux := http.NewServeMux()
	mux.HandleFunc(meta.BasePath+"/users/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(&auth.MockTokenSource{BaseURLValue: srv.URL, TokenValue: "x"})
	c.HTTP = srv.Client()
	resp, err := c.Do(context.Background(), opUserGetByUuid, Args{
		"uuid": "f3d4e5f6-1234-5678-9abc-def012345678",
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != 404 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestUnknownOpErrors(t *testing.T) {
	c := New(&auth.MockTokenSource{BaseURLValue: "http://nope", TokenValue: "x"})
	_, err := c.Do(context.Background(), "completely.fake.op", Args{})
	if err == nil {
		t.Fatal("expected error for unknown op")
	}
}

// TestRateLimitRetryHonorsRetryAfter: on 429, the client should sleep
// the Retry-After value (capped) and retry once. If the retry succeeds,
// the user sees a normal response and never the 429.
func TestRateLimitRetryHonorsRetryAfter(t *testing.T) {
	meta := generated.Ops[opDevicesSearch]
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc(meta.BasePath+"/devices/search", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Devices":[],"Total":0}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{
		Source: &auth.MockTokenSource{BaseURLValue: srv.URL, TokenValue: "abc"},
		HTTP:   srv.Client(),
	}
	resp, err := c.Do(context.Background(), opDevicesSearch, Args{})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200 after retry", resp.StatusCode)
	}
	if calls != 2 {
		t.Errorf("call count = %d, want 2 (one retry)", calls)
	}
}

// TestRateLimitGivesUpAfterOneRetry: persistent 429 surfaces the 429 to
// the caller; we don't loop forever.
func TestRateLimitGivesUpAfterOneRetry(t *testing.T) {
	meta := generated.Ops[opDevicesSearch]
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc(meta.BasePath+"/devices/search", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{
		Source: &auth.MockTokenSource{BaseURLValue: srv.URL, TokenValue: "x"},
		HTTP:   srv.Client(),
	}
	resp, err := c.Do(context.Background(), opDevicesSearch, Args{})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != 429 {
		t.Errorf("final status = %d, want 429", resp.StatusCode)
	}
	if calls != 2 {
		t.Errorf("call count = %d, want exactly 2 (1 + 1 retry)", calls)
	}
}
