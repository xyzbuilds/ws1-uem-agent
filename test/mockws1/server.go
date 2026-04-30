// Package mockws1 is a minimal stand-in for a WS1 UEM tenant. It is used
// by integration tests AND by the alice-lock end-to-end demo so the full
// CLI -> approval -> API -> audit loop can run without a real tenant.
//
// Routes mirror the real spec shapes (synced from as1831.awmdm.com) for
// the ops the demo + integration tests exercise:
//
//	GET  /api/system/users/search                                    systemv1.user.search
//	GET  /api/system/users/{uuid}                                    systemv2.usersv2.read
//	GET  /api/mdm/devices/search                                     mdmv1.devices.search
//	GET  /api/mdm/devices/{uuid}                                     mdmv2.devicesv2.getbyuuid
//	POST /api/mdm/devices/{deviceUuid}/commands/{commandName}        mdmv2.commandsv2.execute
//	POST /api/mdm/devices/commands/{commandName}                     mdmv2.commandsv2.bulkexecute
//
// Other ops (the other ~970) return 501 Not Implemented; they're
// reachable against a real tenant via `ws1 profile add operator ...`.
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

// User mirrors the v2 user record shape. Both UserID (legacy) and Uuid
// are populated so callers can choose either identifier.
type User struct {
	UserID      int    `json:"UserID"`
	Uuid        string `json:"Uuid"`
	Username    string `json:"userName"`
	Email       string `json:"emailAddress"`
	FirstName   string `json:"firstName"`
	LastName    string `json:"lastName"`
	DisplayName string `json:"displayName"`
}

// OrgGroup mirrors the org-group record shape the wizard expects from
// systemv2.organizationgroups.organizationgroupsearch (and the v1
// variant). Both Id and Uuid are populated.
type OrgGroup struct {
	Id   int    `json:"Id"`
	Uuid string `json:"Uuid"`
	Name string `json:"Name"`
}

// Device mirrors the device record shape; both flavors of identifier.
type Device struct {
	DeviceID              int    `json:"DeviceID"`
	Uuid                  string `json:"Uuid"`
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
	mu        sync.RWMutex
	users     []User
	devices   []Device
	orgGroups []OrgGroup

	// Issued maps device UUIDs to dispatched command UUIDs so tests can
	// verify a Lock/Wipe etc. actually landed at the API.
	Issued map[string][]string
}

// New returns a populated Server with the canonical demo fixtures.
//
// Alice (UserID=10001, Uuid=alice-uuid-...) owns DeviceID=12345
// (iPhone, deviceUuid=ip15-uuid) and DeviceID=12346 (MacBook, mbp-uuid).
// Bob owns one Pixel. Plus a sentinel "stale" device whose snapshot
// drifts at execute time so tests can exercise STALE_RESOURCE.
func New() *Server {
	return &Server{
		users: []User{
			{UserID: 10001, Uuid: "alice-uuid-0000-0000-000000000001",
				Username: "alice", Email: "alice@example.com",
				FirstName: "Alice", LastName: "Anderson", DisplayName: "Alice Anderson"},
			{UserID: 10002, Uuid: "alex-uuid-0000-0000-000000000001",
				Username: "alex", Email: "alex@example.com",
				FirstName: "Alex", LastName: "Allen", DisplayName: "Alex Allen"},
			{UserID: 10003, Uuid: "bob-uuid-0000-0000-000000000001",
				Username: "bob", Email: "bob@example.com",
				FirstName: "Bob", LastName: "Brown", DisplayName: "Bob Brown"},
		},
		devices: []Device{
			{DeviceID: 12345, Uuid: "ip15-uuid-0000-0000-000000000001",
				SerialNumber: "ABC123", FriendlyName: "Alice's iPhone 15",
				EnrollmentUser: "alice@example.com", EnrollmentStatus: "Enrolled",
				Platform: "Apple", OrganizationGroupID: 12345, OrganizationGroupName: "EMEA-Pilot"},
			{DeviceID: 12346, Uuid: "mbp-uuid-0000-0000-000000000001",
				SerialNumber: "DEF456", FriendlyName: "Alice's MacBook Pro",
				EnrollmentUser: "alice@example.com", EnrollmentStatus: "Enrolled",
				Platform: "Apple", OrganizationGroupID: 12345, OrganizationGroupName: "EMEA-Pilot"},
			{DeviceID: 12350, Uuid: "pixel-uuid-0000-0000-000000000001",
				SerialNumber: "GHI789", FriendlyName: "Bob's Pixel",
				EnrollmentUser: "bob@example.com", EnrollmentStatus: "Enrolled",
				Platform: "Android", OrganizationGroupID: 12345, OrganizationGroupName: "EMEA-Pilot"},
		},
		orgGroups: []OrgGroup{
			{Id: 1, Uuid: "70a00000-0000-0000-0000-000000000001", Name: "Global"},
			{Id: 2042, Uuid: "8b300000-0000-0000-0000-000000000001", Name: "EMEA"},
			{Id: 4067, Uuid: "c9100000-0000-0000-0000-000000000001", Name: "EMEA-Pilot"},
		},
		Issued: map[string][]string{},
	}
}

