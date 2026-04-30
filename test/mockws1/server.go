// Package mockws1 is a minimal stand-in for a WS1 UEM tenant. It is used
// by integration tests AND by the alice-lock end-to-end demo so the full
// CLI -> approval -> API -> audit loop can run without a real tenant.
//
// The fixture data is hard-coded (alice@example.com with two devices, plus
// an ambiguous user case) and matches what the demo script expects. Add a
// new fixture by extending Server.users / Server.devices.
package mockws1

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// User mirrors the systemv2.User schema we ship in spec/.
type User struct {
	UserID              int    `json:"UserID"`
	Username            string `json:"Username"`
	Email               string `json:"Email"`
	FirstName           string `json:"FirstName"`
	LastName            string `json:"LastName"`
	OrganizationGroupID int    `json:"OrganizationGroupID"`
}

// Device mirrors the mdmv4.Device schema.
type Device struct {
	DeviceID              int    `json:"DeviceID"`
	SerialNumber          string `json:"SerialNumber"`
	FriendlyName          string `json:"FriendlyName"`
	EnrollmentUser        string `json:"EnrollmentUser"`
	EnrollmentStatus      string `json:"EnrollmentStatus"`
	Platform              string `json:"Platform"`
	OrganizationGroupID   int    `json:"OrganizationGroupID"`
	OrganizationGroupName string `json:"OrganizationGroupName"`
}

// Server is the concrete mock; thread-safe.
type Server struct {
	mu      sync.RWMutex
	users   []User
	devices []Device

	// Issued command UUIDs, keyed by device id, so tests can verify the
	// lock/wipe call actually landed.
	Issued map[int][]string
}

// New returns a populated Server with the canonical demo fixtures.
//
// Alice (UserID=10001) owns DeviceID=12345 (iPhone) and DeviceID=12346
// (MacBook). UserID=10002 is "alex" — present so a search for "alic" is
// ambiguous and the IDENTIFIER_AMBIGUOUS path is exercisable.
func New() *Server {
	return &Server{
		users: []User{
			{UserID: 10001, Username: "alice", Email: "alice@example.com",
				FirstName: "Alice", LastName: "Anderson", OrganizationGroupID: 12345},
			{UserID: 10002, Username: "alex", Email: "alex@example.com",
				FirstName: "Alex", LastName: "Allen", OrganizationGroupID: 12345},
			{UserID: 10003, Username: "bob", Email: "bob@example.com",
				FirstName: "Bob", LastName: "Brown", OrganizationGroupID: 12345},
		},
		devices: []Device{
			{DeviceID: 12345, SerialNumber: "ABC123", FriendlyName: "Alice's iPhone 15",
				EnrollmentUser: "alice@example.com", EnrollmentStatus: "Enrolled",
				Platform: "Apple", OrganizationGroupID: 12345, OrganizationGroupName: "EMEA-Pilot"},
			{DeviceID: 12346, SerialNumber: "DEF456", FriendlyName: "Alice's MacBook Pro",
				EnrollmentUser: "alice@example.com", EnrollmentStatus: "Enrolled",
				Platform: "Apple", OrganizationGroupID: 12345, OrganizationGroupName: "EMEA-Pilot"},
			{DeviceID: 12350, SerialNumber: "GHI789", FriendlyName: "Bob's Pixel",
				EnrollmentUser: "bob@example.com", EnrollmentStatus: "Enrolled",
				Platform: "Android", OrganizationGroupID: 12345, OrganizationGroupName: "EMEA-Pilot"},
		},
		Issued: map[int][]string{},
	}
}

// HTTPHandler returns an http.Handler that implements every route the CLI
// uses. Routes are registered in a single mux so missing route bugs surface
// as 404s rather than goroutine panics.
func (s *Server) HTTPHandler() http.Handler {
	mux := http.NewServeMux()

	// systemv2
	mux.HandleFunc("/api/system/users/search", s.handleUsersSearch)
	mux.HandleFunc("/api/system/users/", s.handleUsersGet) // trailing slash for /{id}

	// mdmv4
	mux.HandleFunc("/api/mdm/devices/search", s.handleDevicesSearch)
	mux.HandleFunc("/api/mdm/devices/", s.handleDevicesPath) // /{id} or /{id}/commands/lock|wipe
	mux.HandleFunc("/api/mdm/devices/commands/bulk", s.handleBulk)

	// dev convenience
	mux.HandleFunc("/", s.handleIndex)

	return logged(mux)
}

// Start spins up an httptest server bound to a random localhost port and
// returns it. Callers Close() it on shutdown.
func (s *Server) Start() *httptest.Server {
	return httptest.NewServer(s.HTTPHandler())
}

// --- handlers ---------------------------------------------------------------

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = fmt.Fprintln(w, "ws1 mock tenant")
}

func (s *Server) handleUsersSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	username := q.Get("username")
	email := q.Get("email")
	first := q.Get("firstname")
	last := q.Get("lastname")

	s.mu.RLock()
	defer s.mu.RUnlock()
	var matched []User
	for _, u := range s.users {
		if username != "" && !contains(u.Username, username) {
			continue
		}
		if email != "" && !contains(u.Email, email) {
			continue
		}
		if first != "" && !contains(u.FirstName, first) {
			continue
		}
		if last != "" && !contains(u.LastName, last) {
			continue
		}
		matched = append(matched, u)
	}
	page, pageSize := paging(q)
	writeJSON(w, http.StatusOK, map[string]any{
		"Users":    paginate(matched, page, pageSize),
		"Page":     page,
		"PageSize": pageSize,
		"Total":    len(matched),
	})
}

