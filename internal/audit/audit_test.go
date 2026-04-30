package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleEntry(op string) Entry {
	return Entry{
		Caller:     "test",
		Operation:  op,
		ArgsHash:   "sha256:abc",
		Class:      "write",
		Profile:    "operator",
		Tenant:     "12345",
		Result:     "ok",
		DurationMs: 42,
	}
}

func TestAppendChainsHashes(t *testing.T) {
	dir := t.TempDir()
	l, err := New(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e1, err := l.Append(sampleEntry("op.a"))
	if err != nil {
		t.Fatalf("Append1: %v", err)
	}
	if e1.Seq != 1 {
		t.Errorf("seq[0] = %d", e1.Seq)
	}
	if !strings.HasPrefix(e1.PrevHash, "sha256:0000") {
		t.Errorf("seq[0].prev_hash = %q (want zero hash)", e1.PrevHash)
	}
	e2, err := l.Append(sampleEntry("op.b"))
	if err != nil {
		t.Fatalf("Append2: %v", err)
	}
	if e2.Seq != 2 {
		t.Errorf("seq[1] = %d", e2.Seq)
	}
	expected, _ := hashEntry(e1)
	if e2.PrevHash != expected {
		t.Errorf("seq[1].prev_hash = %q, want %q", e2.PrevHash, expected)
	}
}

func TestVerifyAllOK(t *testing.T) {
	dir := t.TempDir()
	l, _ := New(filepath.Join(dir, "audit.log"))
	for i := 0; i < 5; i++ {
		if _, err := l.Append(sampleEntry("op")); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	rep, err := l.Verify()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK {
		t.Errorf("Verify failed: %+v", rep)
	}
	if rep.Total != 5 {
		t.Errorf("Total = %d", rep.Total)
	}
}

func TestVerifyDetectsTamper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	l, _ := New(path)
	for i := 0; i < 3; i++ {
		if _, err := l.Append(sampleEntry("op")); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	// Mutate the second entry's args_hash to simulate tampering.
	raw, _ := os.ReadFile(path)
	out := strings.Replace(string(raw), `"args_hash":"sha256:abc"`,
		`"args_hash":"sha256:tampered"`, 1)
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	rep, err := l.Verify()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.OK {
		t.Error("Verify should detect tampering")
	}
	if len(rep.Failures) == 0 {
		t.Error("Failures should be non-empty after tamper")
	}
}

func TestVerifyDetectsSeqGap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	l, _ := New(path)
	l.Append(sampleEntry("a"))
	l.Append(sampleEntry("b"))
	// Manually append a forged entry skipping a sequence number.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"ts":"2026-04-30T00:00:00Z","seq":99,"caller":"x","operation":"y","args_hash":"sha256:0","class":"read","profile":"ro","result":"ok","duration_ms":0,"prev_hash":"sha256:0"}` + "\n")
	f.Close()
	rep, _ := l.Verify()
	if rep.OK {
		t.Error("seq gap should fail verification")
	}
}

func TestTail(t *testing.T) {
	dir := t.TempDir()
	l, _ := New(filepath.Join(dir, "audit.log"))
	for i := 0; i < 7; i++ {
		l.Append(sampleEntry("op"))
	}
	last3, err := l.Tail(3)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(last3) != 3 {
		t.Fatalf("Tail returned %d", len(last3))
	}
	if last3[0].Seq != 5 || last3[2].Seq != 7 {
		t.Errorf("Tail seqs = %v", []int{last3[0].Seq, last3[1].Seq, last3[2].Seq})
	}
}

func TestTailEmptyFile(t *testing.T) {
	dir := t.TempDir()
	l, _ := New(filepath.Join(dir, "audit.log"))
	out, err := l.Tail(10)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("Tail of empty file = %d entries", len(out))
	}
}

func TestVerifyEmptyFileOK(t *testing.T) {
	dir := t.TempDir()
	l, _ := New(filepath.Join(dir, "audit.log"))
	rep, err := l.Verify()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK {
		t.Errorf("Empty log should verify clean: %+v", rep)
	}
}

func TestEntryIDFormat(t *testing.T) {
	e := Entry{Ts: "2026-04-30T14:00:00Z", Seq: 117}
	if got := e.EntryID(); got != "2026-04-30T14:00:00Z#117" {
		t.Errorf("EntryID = %q", got)
	}
}