// HTTPHandler returns an http.Handler covering the ops the demo +
// integration tests need. Everything else returns 501.
func (s *Server) HTTPHandler() http.Handler {
	mux := http.NewServeMux()

	// systemv1 / systemv2: users
	mux.HandleFunc("/api/system/users/search", s.handleUsersSearch)
	mux.HandleFunc("/api/system/users/", s.handleUsersByUuidOrID)

	// systemv1 / systemv2: organization groups
	mux.HandleFunc("/api/system/groups/search", s.handleOrgGroupSearch)

	// mdmv1: devices search
	mux.HandleFunc("/api/mdm/devices/search", s.handleDevicesSearch)

	// Devices/commands tree — matched in order. The mux dispatches
	// /api/mdm/devices/ to handleDevicesPath, which then routes to
	// commands/{commandName} or by-uuid.
	mux.HandleFunc("/api/mdm/devices/commands/", s.handleBulkCommand)
	mux.HandleFunc("/api/mdm/devices/", s.handleDeviceTree)

	mux.HandleFunc("/", s.handleNotImplemented)
	return mux
}

func (s *Server) Start() *httptest.Server {
	return httptest.NewServer(s.HTTPHandler())
}

// --- handlers ---------------------------------------------------------------

func (s *Server) handleNotImplemented(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintln(w, "ws1 mock tenant — limited route surface; see test/mockws1/server.go")
		return
	}
	http.Error(w, fmt.Sprintf(`{"error":"NOT_IMPLEMENTED","path":%q}`, r.URL.Path), http.StatusNotImplemented)
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
	searchtext := q.Get("searchtext") // v2-style

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
		if searchtext != "" {
			// v2 searchtext is a free-text fuzzy match against multiple fields.
			hit := contains(u.Username, searchtext) ||
				contains(u.Email, searchtext) ||
				contains(u.FirstName, searchtext) ||
				contains(u.LastName, searchtext) ||
				contains(u.DisplayName, searchtext)
			if !hit {
				continue
			}
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

// handleUsersByUuidOrID matches both
//
//	GET /api/system/users/{uuid}        — systemv2.usersv2.read
//	GET /api/system/users/{id}          — systemv1 by integer
//
// We dispatch by trying integer-parse first; UUID otherwise.
func (s *Server) handleUsersByUuidOrID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/system/users/")
	if rest == "" || strings.Contains(rest, "/") {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id, err := strconv.Atoi(rest); err == nil {
		for _, u := range s.users {
			if u.UserID == id {
				writeJSON(w, http.StatusOK, u)
				return
			}
		}
	}
	for _, u := range s.users {
		if u.Uuid == rest {
			writeJSON(w, http.StatusOK, u)
			return
		}
	}
	http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
}

// handleOrgGroupSearch serves both
//
//	GET /api/system/groups/search   — systemv2.organizationgroups.organizationgroupsearch
//	GET /api/system/groups/search   — systemv1.organizationgroups.locationgroupsearch
//
// (same path, version negotiated via Accept header). The mock doesn't
// enforce the Accept version; it returns the same body shape for both.
func (s *Server) handleOrgGroupSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	name := q.Get("name")

	s.mu.RLock()
	defer s.mu.RUnlock()
	matched := []OrgGroup{}
	for _, og := range s.orgGroups {
		if name != "" && !contains(og.Name, name) {
			continue
		}
		matched = append(matched, og)
	}
	page, pageSize := paging(q)
	writeJSON(w, http.StatusOK, map[string]any{
		"LocationGroups": paginate(matched, page, pageSize),
		"Page":           page,
		"PageSize":       pageSize,
		"Total":          len(matched),
	})
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

// handleDeviceTree dispatches paths under /api/mdm/devices/{...} that
// aren't /search or /commands/*. Routes:
//
//	GET  /api/mdm/devices/{uuid}                                 — getbyuuid
//	GET  /api/mdm/devices/{id}                                   — get-by-int (legacy)
//	POST /api/mdm/devices/{deviceUuid}/commands/{commandName}    — single
func (s *Server) handleDeviceTree(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/mdm/devices/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	idOrUuid := parts[0]

	if len(parts) == 1 {
		s.serveDeviceByID(w, r, idOrUuid)
		return
	}
	if len(parts) >= 3 && parts[1] == "commands" {
		s.serveSingleCommand(w, r, idOrUuid, parts[2])
		return
	}
	http.Error(w, fmt.Sprintf(`{"error":"unknown device sub-route %q"}`, rest), http.StatusNotFound)
}

func (s *Server) serveDeviceByID(w http.ResponseWriter, r *http.Request, idOrUuid string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id, err := strconv.Atoi(idOrUuid); err == nil {
		for _, d := range s.devices {
			if d.DeviceID == id {
				writeJSON(w, http.StatusOK, d)
				return
			}
		}
	}
	for _, d := range s.devices {
		if d.Uuid == idOrUuid {
			writeJSON(w, http.StatusOK, d)
			return
		}
	}
	http.Error(w, `{"error":"device not found"}`, http.StatusNotFound)
}

func (s *Server) serveSingleCommand(w http.ResponseWriter, r *http.Request, deviceUuid, command string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Verify the device exists.
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, d := range s.devices {
		if d.Uuid == deviceUuid {
			found = true
			break
		}
		if id, err := strconv.Atoi(deviceUuid); err == nil && d.DeviceID == id {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, `{"error":"device not found"}`, http.StatusNotFound)
		return
	}
	uuid := s.issueCommandLocked(deviceUuid, command)
	// Per UEM async-nature: the API confirms dispatch (Queued), not
	// completion. Status reflects the queue side, never the device.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"deviceUuid":   deviceUuid,
		"commandName":  command,
		"command_uuid": uuid,
		"status":       "Queued",
	})
}

