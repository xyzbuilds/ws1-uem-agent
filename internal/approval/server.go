package approval

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// Pending represents an approval request that has been registered with a
// running HTTP server but is not yet decided. Production callers use
// Run() (which combines Begin + browser-launch + Wait); tests use Begin +
// direct HTTP drives so they don't have to spawn a real browser.
//
// One Pending instance per approval request; not reused.
type Pending struct {
	URL       string
	RequestID string

	server *server
}

// server is the unexported HTTP plumbing. The exported surface is Pending.
type server struct {
	req       Request
	requestID string

	listener net.Listener
	httpSrv  *http.Server
	port     int

	mu       sync.Mutex
	decided  bool
	decision Outcome

	done chan struct{} // closed when the user decides
}

// Begin binds 127.0.0.1 on a random ephemeral port, starts the HTTP server
// in a goroutine, and registers the approval routes. The returned Pending
// holds the URL the user (or test) must drive to approve/deny.
//
// Begin does not open a browser. Use Run for that, or call openBrowser
// yourself after Begin.
func Begin(req Request) (*Pending, error) {
	if len(req.Targets) == 0 {
		return nil, errors.New("approval: at least one target required")
	}
	requestID := newRequestID()

	ln, port, err := listenRandomPort()
	if err != nil {
		return nil, fmt.Errorf("approval: bind 127.0.0.1: %w", err)
	}
	s := &server{
		req:       req,
		requestID: requestID,
		listener:  ln,
		port:      port,
		done:      make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/r/"+requestID, s.handleApprovalPage)
	mux.HandleFunc("/r/"+requestID+"/approve", s.handleApprove)
	mux.HandleFunc("/r/"+requestID+"/deny", s.handleDeny)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	s.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = s.httpSrv.Serve(ln)
	}()

	return &Pending{
		URL:       "http://127.0.0.1:" + strconv.Itoa(port) + "/r/" + requestID,
		RequestID: requestID,
		server:    s,
	}, nil
}

// Wait blocks until the user clicks approve/deny, the timeout fires, or
// ctx is cancelled. Tears down the HTTP server before returning. timeout
// of 0 falls back to DefaultTimeout.
func (p *Pending) Wait(ctx context.Context, timeout time.Duration) (*Result, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var outcome Outcome
	select {
	case <-p.server.done:
		p.server.mu.Lock()
		outcome = p.server.decision
		p.server.mu.Unlock()
	case <-timer.C:
		outcome = OutcomeTimeout
	case <-ctx.Done():
		outcome = OutcomeAborted
	}
	p.shutdown()
	return &Result{
		RequestID:  p.RequestID,
		Outcome:    outcome,
		Approved:   outcome == OutcomeApproved,
		ApprovedAt: time.Now(),
		ArgsHash:   argsHash(p.server.req.Args),
		Targets:    p.server.req.Targets,
	}, nil
}

// Cancel tears down the server without waiting for a decision. Test-only;
// production callers should always go through Wait so the result is observed.
func (p *Pending) Cancel() {
	p.shutdown()
}

func (p *Pending) shutdown() {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.server.httpSrv.Shutdown(shutdownCtx)
}

// Run is the production-path convenience wrapper. It begins the request,
// opens the user's browser, prints fallback URL to stderr, and Waits.
func Run(ctx context.Context, req Request) (*Result, error) {
	p, err := Begin(req)
	if err != nil {
		return nil, err
	}
	if !TestSuppressBrowser() {
		_ = openBrowser(p.URL)
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	fmt.Fprintf(os.Stderr, "Approval required for %s (%d target%s).\n",
		req.OperationDesc, len(req.Targets), pluralS(len(req.Targets)))
	fmt.Fprintf(os.Stderr, "Opened your browser for approval. If it didn't open, visit:\n  %s\n", p.URL)
	fmt.Fprintf(os.Stderr, "Waiting up to %s ...\n", timeout)
	return p.Wait(ctx, timeout)
}

// listenRandomPort binds 127.0.0.1:0 and returns the listener + chosen
// port. We let the OS pick rather than retrying random integers.
func listenRandomPort() (net.Listener, int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	return ln, ln.Addr().(*net.TCPAddr).Port, nil
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (s *server) signal(o Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.decided {
		return
	}
	s.decided = true
	s.decision = o
	close(s.done)
}

func (s *server) handleApprovalPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	s.mu.Lock()
	decided := s.decided
	decision := s.decision
	s.mu.Unlock()
	if decided {
		_ = afterDecisionTpl.Execute(w, decision.String())
		return
	}
	data := map[string]any{
		"Operation":   s.req.Operation,
		"Description": s.req.OperationDesc,
		"Class":       s.req.Class,
		"Rev":         s.req.Reversibility,
		"Profile":     s.req.Profile,
		"Tenant":      s.req.Tenant,
		"Targets":     s.req.Targets,
		"Path":        "/r/" + s.requestID,
	}
	_ = approvalPageTpl.Execute(w, data)
}

func (s *server) handleApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.signal(OutcomeApproved)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = afterDecisionTpl.Execute(w, "approved")
}

