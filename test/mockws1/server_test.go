package mockws1

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestUsersSearchByEmail(t *testing.T) {
	srv := New().Start()
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/system/users/search?email=alice@example.com")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var body struct {
		Users []User
		Total int
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Total != 1 || len(body.Users) != 1 {
		t.Fatalf("expected 1 alice, got %d (Total=%d)", len(body.Users), body.Total)
	}
	if !strings.EqualFold(body.Users[0].Email, "alice@example.com") {
		t.Errorf("got user %q", body.Users[0].Email)
	}
	if body.Users[0].Uuid == "" {
		t.Error("user record missing Uuid")
	}
}

func TestUserGetByUuid(t *testing.T) {
	srv := New().Start()
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/system/users/alice-uuid-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestUserGetByUuidNotFound(t *testing.T) {
	srv := New().Start()
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/system/users/nonexistent-uuid")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestDevicesSearchByUser(t *testing.T) {
	srv := New().Start()
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/mdm/devices/search?user=alice@example.com")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Devices []Device
		Total   int
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Total != 2 {
		t.Fatalf("alice should have 2 devices, got %d", body.Total)
	}
	for _, d := range body.Devices {
		if d.Uuid == "" {
			t.Errorf("device %d missing Uuid", d.DeviceID)
		}
	}
}

func TestDeviceGetByUuid(t *testing.T) {
	srv := New().Start()
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/mdm/devices/ip15-uuid-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var d Device
	_ = json.NewDecoder(resp.Body).Decode(&d)
	if d.Uuid != "ip15-uuid-0000-0000-000000000001" {
		t.Errorf("Uuid mismatch: %q", d.Uuid)
	}
}

func TestSingleCommand(t *testing.T) {
	s := New()
	srv := s.Start()
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/mdm/devices/ip15-uuid-0000-0000-000000000001/commands/Lock",
		"application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 202 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if len(s.Issued["ip15-uuid-0000-0000-000000000001"]) != 1 {
		t.Errorf("Issued not recorded: %v", s.Issued)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "Queued" {
		t.Errorf("status field = %v, want Queued (per dispatched-not-executed)", body["status"])
	}
}

func TestBulkCommand_PartialSuccess(t *testing.T) {
	s := New()
	srv := s.Start()
	defer srv.Close()
	body := bytes.NewBufferString(`{"BulkValues":{"Value":["ip15-uuid-0000-0000-000000000001","mbp-uuid-0000-0000-000000000001","stale-uuid-0000-0000-000000000000"]}}`)
	resp, err := http.Post(srv.URL+"/api/mdm/devices/commands/Lock", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d body %s", resp.StatusCode, out)
	}
	var parsed struct {
		Successes []map[string]any `json:"successes"`
		Failures  []map[string]any `json:"failures"`
	}
	_ = json.Unmarshal(out, &parsed)
	if len(parsed.Successes) != 2 {
		t.Errorf("Successes = %d", len(parsed.Successes))
	}
	if len(parsed.Failures) != 1 {
		t.Errorf("Failures = %d", len(parsed.Failures))
	}
	if got := parsed.Failures[0]["deviceUuid"]; got != "stale-uuid-0000-0000-000000000000" {
		t.Errorf("expected stale sentinel to fail, got %+v", got)
	}
}

func TestUnsupportedRouteIs501(t *testing.T) {
	srv := New().Start()
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/mam/apps/search")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 for unmocked op", resp.StatusCode)
	}
}

func TestOrgGroupSearch(t *testing.T) {
	srv := New().Start()
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/system/groups/search")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var body struct {
		LocationGroups []struct {
			Id   int    `json:"Id"`
			Uuid string `json:"Uuid"`
			Name string `json:"Name"`
		} `json:"LocationGroups"`
		Total int `json:"Total"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Total != 3 {
		t.Errorf("Total = %d, want 3", body.Total)
	}
	wantNames := map[string]bool{"Global": false, "EMEA": false, "EMEA-Pilot": false}
	for _, og := range body.LocationGroups {
		wantNames[og.Name] = true
		if og.Uuid == "" {
			t.Errorf("OG %q missing Uuid", og.Name)
		}
	}
	for n, seen := range wantNames {
		if !seen {
			t.Errorf("OG %q missing from response", n)
		}
	}
}