func (s *Server) handleBulkCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/mdm/devices/commands/")
	command := rest
	if command == "" {
		http.Error(w, "command required in path", http.StatusBadRequest)
		return
	}

	var body struct {
		// Real WS1 v2 bulk shape: BulkValues.Value is the list of device
		// UUIDs (or alternate IDs). Permissive about exact key spelling.
		BulkValues struct {
			Value []string `json:"Value"`
		} `json:"BulkValues"`
		// Tolerate a flat shape too — agents that bypass the wrapper.
		DeviceUuids []string `json:"device_uuids"`
		DeviceIds   []int    `json:"device_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	targets := append([]string{}, body.BulkValues.Value...)
	targets = append(targets, body.DeviceUuids...)
	for _, id := range body.DeviceIds {
		targets = append(targets, strconv.Itoa(id))
	}

	successes := []map[string]any{}
	failures := []map[string]any{}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range targets {
		// Sentinel: "stale-uuid" always reports STALE_RESOURCE so the
		// freshness path is exercisable in tests.
		if t == "stale-uuid-0000-0000-000000000000" {
			failures = append(failures, map[string]any{
				"deviceUuid": t,
				"error": map[string]any{
					"code":    "STALE_RESOURCE",
					"message": "Device unenrolled since lookup.",
				},
			})
			continue
		}
		found := false
		for _, d := range s.devices {
			if d.Uuid == t {
				found = true
				break
			}
			if id, err := strconv.Atoi(t); err == nil && d.DeviceID == id {
				found = true
				break
			}
		}
		if !found {
			failures = append(failures, map[string]any{
				"deviceUuid": t,
				"error":      map[string]any{"code": "IDENTIFIER_NOT_FOUND", "message": fmt.Sprintf("no device %s", t)},
			})
			continue
		}
		uuid := s.issueCommandLocked(t, command)
		successes = append(successes, map[string]any{
			"deviceUuid":   t,
			"commandName":  command,
			"command_uuid": uuid,
			"status":       "Queued",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"successes": successes,
		"failures":  failures,
	})
}

func (s *Server) issueCommandLocked(deviceID, name string) string {
	uuid := fmt.Sprintf("cmd_%s_%s_%d", strings.ToLower(name), deviceID, time.Now().UnixNano()%1_000_000)
	s.Issued[deviceID] = append(s.Issued[deviceID], uuid)
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