func (s *server) handleDeny(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.signal(OutcomeDenied)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = afterDecisionTpl.Execute(w, "denied")
}

func openBrowser(url string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("cmd", "/c", "start", "", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	return c.Start()
}

// TestSuppressBrowser is read by Run to skip the browser-launch when
// WS1_NO_BROWSER is set. Used by tests and by the demo runner that wants
// the user to copy the URL manually.
func TestSuppressBrowser() bool {
	return os.Getenv("WS1_NO_BROWSER") != ""
}

// --- HTML templates ---------------------------------------------------------

var approvalPageTpl = template.Must(template.New("approval").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>ws1 approval — {{.Operation}}</title>
<style>
  :root { --fg:#1c1c1e; --bg:#fafaf7; --warn:#b53; --ok:#198754; --no:#b02a2a; --line:#e5e1d8; }
  html,body { margin:0; padding:0; background:var(--bg); color:var(--fg);
              font-family: -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; }
  main { max-width:680px; margin: 48px auto; padding: 0 24px; }
  h1 { margin: 0 0 4px 0; font-size: 22px; }
  h1 .op { font-family: ui-monospace, SFMono-Regular, monospace; font-size: 18px; color: #555; font-weight: 500; }
  .badge { display:inline-block; padding:2px 8px; border-radius:4px; font-size:12px;
           font-weight:600; letter-spacing:.04em; text-transform:uppercase; }
  .badge.destructive { background:#fde2e2; color:#7a1d1d; }
  .badge.write       { background:#fff1d6; color:#7a4f00; }
  .badge.read        { background:#e3f1e0; color:#1d5a1d; }
  table { border-collapse: collapse; width: 100%; margin-top: 16px; }
  td { padding: 10px 0; border-bottom: 1px solid var(--line); vertical-align: top; }
  td.k { width: 180px; color: #777; font-size: 14px; }
  td.v { font-family: ui-monospace, SFMono-Regular, monospace; }
  .targets { margin-top: 12px; padding: 12px; background:#fff; border:1px solid var(--line); border-radius: 6px; }
  .target { padding: 8px 0; border-bottom: 1px solid var(--line); }
  .target:last-child { border-bottom: 0; }
  .label { font-weight:600; }
  .snap  { display: grid; grid-template-columns: 140px 1fr; gap: 4px 12px; margin-top:4px; font-family: ui-monospace, monospace; font-size: 13px; color:#444; }
  .actions { display:flex; gap:12px; margin-top:24px; }
  button { font-size:16px; padding: 10px 18px; border-radius: 6px; cursor: pointer; border: none; }
  button.approve { background: var(--ok); color: white; }
  button.deny    { background: var(--no); color: white; }
  small { color:#777; }
</style>
</head>
<body>
<main>
  <h1>{{.Description}} <span class="op">{{.Operation}}</span></h1>
  <p>
    <span class="badge {{.Class}}">{{.Class}}</span>
    Reversibility: <strong>{{.Rev}}</strong>
  </p>
  <table>
    <tr><td class="k">Profile</td><td class="v">{{.Profile}}</td></tr>
    <tr><td class="k">Org Group</td><td class="v">{{.Tenant}}</td></tr>
    <tr><td class="k">Targets</td><td class="v">{{len .Targets}}</td></tr>
  </table>
  <div class="targets">
  {{range .Targets}}
    <div class="target">
      <div class="label">{{.DisplayLabel}}</div>
      <div class="snap">
        {{range $k,$v := .Snapshot}}
          <div>{{$k}}</div><div>{{$v}}</div>
        {{end}}
      </div>
    </div>
  {{end}}
  </div>
  <div class="actions">
    <form method="POST" action="{{.Path}}/approve"><button class="approve" type="submit">Approve</button></form>
    <form method="POST" action="{{.Path}}/deny"><button class="deny" type="submit">Deny</button></form>
  </div>
  <p><small>This page is served by the ws1 CLI on 127.0.0.1 for the lifetime of one invocation. The agent that called ws1 cannot intercept the click.</small></p>
</main>
</body>
</html>`))

var afterDecisionTpl = template.Must(template.New("after").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>ws1 — {{.}}</title>
<style>
  body { font-family: -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;
         padding: 48px; text-align: center; }
  h1 { font-size: 28px; }
  .approved { color: #198754; }
  .denied   { color: #b02a2a; }
  .timeout  { color: #b53; }
  .aborted  { color: #777; }
</style></head>
<body>
<h1 class="{{.}}">{{.}}</h1>
<p>You can close this tab. Return to your terminal.</p>
</body></html>`))
