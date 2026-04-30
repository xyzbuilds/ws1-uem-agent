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

func TestBuildURLPathAndQuery(t *testing.T) {
	meta := generated.Ops["mdmv4.devices.search"]
	got, err := buildURL("https://example.awmdm.com", meta, Args{
		"user": "alice@example.com",
		"page": 0,
	})
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}
	if !strings.Contains(got, "/api/mdm/devices/search?") {
		t.Errorf("URL missing base path or path template: %s", got)
	}
	if !strings.Contains(got, "user=alice%40example.com") {
		t.Errorf("URL missing escaped query: %s", got)
	}
}

func TestBuildURLPathParam(t *testing.T) {
	meta := generated.Ops["mdmv4.devices.get"]
	got, err := buildURL("https://example.awmdm.com", meta, Args{"id": 12345})
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}
	if !strings.HasSuffix(got, "/api/mdm/devices/12345") {
		t.Errorf("URL did not interpolate path id: %s", got)
	}
}

func TestBuildURLMissingPathParamErrors(t *testing.T) {
	meta := generated.Ops["mdmv4.devices.get"]
	if _, err := buildURL("https://example.com", meta, Args{}); err == nil {
		t.Fatal("expected error for missing path param")
	}
}

func TestDoSetsBearerAuth(t *testing.T) {
	mux := http.NewServeMux()
	var sawAuth string
	mux.HandleFunc("/api/system/users/search", func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Users":[],"Total":0}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{
		Source: &auth.MockTokenSource{BaseURLValue: srv.URL, TokenValue: "abc"},
		HTTP:   srv.Client(),
	}
	resp, err := c.Do(context.Background(), "systemv2.users.search", Args{
		"email": "alice@example.com",
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if sawAuth != "Bearer abc" {
		t.Errorf("Authorization header = %q", sawAuth)
	}
	var parsed map[string]any
	if err := resp.JSON(&parsed); err != nil {
		t.Fatalf("JSON: %v", err)
	}
}

func TestDoSendsJSONBodyWhenDeclared(t *testing.T) {
	mux := http.NewServeMux()
	var sawBody map[string]any
	mux.HandleFunc("/api/mdm/devices/commands/bulk", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sawBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"successes":[],"failures":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{
		Source: &auth.MockTokenSource{BaseURLValue: srv.URL, TokenValue: "x"},
		HTTP:   srv.Client(),
	}
	_, err := c.Do(context.Background(), "mdmv4.devices.bulkcommand", Args{
		"command":    "Lock",
		"device_ids": []int{12345, 12346},
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if sawBody["command"] != "Lock" {
		t.Errorf("body command = %v", sawBody["command"])
	}
	if ids, _ := sawBody["device_ids"].([]any); len(ids) != 2 {
		t.Errorf("body device_ids = %v", sawBody["device_ids"])
	}
}

func TestDoSurfacesHTTPErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/mdm/devices/99999", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(&auth.MockTokenSource{BaseURLValue: srv.URL, TokenValue: "x"})
	c.HTTP = srv.Client()
	resp, err := c.Do(context.Background(), "mdmv4.devices.get", Args{"id": 99999})
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