func (s *Server) handleUsersGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/system/users/"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.UserID == id {
			writeJSON(w, http.StatusOK, u)
			return
		}
	}
	http.Error(w, "user not found", http.StatusNotFound)
}

func (s *Server) handleDevicesSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	user := q.Get("user")
	platform := q.Get("platform")

	s.mu.RLock()
	defer s.mu.RUnlock()
	var matched []Device
	for _, d := range s.devices {
		if user != "" && !equalFold(d.EnrollmentUser, user) {
			continue
		}
		if platform != "" && !equalFold(d.Platform, platform) {
			continue
		}
		matched = append(matched, d)
	}
	page, pageSize := paging(q)
	writeJSON(w, http.StatusOK, map[string]any{
		"Devices":  paginate(matched, page, pageSize),
		"Page":     page,
		"PageSize": pageSize,
		"Total":    len(matched),
	})
}

func (s *Server) handleDevicesPath(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/mdm/devices/")
	parts := strings.Split(rest, "/")
	idStr := parts[0]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if len(parts) == 1 {
		s.serveDevice(w, r, id)
		return
	}
	// /{id}/commands/lock or /commands/wipe
	if len(parts) >= 3 && parts[1] == "commands" {
		switch parts[2] {
		case "lock":
			s.serveLock(w, r, id)
			return
		case "wipe":
			s.serveWipe(w, r, id)
			return
		}
	}
	http.Error(w, "unknown route", http.StatusNotFound)
}

func (s *Server) serveDevice(w http.ResponseWriter, r *http.Request, id int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.devices {
		if d.DeviceID == id {
			writeJSON(w, http.StatusOK, d)
			return
		}
	}
	http.Error(w, "device not found", http.StatusNotFound)
}

func (s *Server) serveLock(w http.ResponseWriter, r *http.Request, id int) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	uuid := s.issueCommand(id, "Lock")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"DeviceID":     id,
		"command_uuid": uuid,
		"status":       "Queued",
	})
}

func (s *Server) serveWipe(w http.ResponseWriter, r *http.Request, id int) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	uuid := s.issueCommand(id, "Wipe")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id":   "job_" + uuid,
		"status":   "Pending",
		"poll_url": "ws1://jobs/job_" + uuid,
	})
}

func (s *Server) handleBulk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Command   string `json:"command"`
		DeviceIDs []int  `json:"device_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	successes := []map[string]any{}
	failures := []map[string]any{}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range body.DeviceIDs {
		// Drift simulation: a sentinel ID (12399) always reports stale.
		if id == 12399 {
			failures = append(failures, map[string]any{
				"DeviceID": id,
				"error": map[string]any{
					"code":    "STALE_RESOURCE",
					"message": "Device unenrolled since lookup.",
				},
			})
			continue
		}
		found := false
		for _, d := range s.devices {
			if d.DeviceID == id {
				found = true
				break
			}
		}
		if !found {
			failures = append(failures, map[string]any{
				"DeviceID": id,
				"error":    map[string]any{"code": "IDENTIFIER_NOT_FOUND", "message": fmt.Sprintf("no device %d", id)},
			})
			continue
		}
		uuid := s.issueCommandLocked(id, body.Command)
		successes = append(successes, map[string]any{
			"DeviceID":     id,
			"command_uuid": uuid,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"successes": successes,
		"failures":  failures,
	})
}

func (s *Server) issueCommand(id int, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issueCommandLocked(id, name)
}

func (s *Server) issueCommandLocked(id int, name string) string {
	uuid := fmt.Sprintf("cmd_%s_%d_%d", strings.ToLower(name), id, time.Now().UnixNano()%1_000_000)
	s.Issued[id] = append(s.Issued[id], uuid)
	return uuid
}

// --- helpers ----------------------------------------------------------------

func paging(q map[string][]string) (int, int) {
	page, _ := strconv.Atoi(get(q, "page"))
	pageSize, err := strconv.Atoi(get(q, "pagesize"))
	if err != nil || pageSize == 0 {
		pageSize = 100
	}
	return page, pageSize
}

func get(q map[string][]string, k string) string {
	if v, ok := q[k]; ok && len(v) > 0 {
		return v[0]
	}
	return ""
}

func paginate[T any](items []T, page, pageSize int) []T {
	start := page * pageSize
	if start >= len(items) {
		return []T{}
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func equalFold(a, b string) bool { return strings.EqualFold(a, b) }

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// logged is a tiny middleware that prints request lines to stderr for the
// demo's stdout-only ws1 envelope contract; demos pipe ws1 stdout to jq
// while the mock is logging to its own stderr.
func logged(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// kept as a placeholder hook; intentionally silent so test runs
		// don't pollute stderr. cmd/mockws1 wraps with its own verbose
		// logger when run interactively.
		h.ServeHTTP(w, r)
	})
}
