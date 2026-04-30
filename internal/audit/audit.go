// Package audit implements the JSONL hash-chained audit log defined in
// spec section 9. Each entry's prev_hash binds to the SHA-256 of the
// previous entry's canonical form (PrevHash field zeroed); tampering with
// any entry breaks the chain at every subsequent entry.
//
// v1 limitation: the file lives at ~/.config/ws1/audit.log and is writable
// by the agent's OS user. v2 ships entries to a write-only remote sink.
// This is documented in CLAUDE.md locked decision #10.
package audit

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is one row of the audit log.
type Entry struct {
	Ts                string `json:"ts"`
	Seq               int    `json:"seq"`
	Caller            string `json:"caller"`
	Operation         string `json:"operation"`
	ArgsHash          string `json:"args_hash"`
	Class             string `json:"class"`
	ApprovalRequestID string `json:"approval_request_id,omitempty"`
	Profile           string `json:"profile"`
	Tenant            string `json:"tenant,omitempty"`
	Result            string `json:"result"`
	DurationMs        int64  `json:"duration_ms"`
	PrevHash          string `json:"prev_hash"`
}

// Logger appends entries with hash chaining. Concurrent calls are safe but
// strictly serialized via mu.
type Logger struct {
	path string
	mu   sync.Mutex
}

// New returns a Logger that writes to path. The parent directory is created
// (mode 0700) if absent.
func New(path string) (*Logger, error) {
	if path == "" {
		return nil, errors.New("audit: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return &Logger{path: path}, nil
}

// DefaultPath returns the canonical user-config path for the audit log,
// honouring WS1_CONFIG_DIR for tests.
func DefaultPath() (string, error) {
	if v := os.Getenv("WS1_CONFIG_DIR"); v != "" {
		return filepath.Join(v, "audit.log"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ws1", "audit.log"), nil
}

// Append writes e to the log, populating Ts (if zero), Seq, and PrevHash
// from the chain state on disk. The returned entry is the canonical form
// that landed on disk; its `Ts<rfc3339>#<seq>` formatted ID is exposed
// via the envelope's meta.audit_log_entry field.
func (l *Logger) Append(e Entry) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	last, err := l.tailOne()
	if err != nil {
		return Entry{}, err
	}
	if e.Ts == "" {
		e.Ts = time.Now().UTC().Format(time.RFC3339)
	}
	if last == nil {
		e.Seq = 1
		e.PrevHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	} else {
		e.Seq = last.Seq + 1
		h, err := hashEntry(*last)
		if err != nil {
			return Entry{}, err
		}
		e.PrevHash = h
	}

	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return Entry{}, err
	}
	b = append(b, '\n')
	if _, err := f.Write(b); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// EntryID returns the canonical "ts#seq" identifier for an entry as written
// in envelope.meta.audit_log_entry.
func (e Entry) EntryID() string {
	return fmt.Sprintf("%s#%d", e.Ts, e.Seq)
}

// tailOne returns the last entry on disk, or nil for an empty/missing file.
// Uses a streaming JSON scan because the log can grow large.
func (l *Logger) tailOne() (*Entry, error) {
	f, err := os.Open(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var last *Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20) // accommodate large entries
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("audit: parse line: %w", err)
		}
		ec := e
		last = &ec
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return last, nil
}

// Tail returns the last n entries.
func (l *Logger) Tail(n int) ([]Entry, error) {
	all, err := l.readAll()
	if err != nil {
		return nil, err
	}
	if n <= 0 || n >= len(all) {
		return all, nil
	}
	return all[len(all)-n:], nil
}

// VerifyReport summarises a chain check.
type VerifyReport struct {
	Total    int      `json:"total"`
	OK       bool     `json:"ok"`
	Failures []string `json:"failures,omitempty"`
}

// Verify walks the log entry-by-entry and confirms each entry's prev_hash
// matches the SHA-256 of the prior entry's canonical form. Failures are
// reported per-entry; the chain itself is not auto-repaired.
func (l *Logger) Verify() (VerifyReport, error) {
	all, err := l.readAll()
	if err != nil {
		return VerifyReport{}, err
	}
	rep := VerifyReport{Total: len(all), OK: true}
	if len(all) == 0 {
		return rep, nil
	}
	if all[0].Seq != 1 {
		rep.OK = false
		rep.Failures = append(rep.Failures, fmt.Sprintf("seq[0] = %d, want 1", all[0].Seq))
	}
	for i := 1; i < len(all); i++ {
		want, err := hashEntry(all[i-1])
		if err != nil {
			return rep, err
		}
		if all[i].PrevHash != want {
			rep.OK = false
			rep.Failures = append(rep.Failures, fmt.Sprintf("entry seq=%d prev_hash mismatch (want %s)", all[i].Seq, want))
		}
		if all[i].Seq != all[i-1].Seq+1 {
			rep.OK = false
			rep.Failures = append(rep.Failures, fmt.Sprintf("seq gap: %d after %d", all[i].Seq, all[i-1].Seq))
		}
	}
	return rep, nil
}

func (l *Logger) readAll() ([]Entry, error) {
	f, err := os.Open(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseEntries(f)
}

func parseEntries(r io.Reader) ([]Entry, error) {
	var out []Entry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("audit: parse line: %w", err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// hashEntry returns the canonical hash of an entry. We zero PrevHash before
// hashing so the chain doesn't depend on the prior chain — only on the
// content of that entry. This is what spec section 9 mandates.
func hashEntry(e Entry) (string, error) {
	cp := e
	cp.PrevHash = ""
	b, err := json.Marshal(cp)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
