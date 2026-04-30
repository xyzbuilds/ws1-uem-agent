package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSpinnerDoneReplacesLineWithCheck(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	var buf safeBuffer
	s := newSpinner(&buf, "Validating")
	time.Sleep(50 * time.Millisecond) // let it tick at least once
	s.Done(true, "Token issued")

	out := buf.String()
	if !strings.Contains(out, "Token issued") {
		t.Errorf("missing result; got %q", out)
	}
	if !strings.Contains(out, "✓") {
		t.Errorf("missing success sigil; got %q", out)
	}
	if !strings.Contains(out, "\r") {
		t.Errorf("expected carriage-return-based replacement; got %q", out)
	}
}

func TestSpinnerDoneFailureSigil(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	var buf safeBuffer
	s := newSpinner(&buf, "Validating")
	s.Done(false, "Auth failed")
	out := buf.String()
	if !strings.Contains(out, "✗") {
		t.Errorf("missing failure sigil; got %q", out)
	}
}

func TestSpinnerASCIIFallback(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "")
	if isUTF8Locale() {
		t.Skip("environment overrides not honored on this platform")
	}
	got := spinnerGlyphs()
	want := []rune(`|/-\`)
	if string(got) != string(want) {
		t.Errorf("ASCII fallback = %q, want %q", string(got), string(want))
	}
}

func TestSpinnerUTF8Detected(t *testing.T) {
	cases := map[string]bool{
		"en_US.UTF-8": true,
		"en_US.utf-8": true,
		"de_DE.UTF-8": true,
		"C":           false,
		"POSIX":       false,
		"":            false,
	}
	for v, want := range cases {
		t.Run(v, func(t *testing.T) {
			t.Setenv("LC_ALL", v)
			t.Setenv("LC_CTYPE", "")
			t.Setenv("LANG", "")
			if got := isUTF8Locale(); got != want {
				t.Errorf("isUTF8Locale(%q) = %v, want %v", v, got, want)
			}
		})
	}
}

func TestSpinnerDoneClearsLongerTickLine(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	var buf safeBuffer
	s := newSpinner(&buf, "Validating Workspace ONE credentials")
	time.Sleep(100 * time.Millisecond) // let it tick
	s.Done(true, "Done")
	out := buf.String()
	// Final write must include the ANSI erase-line so a short result
	// doesn't leave the long label tail visible.
	if !strings.Contains(out, "\r\x1b[K  ✓ Done") {
		t.Errorf("final line should be cleared with \\r\\x1b[K; got %q", out)
	}
}

// safeBuffer is bytes.Buffer with a mutex, since the spinner writes
// from a goroutine.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
