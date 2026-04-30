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
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Users []User
		Total int
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if body.Total != 1 || len(body.Users) != 1 {
		t.Fatalf("expected 1 alice, got %d (Total=%d)", len(body.Users), body.Total)
	}
	if !strings.EqualFold(body.Users[0].Email, "alice@example.com") {
		t.Errorf("got user %q", body.Users[0].Email)
	}
}

func TestUsersSearchAmbiguous(t *testing.T) {
	srv := New().Start()
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/system/users/search?username=al")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Total int
		Users []User
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if body.Total < 2 {
		t.Errorf("expected >=2 matches for username=al, got %d", body.Total)
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
	json.NewDecoder(resp.Body).Decode(&body)
	if body.Total != 2 {
		t.Fatalf("alice should have 2 devices, got %d", body.Total)
	}
}

func TestLockEndpointQueuesCommand(t *testing.T) {
	s := New()
	srv := s.Start()
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/mdm/devices/12345/commands/lock", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(s.Issued[12345]) != 1 {
		t.Errorf("Issued[12345] = %v", s.Issued[12345])
	}
}

func TestBulkPartialSuccess(t *testing.T) {
	s := New()
	srv := s.Start()
	defer srv.Close()
	body := bytes.NewBufferString(`{"command":"Lock","device_ids":[12345,12346,12399]}`)
	resp, err := http.Post(srv.URL+"/api/mdm/devices/commands/bulk", "application/json", body)
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
	json.Unmarshal(out, &parsed)
	if len(parsed.Successes) != 2 {
		t.Errorf("Successes = %d", len(parsed.Successes))
	}
	if len(parsed.Failures) != 1 {
		t.Errorf("Failures = %d", len(parsed.Failures))
	}
	if got := parsed.Failures[0]["DeviceID"]; got != float64(12399) {
		t.Errorf("expected 12399 to fail, got %+v", got)
	}
}

func TestUserNotFound(t *testing.T) {
	srv := New().Start()
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/system/users/99999")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}
