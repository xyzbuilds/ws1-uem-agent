# ws1 setup Onboarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `ws1 setup` (interactive first-run wizard) plus the companion `ws1 status` snapshot command, per the design at `docs/superpowers/specs/2026-04-30-ws1-setup-onboarding-design.md`.

**Architecture:** New `cmd/ws1/setup.go` orchestrator wraps existing primitives (`profile add`, `og use`, `profile use`). Stub-able `Prompter` interface in `cmd/ws1/prompt.go` so the wizard is unit-testable without real stdin. Spinner with UTF-8 detection (`cmd/ws1/spinner.go`). New mock route in `test/mockws1/server.go` for OG-list lookup. Independent `cmd/ws1/status.go` companion command emits a one-envelope snapshot of current config.

**Tech Stack:** Go 1.25, Cobra (existing), `golang.org/x/term` (existing), no new deps. CI: `golangci-lint v2` + `go test ./...` on Go 1.25.

**Sub-decisions resolved during planning:**
- **OG-list op identifier:** `systemv2.organizationgroups.organizationgroupsearch` (preferred) at `GET /api/system/groups/search`; falls back to `systemv1.organizationgroups.locationgroupsearch` at the same path if the v2 op isn't in the compiled index.
- **Spinner UTF-8 detection rule:** check `LC_ALL`, `LC_CTYPE`, `LANG` env vars (in that order); if any contains the substring `UTF-8` or `utf-8`, use the Braille glyphs; otherwise fall back to ASCII `| / - \`.
- **Mock OG fixtures:** three OGs — `Global` (id 1, uuid `70a00000-0000-0000-0000-000000000001`), `EMEA` (id 2042, uuid `8b300000-0000-0000-0000-000000000001`), `EMEA-Pilot` (id 4067, uuid `c9100000-0000-0000-0000-000000000001`).

---

## Task 1: Prompter interface + StubPrompter

**Files:**
- Create: `cmd/ws1/prompt.go`
- Test: `cmd/ws1/prompt_test.go`

The interface every wizard prompt goes through. `StubPrompter` is the test double that all later setup-wizard tests will inject. We build the stub first so subsequent tests have something to drive.

- [ ] **Step 1: Write the failing test** for the stub at `cmd/ws1/prompt_test.go`:

```go
package main

import (
	"errors"
	"testing"
)

func TestStubPrompterAsk(t *testing.T) {
	p := &StubPrompter{
		AskAnswers: map[string]string{
			"Tenant hostname": "cn1506.awmdm.com",
		},
	}
	got, err := p.Ask("Tenant hostname", "default.awmdm.com")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got != "cn1506.awmdm.com" {
		t.Errorf("Ask = %q, want cn1506.awmdm.com", got)
	}
	if len(p.AskedLabels) != 1 || p.AskedLabels[0] != "Tenant hostname" {
		t.Errorf("AskedLabels = %v", p.AskedLabels)
	}
}

func TestStubPrompterAskFallsBackToDefault(t *testing.T) {
	p := &StubPrompter{}
	got, err := p.Ask("Region", "na")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got != "na" {
		t.Errorf("expected default fallback, got %q", got)
	}
}

func TestStubPrompterAskSecret(t *testing.T) {
	p := &StubPrompter{SecretAnswers: []string{"hunter2", "second-secret"}}
	got, _ := p.AskSecret("Client Secret")
	if got != "hunter2" {
		t.Errorf("first secret = %q", got)
	}
	got, _ = p.AskSecret("Client Secret again")
	if got != "second-secret" {
		t.Errorf("second secret = %q", got)
	}
}

func TestStubPrompterPick(t *testing.T) {
	options := []PickItem{
		{Label: "uat", Hint: "Ohio"},
		{Label: "na", Hint: "Virginia"},
		{Label: "emea", Hint: "Frankfurt"},
		{Label: "apac", Hint: "Tokyo"},
	}
	p := &StubPrompter{PickIndex: []int{2}} // 1-indexed: pick "na"
	got, err := p.Pick("Region", options)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if got.Label != "na" {
		t.Errorf("picked = %q, want na", got.Label)
	}
}

func TestStubPrompterPickByLetter(t *testing.T) {
	options := []ByLetterItem{
		{Letter: 'O', Label: "Overwrite"},
		{Letter: 'k', Label: "Keep"},
		{Letter: 'c', Label: "Cancel"},
	}
	p := &StubPrompter{LetterPicks: []byte{'O'}}
	got, err := p.PickByLetter("Profile exists", options)
	if err != nil {
		t.Fatalf("PickByLetter: %v", err)
	}
	if got.Letter != 'O' {
		t.Errorf("letter = %c", got.Letter)
	}
}

func TestStubPrompterPickIndexOutOfRange(t *testing.T) {
	p := &StubPrompter{PickIndex: []int{}}
	_, err := p.Pick("anything", []PickItem{{Label: "x"}})
	if !errors.Is(err, ErrStubPromptExhausted) {
		t.Errorf("expected ErrStubPromptExhausted, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ws1/... -run TestStubPrompter -v`
Expected: FAIL with `undefined: StubPrompter`, `undefined: PickItem`, etc.

- [ ] **Step 3: Implement** `cmd/ws1/prompt.go`:

```go
package main

import (
	"errors"
)

// Prompter is the abstraction over interactive input. Production code
// uses a TTY-backed implementation; tests use StubPrompter.
//
// All methods return an error if input fails (EOF on a closed stdin,
// stub's queue exhausted, etc.). Empty user input on Ask returns the
// default value.
type Prompter interface {
	Ask(label, defaultValue string) (string, error)
	AskSecret(label string) (string, error)
	Pick(label string, options []PickItem) (PickItem, error)
	PickByLetter(label string, options []ByLetterItem) (ByLetterItem, error)
	Spinner(label string) Spinner
}

// PickItem is one entry in a numbered picker (regions, OGs, profiles).
// Label is shown in the menu; Hint is optional secondary text; Value
// is what the caller acts on (often == Label, but can differ for
// labels with metadata).
type PickItem struct {
	Label string
	Hint  string
	Value string
}

// ByLetterItem is one entry in a single-character picker (binary
// confirmations like overwrite/keep/cancel). Letter is the keystroke
// the user presses; Label is the displayed text.
type ByLetterItem struct {
	Letter byte
	Label  string
	Value  string
}

// Spinner is the in-place progress indicator. Done(ok, result)
// replaces the spinner line with "✓ <result>" or "✗ <result>" and
// stops the goroutine.
type Spinner interface {
	Done(ok bool, result string)
}

// ErrStubPromptExhausted means a test ran the wizard further than the
// stub had answers queued for. Indicates the test is missing answers.
var ErrStubPromptExhausted = errors.New("stub prompter: no more queued answers")

// StubPrompter is the test double. Tests pre-load answers in
// AskAnswers / SecretAnswers / PickIndex / LetterPicks and assert on
// AskedLabels / Spins after the wizard runs.
//
// Ask: if AskAnswers[label] is set, returns it; otherwise returns
// defaultValue. SecretAnswers is consumed in order, one per call.
// PickIndex is 1-indexed and consumed in order. LetterPicks is
// consumed in order.
type StubPrompter struct {
	AskAnswers    map[string]string
	SecretAnswers []string
	PickIndex     []int  // 1-indexed
	LetterPicks   []byte

	// AskedLabels records labels passed to Ask, for assertions.
	AskedLabels []string
	// Spins records labels passed to Spinner, for assertions.
	Spins []string
}

func (s *StubPrompter) Ask(label, def string) (string, error) {
	s.AskedLabels = append(s.AskedLabels, label)
	if v, ok := s.AskAnswers[label]; ok {
		return v, nil
	}
	return def, nil
}

func (s *StubPrompter) AskSecret(label string) (string, error) {
	if len(s.SecretAnswers) == 0 {
		return "", ErrStubPromptExhausted
	}
	v := s.SecretAnswers[0]
	s.SecretAnswers = s.SecretAnswers[1:]
	return v, nil
}

func (s *StubPrompter) Pick(label string, options []PickItem) (PickItem, error) {
	if len(s.PickIndex) == 0 {
		return PickItem{}, ErrStubPromptExhausted
	}
	idx := s.PickIndex[0]
	s.PickIndex = s.PickIndex[1:]
	if idx < 1 || idx > len(options) {
		return PickItem{}, errors.New("stub: pick index out of range")
	}
	return options[idx-1], nil
}

func (s *StubPrompter) PickByLetter(label string, options []ByLetterItem) (ByLetterItem, error) {
	if len(s.LetterPicks) == 0 {
		return ByLetterItem{}, ErrStubPromptExhausted
	}
	letter := s.LetterPicks[0]
	s.LetterPicks = s.LetterPicks[1:]
	for _, o := range options {
		if o.Letter == letter {
			return o, nil
		}
	}
	return ByLetterItem{}, errors.New("stub: letter not in options")
}

// stubSpinner is the no-op spinner used by StubPrompter. Records
// invocations into the parent stub's Spins slice; Done is a no-op.
type stubSpinner struct {
	parent *StubPrompter
	label  string
}

func (s *stubSpinner) Done(ok bool, result string) {
	// no-op: stub captures only the label
}

func (s *StubPrompter) Spinner(label string) Spinner {
	s.Spins = append(s.Spins, label)
	return &stubSpinner{parent: s, label: label}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/ws1/... -run TestStubPrompter -v`
Expected: PASS for all six TestStubPrompter* tests.

- [ ] **Step 5: Verify lint + vet clean**

Run: `go vet ./cmd/ws1/... && golangci-lint run ./cmd/ws1/...`
Expected: no issues. If gofmt complains, run `golangci-lint run --fix ./cmd/ws1/...`.

- [ ] **Step 6: Commit**

```bash
git add cmd/ws1/prompt.go cmd/ws1/prompt_test.go
git commit -m "$(cat <<'EOF'
feat(cmd): Prompter interface + StubPrompter for the setup wizard

The abstraction every interactive prompt goes through. Tests inject
StubPrompter with pre-loaded answers; production wires the
TTY-backed implementation in the next task. Six unit tests cover
Ask (with default fallback), AskSecret, Pick (numbered), PickByLetter
(single-char), and stub-exhaustion errors.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: TTY Prompter implementation

**Files:**
- Modify: `cmd/ws1/prompt.go` (append TTYPrompter)
- Test: `cmd/ws1/prompt_test.go` (append TTY-related tests using `os.Pipe`)

The real-stdin implementation. Tests drive it through an `os.Pipe` to keep them deterministic.

- [ ] **Step 1: Write the failing test** by appending to `cmd/ws1/prompt_test.go`:

```go
import (
	"io"
	"os"
	"strings"
	"sync"
)

// driveTTYPrompter writes input to an in-process pipe wired to a
// TTYPrompter, runs fn (which calls Ask/Pick/etc.), and returns
// captured stderr output for assertion.
func driveTTYPrompter(t *testing.T, input string, fn func(p *TTYPrompter)) string {
	t.Helper()
	pr, pw, _ := os.Pipe()
	ow, oerrR := newCaptureWriter()

	p := &TTYPrompter{In: pr, Out: ow}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.WriteString(pw, input)
		_ = pw.Close()
	}()

	fn(p)
	wg.Wait()
	return oerrR()
}

// newCaptureWriter returns an io.Writer plus a function that returns
// everything written to it as a string.
func newCaptureWriter() (io.Writer, func() string) {
	var sb strings.Builder
	var mu sync.Mutex
	return &lockedWriter{sb: &sb, mu: &mu}, func() string {
		mu.Lock()
		defer mu.Unlock()
		return sb.String()
	}
}

type lockedWriter struct {
	sb *strings.Builder
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sb.Write(p)
}

func TestTTYPrompterAsk(t *testing.T) {
	var got string
	out := driveTTYPrompter(t, "cn1506.awmdm.com\n", func(p *TTYPrompter) {
		v, err := p.Ask("Tenant hostname", "default.awmdm.com")
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		got = v
	})
	if got != "cn1506.awmdm.com" {
		t.Errorf("Ask = %q, want cn1506.awmdm.com", got)
	}
	if !strings.Contains(out, "Tenant hostname") {
		t.Errorf("output missing label; got %q", out)
	}
	if !strings.Contains(out, "[default.awmdm.com]") {
		t.Errorf("output missing default hint; got %q", out)
	}
}

func TestTTYPrompterAskEmptyKeepsDefault(t *testing.T) {
	var got string
	driveTTYPrompter(t, "\n", func(p *TTYPrompter) {
		v, _ := p.Ask("Region", "na")
		got = v
	})
	if got != "na" {
		t.Errorf("empty input should keep default; got %q", got)
	}
}

func TestTTYPrompterPick(t *testing.T) {
	var picked PickItem
	driveTTYPrompter(t, "2\n", func(p *TTYPrompter) {
		v, err := p.Pick("Region", []PickItem{
			{Label: "uat", Hint: "Ohio"},
			{Label: "na", Hint: "Virginia"},
		})
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		picked = v
	})
	if picked.Label != "na" {
		t.Errorf("Picked.Label = %q, want na", picked.Label)
	}
}

func TestTTYPrompterPickRetriesOnInvalid(t *testing.T) {
	var picked PickItem
	out := driveTTYPrompter(t, "5\n2\n", func(p *TTYPrompter) {
		v, err := p.Pick("Region", []PickItem{
			{Label: "uat"},
			{Label: "na"},
		})
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		picked = v
	})
	if picked.Label != "na" {
		t.Errorf("retry: picked %q", picked.Label)
	}
	if !strings.Contains(out, "invalid") {
		t.Errorf("expected 'invalid' hint; got %q", out)
	}
}

func TestTTYPrompterPickByLetter(t *testing.T) {
	var picked ByLetterItem
	driveTTYPrompter(t, "O", func(p *TTYPrompter) {
		v, err := p.PickByLetter("?", []ByLetterItem{
			{Letter: 'O', Label: "Overwrite"},
			{Letter: 'k', Label: "Keep"},
		})
		if err != nil {
			t.Fatalf("PickByLetter: %v", err)
		}
		picked = v
	})
	if picked.Letter != 'O' {
		t.Errorf("letter = %c", picked.Letter)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ws1/... -run TestTTYPrompter -v`
Expected: FAIL with `undefined: TTYPrompter`.

- [ ] **Step 3: Implement** TTYPrompter by appending to `cmd/ws1/prompt.go`:

```go
import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// TTYPrompter is the real-stdin implementation of Prompter.
//
// In must be a *os.File whose Fd() returns a TTY for AskSecret to
// suppress echo; tests can substitute an os.Pipe (AskSecret in that
// case falls back to plain ReadString, since term.ReadPassword on a
// non-TTY returns an error).
//
// Out receives prompt labels and (in Pick) the menu listing. Default
// production wiring: In = os.Stdin, Out = os.Stderr (so stdout stays
// envelope-clean for downstream parsers).
type TTYPrompter struct {
	In  *os.File
	Out io.Writer
}

// NewTTYPrompter wires stdin + stderr.
func NewTTYPrompter() *TTYPrompter {
	return &TTYPrompter{In: os.Stdin, Out: os.Stderr}
}

func (p *TTYPrompter) Ask(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.Out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.Out, "%s: ", label)
	}
	line, err := bufio.NewReader(p.In).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

func (p *TTYPrompter) AskSecret(label string) (string, error) {
	fmt.Fprintf(p.Out, "%s: ", label)
	if term.IsTerminal(int(p.In.Fd())) {
		pw, err := term.ReadPassword(int(p.In.Fd()))
		fmt.Fprintln(p.Out)
		return string(pw), err
	}
	// Test path: pipe-backed input, no echo suppression.
	line, err := bufio.NewReader(p.In).ReadString('\n')
	return strings.TrimSpace(line), err
}

func (p *TTYPrompter) Pick(label string, options []PickItem) (PickItem, error) {
	for {
		fmt.Fprintf(p.Out, "%s:\n", label)
		for i, o := range options {
			if o.Hint != "" {
				fmt.Fprintf(p.Out, "  [%d] %-8s %s\n", i+1, o.Label, o.Hint)
			} else {
				fmt.Fprintf(p.Out, "  [%d] %s\n", i+1, o.Label)
			}
		}
		fmt.Fprintf(p.Out, "Pick: ")
		line, err := bufio.NewReader(p.In).ReadString('\n')
		if err != nil && line == "" {
			return PickItem{}, err
		}
		line = strings.TrimSpace(line)
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(options) {
			fmt.Fprintf(p.Out, "  invalid; pick a number 1-%d\n", len(options))
			continue
		}
		return options[n-1], nil
	}
}

func (p *TTYPrompter) PickByLetter(label string, options []ByLetterItem) (ByLetterItem, error) {
	parts := make([]string, 0, len(options))
	for _, o := range options {
		parts = append(parts, fmt.Sprintf("[%c] %s", o.Letter, o.Label))
	}
	fmt.Fprintf(p.Out, "%s  %s: ", label, strings.Join(parts, "  "))
	buf := make([]byte, 1)
	if _, err := p.In.Read(buf); err != nil {
		return ByLetterItem{}, err
	}
	fmt.Fprintln(p.Out)
	for _, o := range options {
		if o.Letter == buf[0] {
			return o, nil
		}
	}
	return ByLetterItem{}, fmt.Errorf("invalid choice %q", string(buf))
}

// Spinner construction lives in spinner.go (next task); this is a
// placeholder that returns a no-op until that file lands. Kept here
// as a method so TTYPrompter implements the Prompter interface even
// during the transitional commit.
func (p *TTYPrompter) Spinner(label string) Spinner {
	return &noopSpinner{}
}

type noopSpinner struct{}

func (n *noopSpinner) Done(ok bool, result string) {}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/ws1/... -run TestTTYPrompter -v`
Expected: PASS for all five TTY tests.

- [ ] **Step 5: Verify all `cmd/ws1` tests still pass**

Run: `go test ./cmd/ws1/...`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/ws1/prompt.go cmd/ws1/prompt_test.go
git commit -m "$(cat <<'EOF'
feat(cmd): TTY-backed Prompter implementation

Real-stdin implementation backing StubPrompter's interface. Drives
through os.Pipe in tests for determinism. AskSecret uses term.ReadPassword
when In is a real TTY; falls back to plain read on a pipe so tests
can exercise the secret path without a PTY harness. Pick retries on
invalid input. PickByLetter takes a single byte (no Enter required).

Spinner returns a noopSpinner placeholder; real implementation lands
in the next task so this commit stays bounded.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Spinner with UTF-8 detection

**Files:**
- Create: `cmd/ws1/spinner.go`
- Test: `cmd/ws1/spinner_test.go`
- Modify: `cmd/ws1/prompt.go` (replace `noopSpinner` placeholder with real wiring)

In-place spinner per the design. Goroutine + ticker; `Done` overwrites the line and stops the goroutine. Sub-decision pinned: UTF-8 detection via `LC_ALL`/`LC_CTYPE`/`LANG` envs.

- [ ] **Step 1: Write the failing test** at `cmd/ws1/spinner_test.go`:

```go
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
		t.Skip("environment overrides not honoured on this platform")
	}
	got := spinnerGlyphs()
	want := []rune(`|/-\`)
	if string(got) != string(want) {
		t.Errorf("ASCII fallback = %q, want %q", string(got), string(want))
	}
}

func TestSpinnerUTF8Detected(t *testing.T) {
	cases := map[string]bool{
		"en_US.UTF-8":  true,
		"en_US.utf-8":  true,
		"de_DE.UTF-8":  true,
		"C":            false,
		"POSIX":        false,
		"":             false,
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ws1/... -run 'TestSpinner' -v`
Expected: FAIL with `undefined: newSpinner`, `undefined: spinnerGlyphs`, `undefined: isUTF8Locale`.

- [ ] **Step 3: Implement** `cmd/ws1/spinner.go`:

```go
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// realSpinner is the in-place progress indicator. Writes a tick line,
// overwritten on Done with a sigil + result. Goroutine cleans up
// deterministically when Done is called.
type realSpinner struct {
	label string
	out   io.Writer
	stop  chan struct{}
	done  chan struct{}
	once  sync.Once
}

func newSpinner(out io.Writer, label string) *realSpinner {
	s := &realSpinner{
		label: label,
		out:   out,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go s.run()
	return s
}

// run paints the spinner glyph + label until stop is closed. Uses
// `\r` to overwrite the same line every tick.
func (s *realSpinner) run() {
	defer close(s.done)
	glyphs := spinnerGlyphs()
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	i := 0
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			fmt.Fprintf(s.out, "\r  %s %s   ", string(glyphs[i%len(glyphs)]), s.label)
			i++
		}
	}
}

// Done stops the spinner and writes the final state in place. Calling
// Done more than once is a no-op (sync.Once).
func (s *realSpinner) Done(ok bool, result string) {
	s.once.Do(func() {
		close(s.stop)
		<-s.done
		sigil := "✓"
		if !ok {
			sigil = "✗"
		}
		fmt.Fprintf(s.out, "\r  %s %s\n", sigil, result)
	})
}

// spinnerGlyphs picks the Braille frames when the locale supports
// UTF-8, otherwise the ASCII fallback. Sub-decision pinned in the
// plan: check LC_ALL, LC_CTYPE, LANG (in that order) for "UTF-8".
func spinnerGlyphs() []rune {
	if isUTF8Locale() {
		return []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	}
	return []rune(`|/-\`)
}

// isUTF8Locale reports whether the user's locale advertises UTF-8.
// Case-insensitive match for "UTF-8" or "utf-8" in any of the three
// locale env vars; first non-empty value wins.
func isUTF8Locale() bool {
	for _, env := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		v := os.Getenv(env)
		if v == "" {
			continue
		}
		up := strings.ToUpper(v)
		return strings.Contains(up, "UTF-8") || strings.Contains(up, "UTF8")
	}
	return false
}
```

- [ ] **Step 4: Replace the placeholder Spinner method on TTYPrompter** in `cmd/ws1/prompt.go`. Find:

```go
// Spinner construction lives in spinner.go (next task); this is a
// placeholder that returns a no-op until that file lands. Kept here
// as a method so TTYPrompter implements the Prompter interface even
// during the transitional commit.
func (p *TTYPrompter) Spinner(label string) Spinner {
	return &noopSpinner{}
}

type noopSpinner struct{}

func (n *noopSpinner) Done(ok bool, result string) {}
```

Replace with:

```go
// Spinner returns an in-place spinner writing to p.Out.
func (p *TTYPrompter) Spinner(label string) Spinner {
	return newSpinner(p.Out, label)
}
```

- [ ] **Step 5: Run all tests**

Run: `go test ./cmd/ws1/... -v -run 'TestSpinner|TestTTYPrompter|TestStubPrompter'`
Expected: every TestSpinner*, TestTTYPrompter*, and TestStubPrompter* passes.

- [ ] **Step 6: Lint clean**

Run: `golangci-lint run ./cmd/ws1/...`
Expected: 0 issues.

- [ ] **Step 7: Commit**

```bash
git add cmd/ws1/spinner.go cmd/ws1/spinner_test.go cmd/ws1/prompt.go
git commit -m "$(cat <<'EOF'
feat(cmd): in-place spinner with UTF-8 detection

Goroutine + ticker writes the spinner line every 80ms; Done overwrites
the same line with a ✓/✗ sigil + result. Glyph set depends on locale:
Braille on UTF-8 terminals, ASCII | / - \ fallback otherwise. Sub-decision
pinned in the plan: detection checks LC_ALL, LC_CTYPE, LANG envs (case
insensitive) for the "UTF-8" / "UTF8" substring.

Replaces the noopSpinner placeholder in TTYPrompter; tests cover both
glyph paths plus success/failure sigil + replacement semantics.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Mock OG-list route

**Files:**
- Modify: `test/mockws1/server.go` (add route + fixture)
- Modify: `test/mockws1/server_test.go` (test the new route)

The wizard needs to call an OG-search op against the mock during integration tests. The mock currently doesn't serve `/api/system/groups/search`. Adding the route + canned 3-OG fixture matching the design's mockup.

- [ ] **Step 1: Write the failing test** by appending to `test/mockws1/server_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./test/mockws1/... -run TestOrgGroupSearch -v`
Expected: FAIL with status 501 or 404.

- [ ] **Step 3: Add the OG fixture and route** to `test/mockws1/server.go`. After the `Device` struct definition, add:

```go
// OrgGroup mirrors the org-group record shape the wizard expects from
// systemv2.organizationgroups.organizationgroupsearch (and the v1
// variant). Both Id and Uuid are populated.
type OrgGroup struct {
	Id   int    `json:"Id"`
	Uuid string `json:"Uuid"`
	Name string `json:"Name"`
}
```

Inside `Server` struct, add:

```go
	orgGroups []OrgGroup
```

Inside `New()`, populate the fixture. Find the existing `devices: []Device{...}` block; immediately after the closing of that slice's items (before the `Issued: ...` line in the returned Server literal), the struct now also needs `orgGroups`:

Replace the `New()` function body's `return &Server{...}` block with:

```go
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
```

In `HTTPHandler()`, register the new route. Find the section commenting `// systemv1 / systemv2: users` and add a route directly under it for groups. Replace:

```go
	// systemv1 / systemv2: users
	mux.HandleFunc("/api/system/users/search", s.handleUsersSearch)
	mux.HandleFunc("/api/system/users/", s.handleUsersByUuidOrID)
```

With:

```go
	// systemv1 / systemv2: users
	mux.HandleFunc("/api/system/users/search", s.handleUsersSearch)
	mux.HandleFunc("/api/system/users/", s.handleUsersByUuidOrID)

	// systemv1 / systemv2: organization groups
	mux.HandleFunc("/api/system/groups/search", s.handleOrgGroupSearch)
```

- [ ] **Step 4: Implement** `handleOrgGroupSearch` by appending to `test/mockws1/server.go` after `handleUsersByUuidOrID`:

```go
// handleOrgGroupSearch serves both
//   GET /api/system/groups/search   — systemv2.organizationgroups.organizationgroupsearch
//   GET /api/system/groups/search   — systemv1.organizationgroups.locationgroupsearch
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
```

- [ ] **Step 5: Run tests**

Run: `go test ./test/mockws1/... -v`
Expected: all PASS, including new `TestOrgGroupSearch`.

- [ ] **Step 6: Verify integration suite still passes** (mock changes can break existing routes if mux ordering shifts):

Run: `go test ./test/integration/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add test/mockws1/server.go test/mockws1/server_test.go
git commit -m "$(cat <<'EOF'
feat(test/mockws1): add /api/system/groups/search route + OG fixtures

Three canned org groups matching the design's mockup: Global (id 1),
EMEA (id 2042), EMEA-Pilot (id 4067), each with a deterministic UUID.
Same path serves both v1 (locationgroupsearch) and v2
(organizationgroupsearch) — the WS1 path is identical, version is
negotiated via Accept header. Mock doesn't enforce version; returns
the same shape regardless. Used by the upcoming setup wizard
integration test.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: `ws1 status` command

**Files:**
- Create: `cmd/ws1/status.go`
- Test: `cmd/ws1/status_test.go`
- Modify: `cmd/ws1/root.go` (register the command)

Companion command: snapshot of current config, on-disk only, no API call. Independent of `setup`. Built first so the wizard can suggest it as a smoke-test verb at the end.

- [ ] **Step 1: Write the failing test** at `cmd/ws1/status_test.go`:

```go
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
)

func runWS1Status(t *testing.T, cfgDir string) envelope.Envelope {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ws1")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}
	out, err := exec.Command(bin, "status").CombinedOutput()
	if err != nil {
		t.Logf("output: %s", out)
	}
	// Filter envelope JSON line.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	last := lines[len(lines)-1]
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(last), &env); err != nil {
		t.Fatalf("parse %q: %v", last, err)
	}
	_ = cfgDir
	return env
}

func TestStatusEmptyConfig(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", cfg)
	t.Setenv("HOME", cfg)
	env := runWS1Status(t, cfg)
	if env.Operation != "ws1.status" {
		t.Errorf("op = %q", env.Operation)
	}
	if !env.OK {
		t.Errorf("ok=false: %+v", env.Error)
	}
	data, _ := env.Data.(map[string]any)
	if data["active_profile"] != "ro" {
		// First-run defaults: active = ro per auth.Active().
		t.Errorf("active_profile = %v, want ro", data["active_profile"])
	}
	cps, _ := data["configured_profiles"].([]any)
	if len(cps) != 0 {
		t.Errorf("configured_profiles = %v, want []", cps)
	}
}

func TestStatusInfersRegion(t *testing.T) {
	if testing.Short() {
		t.Skip("rebuild slow in -short")
	}
	cfg := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", cfg)
	t.Setenv("HOME", cfg)
	// Write a profile with an na auth_url.
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	profilesYAML := `version: 1
profiles:
- name: operator
  tenant: cn1506.awmdm.com
  api_url: https://cn1506.awmdm.com
  auth_url: https://na.uemauth.workspaceone.com/connect/token
  client_id: dummy
`
	if err := os.WriteFile(filepath.Join(cfg, "profiles.yaml"), []byte(profilesYAML), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "profile"), []byte("operator\n"), 0o600); err != nil {
		t.Fatalf("write active: %v", err)
	}
	env := runWS1Status(t, cfg)
	data, _ := env.Data.(map[string]any)
	if data["region_inferred"] != "na" {
		t.Errorf("region_inferred = %v, want na", data["region_inferred"])
	}
	if data["tenant"] != "cn1506.awmdm.com" {
		t.Errorf("tenant = %v", data["tenant"])
	}
}

func TestStatusUnknownRegion(t *testing.T) {
	if testing.Short() {
		t.Skip("rebuild slow in -short")
	}
	cfg := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", cfg)
	t.Setenv("HOME", cfg)
	profilesYAML := `version: 1
profiles:
- name: operator
  tenant: x.example.com
  api_url: https://x.example.com
  auth_url: https://custom-oauth.example.com/connect/token
  client_id: dummy
`
	_ = os.WriteFile(filepath.Join(cfg, "profiles.yaml"), []byte(profilesYAML), 0o600)
	_ = os.WriteFile(filepath.Join(cfg, "profile"), []byte("operator\n"), 0o600)
	env := runWS1Status(t, cfg)
	data, _ := env.Data.(map[string]any)
	if data["region_inferred"] != "unknown" {
		t.Errorf("region_inferred = %v, want unknown", data["region_inferred"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ws1/... -run TestStatus -v`
Expected: FAIL — `unknown command "status"`.

- [ ] **Step 3: Implement** `cmd/ws1/status.go`:

```go
package main

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/xyzbuilds/ws1-uem-agent/internal/audit"
	"github.com/xyzbuilds/ws1-uem-agent/internal/auth"
	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
)

// newStatusCmd is `ws1 status`: a single-envelope snapshot of current
// configuration. Reads from disk only — no API call. Replaces the
// three-call sequence (profile current + og current + audit tail
// --last 1) for agent introspection.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current configuration snapshot (profile, OG, region, recent audit)",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			env := envelope.New("ws1.status").WithData(buildStatusData()).WithDuration(time.Since(start))
			emitAndExit(env)
		},
	}
}

// buildStatusData composes the status envelope data. Pure function of
// disk state; never errors out (missing files become empty fields).
func buildStatusData() map[string]any {
	out := map[string]any{
		"active_profile":      "",
		"configured_profiles": []string{},
		"tenant":              "",
		"region_inferred":     "unknown",
		"og":                  nil,
		"audit_seq":           nil,
		"last_op_at":          nil,
	}

	if active, err := auth.Active(); err == nil {
		out["active_profile"] = active
	}

	profiles, _ := auth.LoadProfiles()
	names := make([]string, 0, len(profiles))
	var activeProfile *auth.Profile
	activeName, _ := out["active_profile"].(string)
	for i := range profiles {
		names = append(names, profiles[i].Name)
		if profiles[i].Name == activeName {
			activeProfile = &profiles[i]
		}
	}
	out["configured_profiles"] = names

	if activeProfile != nil {
		out["tenant"] = activeProfile.Tenant
		out["region_inferred"] = inferRegionFromAuthURL(activeProfile.AuthURL)
	}

	if og, err := auth.CurrentOG(); err == nil && og != "" {
		out["og"] = map[string]any{"id": og}
	}

	// Tail of the audit log: latest entry's seq + ts.
	if path, err := audit.DefaultPath(); err == nil {
		if l, lerr := audit.New(path); lerr == nil {
			if entries, terr := l.Tail(1); terr == nil && len(entries) > 0 {
				e := entries[len(entries)-1]
				out["audit_seq"] = e.Seq
				out["last_op_at"] = e.Ts
			}
		}
	}

	return out
}

// inferRegionFromAuthURL reverse-maps an auth_url to a region code.
// Returns "unknown" if no region in cmd/ws1/regions.go matches.
func inferRegionFromAuthURL(authURL string) string {
	for _, r := range Regions {
		if r.TokenURL == authURL {
			return r.Code
		}
	}
	return "unknown"
}
```

- [ ] **Step 4: Register the command** in `cmd/ws1/root.go`. Find:

```go
	cmd.AddCommand(
		newDoctorCmd(),
		newProfileCmd(),
		newOgCmd(),
		newOpsCmd(),
		newAuditCmd(),
	)
```

Replace with:

```go
	cmd.AddCommand(
		newDoctorCmd(),
		newProfileCmd(),
		newOgCmd(),
		newOpsCmd(),
		newAuditCmd(),
		newStatusCmd(),
	)
```

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/ws1/... -run TestStatus -v`
Expected: all three TestStatus* PASS.

- [ ] **Step 6: Run the full ws1 test suite**

Run: `go test ./cmd/ws1/...`
Expected: PASS.

- [ ] **Step 7: Smoke-test the binary by hand**

Run:
```
go build -o /tmp/ws1 ./cmd/ws1 && WS1_CONFIG_DIR=/tmp/ws1cfg /tmp/ws1 status | jq
```
Expected output: an envelope with `"operation":"ws1.status"`, `active_profile: "ro"`, empty `configured_profiles`, `region_inferred: "unknown"`.

- [ ] **Step 8: Commit**

```bash
git add cmd/ws1/status.go cmd/ws1/status_test.go cmd/ws1/root.go
git commit -m "$(cat <<'EOF'
feat(cmd): ws1 status — one-envelope config snapshot

Reads ~/.config/ws1/{profile,profiles.yaml,og,audit.log} (or the
WS1_CONFIG_DIR override) and emits a single envelope summarising
{active_profile, configured_profiles, tenant, region_inferred, og,
audit_seq, last_op_at}. Pure on-disk read, no API call. Replaces
agent code that previously called profile current + og current +
audit tail --last 1 to assemble the same picture.

Region inference reverse-maps the auth_url against the canonical
regions table; emits "unknown" for custom auth_urls (no fallback to
slug-derived guess — that would be misleading for genuinely-private
deployments).

Three table-driven tests cover empty-config, na region inference, and
unknown-region (custom auth_url).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: ws1 setup — QuickStart happy path

**Files:**
- Create: `cmd/ws1/setup.go`
- Test: `cmd/ws1/setup_test.go`
- Modify: `cmd/ws1/root.go` (register the command)

The wizard's happy path: operator profile, real OAuth round-trip against the mock, OG list fetched, smoke test passes. No retries, no advanced mode, no reconfigure logic yet.

- [ ] **Step 1: Write the failing test** at `cmd/ws1/setup_test.go`:

```go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xyzbuilds/ws1-uem-agent/internal/auth"
)

// setupTestServer spins a small mock that responds to OAuth + an
// OG-list call. Returned closer must be invoked.
func setupTestServer(t *testing.T) (string, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("client_id") == "" {
			http.Error(w, "missing client_id", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
	})
	mux.HandleFunc("/api/system/groups/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"LocationGroups":[
			{"Id":1,"Uuid":"u1","Name":"Global"},
			{"Id":4067,"Uuid":"u4067","Name":"EMEA-Pilot"}
		],"Total":2}`))
	})
	mux.HandleFunc("/api/mdm/devices/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Devices":[{"DeviceID":1}],"Total":1}`))
	})
	srv := httptest.NewServer(mux)
	return srv.URL, srv.Close
}

func TestSetupQuickStartHappyPath(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", cfg)
	t.Setenv("HOME", cfg)
	t.Setenv("WS1_ALLOW_DISK_SECRETS", "1")
	t.Setenv("WS1_FORCE_INTERACTIVE", "1")

	url, closer := setupTestServer(t)
	defer closer()
	t.Setenv("WS1_BASE_URL", url)

	stub := &StubPrompter{
		AskAnswers: map[string]string{
			"Tenant hostname": "cn1506.awmdm.com",
			"Client ID":       "cid",
		},
		SecretAnswers: []string{"csec"},
		PickIndex: []int{
			2, // region: na
			2, // OG: EMEA-Pilot (second entry)
		},
	}

	opts := SetupOptions{
		Profile:      "operator",
		AuthURL:      url + "/oauth",
		Quick:        true,
		SkipSmoke:    true, // smoke uses mdmv1.devices.search; mock provides
	}

	err := RunSetup(context.Background(), opts, stub)
	if err != nil {
		t.Fatalf("RunSetup: %v", err)
	}

	// Profile written?
	profiles, perr := auth.LoadProfiles()
	if perr != nil {
		t.Fatalf("LoadProfiles: %v", perr)
	}
	if len(profiles) != 1 || profiles[0].Name != "operator" {
		t.Fatalf("profiles = %+v", profiles)
	}
	if profiles[0].Tenant != "cn1506.awmdm.com" {
		t.Errorf("tenant = %q", profiles[0].Tenant)
	}
	if profiles[0].AuthURL != url+"/oauth" {
		t.Errorf("auth_url = %q", profiles[0].AuthURL)
	}
	// Active profile set to operator (only one configured).
	active, _ := auth.Active()
	if active != "operator" {
		t.Errorf("active = %q, want operator", active)
	}
	// OG context set.
	og, _ := auth.CurrentOG()
	if og != "4067" {
		t.Errorf("og = %q, want 4067", og)
	}
	// Spinner messages emitted.
	if !containsLabel(stub.Spins, "Validating") {
		t.Errorf("expected Validating spinner; got %v", stub.Spins)
	}
	if !containsLabel(stub.Spins, "Fetching organization groups") {
		t.Errorf("expected OG fetch spinner; got %v", stub.Spins)
	}
}

func containsLabel(labels []string, prefix string) bool {
	for _, l := range labels {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ws1/... -run TestSetupQuick -v`
Expected: FAIL with `undefined: SetupOptions`, `undefined: RunSetup`.

- [ ] **Step 3: Implement** `cmd/ws1/setup.go`. This is the largest single addition; about 280 lines split into the cobra command + the orchestrator + helpers:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/xyzbuilds/ws1-uem-agent/internal/api"
	"github.com/xyzbuilds/ws1-uem-agent/internal/auth"
	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
	"github.com/xyzbuilds/ws1-uem-agent/internal/generated"
)

// SetupOptions holds the values that may come from flags. The wizard
// fills in any unset string from prompts (in interactive mode) or
// errors out (in non-interactive mode).
type SetupOptions struct {
	Profile      string // ro / operator / admin (default operator)
	Tenant       string
	Region       string // resolves AuthURL via regionToAuthURL
	AuthURL      string // overrides Region if set
	ClientID     string
	ClientSecret string
	OG           string

	Quick        bool // skip multi-profile picker (default true; --advanced flips it)
	SkipValidate bool // skip OAuth round-trip
	SkipSmoke    bool // skip the final smoke test
}

func newSetupCmd() *cobra.Command {
	var opts SetupOptions
	var advanced bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Connect ws1 to your Workspace ONE UEM tenant (interactive wizard)",
		Long: `ws1 setup walks through the first-run configuration: tenant URL,
region, OAuth credentials, and a default Org Group context. Re-running
setup detects existing config and offers each value as a default.

Quick mode (the default) configures one profile (operator). Use
--advanced to pick which profiles to configure (ro / operator / admin).

Non-interactive mode: supply --tenant + --region + --client-id +
--client-secret + --og to bootstrap from CI without prompts. Setting
the active profile from CI is intentionally refused; run
'ws1 profile use <name>' from a terminal afterward.

The OAuth client you provision in the WS1 console MUST have a role
matching the chosen profile. The CLI's class gate is belt-and-braces;
the OAuth role is the API-side enforcer.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			opts.Quick = !advanced
			prompter := Prompter(NewTTYPrompter())
			if !auth.IsInteractive() {
				// Non-interactive: every required flag must be set;
				// otherwise IDENTIFIER_AMBIGUOUS.
				if missing := missingRequiredFlags(opts); len(missing) > 0 {
					emitAndExit(envelope.NewError("ws1.setup",
						envelope.CodeIdentifierAmbiguous,
						"non-interactive setup requires all configuration via flags").
						WithErrorDetails(map[string]any{"missing": missing}).
						WithDuration(time.Since(start)))
					return
				}
			}
			if err := RunSetup(context.Background(), opts, prompter); err != nil {
				emitAndExit(envelope.NewError("ws1.setup",
					envelope.CodeInternalError, err.Error()).
					WithDuration(time.Since(start)))
				return
			}
			emitAndExit(envelope.New("ws1.setup").
				WithData(map[string]any{"complete": true}).
				WithDuration(time.Since(start)))
		},
	}
	cmd.Flags().BoolVar(&advanced, "advanced", false, "configure multiple profiles (ro/operator/admin)")
	cmd.Flags().StringVar(&opts.Profile, "profile", "operator", "profile name (ro|operator|admin)")
	cmd.Flags().StringVar(&opts.Tenant, "tenant", "", "tenant hostname")
	cmd.Flags().StringVar(&opts.Region, "region", "", "OAuth region: "+regionCodesString())
	cmd.Flags().StringVar(&opts.AuthURL, "auth-url", "", "OAuth token endpoint (overrides --region)")
	cmd.Flags().StringVar(&opts.ClientID, "client-id", "", "OAuth client_id")
	cmd.Flags().StringVar(&opts.ClientSecret, "client-secret", "", "OAuth client_secret")
	cmd.Flags().StringVar(&opts.OG, "og", "", "default OG ID")
	cmd.Flags().BoolVar(&opts.SkipValidate, "skip-validate", false, "skip OAuth round-trip")
	cmd.Flags().BoolVar(&opts.SkipSmoke, "skip-smoke-test", false, "skip the final smoke test")
	return cmd
}

func missingRequiredFlags(o SetupOptions) []string {
	var miss []string
	if o.Tenant == "" {
		miss = append(miss, "--tenant")
	}
	if o.Region == "" && o.AuthURL == "" {
		miss = append(miss, "--region (or --auth-url)")
	}
	if o.ClientID == "" {
		miss = append(miss, "--client-id")
	}
	if o.ClientSecret == "" {
		miss = append(miss, "--client-secret")
	}
	if o.OG == "" {
		miss = append(miss, "--og")
	}
	return miss
}

// RunSetup orchestrates the wizard. Pure function of options +
// prompter; emits to stderr via the prompter's Spinner / TTYPrompter.
// Returns nil on success, error if any step fatally fails.
func RunSetup(ctx context.Context, opts SetupOptions, p Prompter) error {
	// Step 1: Tenant.
	tenant, err := promptIfEmpty(p, "Tenant hostname", opts.Tenant, "")
	if err != nil {
		return err
	}
	opts.Tenant = tenant

	// Step 2: Region (skipped if AuthURL was supplied directly).
	if opts.AuthURL == "" {
		if opts.Region == "" {
			region, err := pickRegion(p)
			if err != nil {
				return err
			}
			opts.Region = region
		}
		url, ok := regionToAuthURL(opts.Region)
		if !ok {
			return fmt.Errorf("unknown region %q", opts.Region)
		}
		opts.AuthURL = url
	}

	// Step 3: Profile (Quick mode only configures opts.Profile; advanced
	// mode lands in a later task).
	profileName := opts.Profile
	if profileName == "" {
		profileName = "operator"
	}

	// Step 4: Credentials.
	clientID, err := promptIfEmpty(p, "Client ID", opts.ClientID, "")
	if err != nil {
		return err
	}
	opts.ClientID = clientID

	clientSecret := opts.ClientSecret
	if clientSecret == "" {
		clientSecret, err = p.AskSecret("Client Secret")
		if err != nil {
			return err
		}
	}
	opts.ClientSecret = clientSecret

	// Persist profile (without secret) and secret to keychain BEFORE
	// validation so the keychain prompt fires alongside the validating
	// spinner — feels like one flow rather than two unrelated dialogs.
	prof := auth.Profile{
		Name: profileName, Tenant: opts.Tenant,
		APIURL: "https://" + opts.Tenant, AuthURL: opts.AuthURL,
		ClientID: opts.ClientID,
	}
	if err := auth.SaveProfile(prof); err != nil {
		return fmt.Errorf("save profile: %w", err)
	}
	if err := auth.SaveClientSecret(profileName, opts.ClientID, opts.ClientSecret); err != nil {
		return fmt.Errorf("save secret: %w", err)
	}

	// Step 5: Validate.
	if !opts.SkipValidate {
		spin := p.Spinner("Validating against " + opts.AuthURL + "...")
		client := api.New(auth.NewOAuthClient(&prof))
		_, err := client.Source.Token(ctx)
		if err != nil {
			spin.Done(false, "Auth failed: "+err.Error())
			return fmt.Errorf("auth: %w", err)
		}
		spin.Done(true, "Token issued")
	}

	// Step 6: OG selection.
	og, err := pickOG(ctx, p, &prof, opts.OG)
	if err != nil {
		return err
	}
	if err := auth.SetOG(og); err != nil {
		return fmt.Errorf("save OG: %w", err)
	}

	// Step 7: Active profile.
	if auth.IsInteractive() {
		if err := auth.SwitchActive(profileName, true); err != nil {
			return fmt.Errorf("set active: %w", err)
		}
	}

	// Step 8: Smoke test.
	if !opts.SkipSmoke {
		runSmokeTest(ctx, p, &prof)
	}
	return nil
}

func promptIfEmpty(p Prompter, label, current, def string) (string, error) {
	if current != "" {
		return current, nil
	}
	return p.Ask(label, def)
}

func pickRegion(p Prompter) (string, error) {
	options := []PickItem{}
	for _, r := range Regions {
		options = append(options, PickItem{Label: r.Code, Hint: r.DataCenter, Value: r.Code})
	}
	pick, err := p.Pick("Region", options)
	if err != nil {
		return "", err
	}
	return pick.Value, nil
}

// pickOG fetches the OG list from the tenant and lets the user pick
// from a numbered menu. If the OG-list call fails (network or 4xx),
// falls back to a freeform "OG ID:" prompt.
func pickOG(ctx context.Context, p Prompter, prof *auth.Profile, prefilled string) (string, error) {
	if prefilled != "" {
		return prefilled, nil
	}
	spin := p.Spinner("Fetching organization groups...")
	ogs, err := fetchOGList(ctx, prof)
	if err != nil || len(ogs) == 0 {
		spin.Done(false, "Could not list OGs; enter ID manually")
		return p.Ask("OG ID", "")
	}
	spin.Done(true, fmt.Sprintf("Found %d OGs", len(ogs)))
	options := make([]PickItem, 0, len(ogs))
	for _, og := range ogs {
		options = append(options, PickItem{
			Label: og.Name,
			Hint:  fmt.Sprintf("(id %d)", og.ID),
			Value: strconv.Itoa(og.ID),
		})
	}
	pick, err := p.Pick("Organization group", options)
	if err != nil {
		return "", err
	}
	return pick.Value, nil
}

// ogRow is the parsed row of the OG-list response.
type ogRow struct {
	ID   int    `json:"Id"`
	UUID string `json:"Uuid"`
	Name string `json:"Name"`
}

// fetchOGList calls the v2 (or v1 fallback) org-group search op.
// Sub-decision pinned: prefer systemv2.organizationgroups.organizationgroupsearch;
// fall back to systemv1.organizationgroups.locationgroupsearch.
func fetchOGList(ctx context.Context, prof *auth.Profile) ([]ogRow, error) {
	client := api.New(auth.NewOAuthClient(prof))
	for _, op := range []string{
		"systemv2.organizationgroups.organizationgroupsearch",
		"systemv1.organizationgroups.locationgroupsearch",
	} {
		if _, ok := generated.Ops[op]; !ok {
			continue
		}
		resp, err := client.Do(ctx, op, api.Args{})
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == 403 {
			return nil, fmt.Errorf("403 forbidden")
		}
		if resp.StatusCode >= 400 {
			continue
		}
		var body struct {
			LocationGroups []ogRow `json:"LocationGroups"`
		}
		if err := resp.JSON(&body); err != nil {
			return nil, err
		}
		return body.LocationGroups, nil
	}
	return nil, errors.New("no OG-search op found in compiled index")
}

// runSmokeTest emits a spinner + final result. Failures are
// informational; setup is still considered successful.
func runSmokeTest(ctx context.Context, p Prompter, prof *auth.Profile) {
	spin := p.Spinner("Smoke test: ws1 mdmv1 devices search --pagesize 1")
	client := api.New(auth.NewOAuthClient(prof))
	resp, err := client.Do(ctx, "mdmv1.devices.search", api.Args{"pagesize": 1})
	if err != nil {
		spin.Done(false, "smoke test error: "+err.Error())
		return
	}
	if resp.StatusCode >= 400 {
		spin.Done(false, fmt.Sprintf("smoke test API %d", resp.StatusCode))
		return
	}
	spin.Done(true, "Received response")
}
```

- [ ] **Step 4: Register the command** in `cmd/ws1/root.go`. Find:

```go
	cmd.AddCommand(
		newDoctorCmd(),
		newProfileCmd(),
		newOgCmd(),
		newOpsCmd(),
		newAuditCmd(),
		newStatusCmd(),
	)
```

Replace with:

```go
	cmd.AddCommand(
		newDoctorCmd(),
		newProfileCmd(),
		newOgCmd(),
		newOpsCmd(),
		newAuditCmd(),
		newStatusCmd(),
		newSetupCmd(),
	)
```

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/ws1/... -run TestSetupQuick -v`
Expected: PASS.

- [ ] **Step 6: Run the full ws1 test suite**

Run: `go test ./cmd/ws1/...`
Expected: PASS.

- [ ] **Step 7: Verify no regressions in other packages**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add cmd/ws1/setup.go cmd/ws1/setup_test.go cmd/ws1/root.go
git commit -m "$(cat <<'EOF'
feat(cmd): ws1 setup — QuickStart wizard happy path

The new top-level setup command. QuickStart mode (default) configures
one profile (operator), validates via real OAuth round-trip, fetches
the OG list from the tenant, and runs a smoke test before exiting.
Re-uses existing primitives (auth.SaveProfile, auth.SaveClientSecret,
auth.SetOG, auth.SwitchActive); the wizard is just orchestration.

OG-list lookup prefers systemv2.organizationgroups.organizationgroupsearch;
falls back to systemv1.organizationgroups.locationgroupsearch when the
v2 op isn't compiled in. Both at GET /api/system/groups/search.

This commit covers only the happy path. Error retries (auth 401, region
typo, OG 403 fallback), --advanced multi-profile mode, non-interactive
flag-only mode, and reconfigure-from-existing-config land in subsequent
tasks.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Setup wizard error handling

**Files:**
- Modify: `cmd/ws1/setup.go` (retry loops, fallbacks)
- Modify: `cmd/ws1/setup_test.go` (test the retry / fallback paths)

Add the three retry-on-failure paths from spec §4.6: OAuth 401 retries up to 3, region-typo retries the region pick, OG-list 403 falls back to freeform OG-ID prompt.

- [ ] **Step 1: Write the failing tests** by appending to `cmd/ws1/setup_test.go`:

```go
func TestSetupOAuthRetryThenSucceed(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", cfg)
	t.Setenv("HOME", cfg)
	t.Setenv("WS1_ALLOW_DISK_SECRETS", "1")
	t.Setenv("WS1_FORCE_INTERACTIVE", "1")

	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, `{"error":"invalid_client"}`, 401)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
	})
	mux.HandleFunc("/api/system/groups/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"LocationGroups":[{"Id":1,"Uuid":"u","Name":"Global"}],"Total":1}`))
	})
	mux.HandleFunc("/api/mdm/devices/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Devices":[],"Total":0}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("WS1_BASE_URL", srv.URL)

	stub := &StubPrompter{
		AskAnswers: map[string]string{
			"Tenant hostname": "x",
			"Client ID":       "cid",
		},
		// First creds fail, second succeed.
		SecretAnswers: []string{"wrong", "right"},
		PickIndex:     []int{2 /*na*/, 1 /*OG Global*/},
	}
	// On retry the wizard re-prompts for client_id too; provide it.
	stub.AskAnswers["Client ID (retry)"] = "cid"

	opts := SetupOptions{Profile: "operator", AuthURL: srv.URL + "/oauth", Quick: true, SkipSmoke: true}
	if err := RunSetup(context.Background(), opts, stub); err != nil {
		t.Fatalf("RunSetup: %v", err)
	}
	if calls != 2 {
		t.Errorf("oauth calls = %d, want 2", calls)
	}
}

func TestSetupOAuthThreeFailuresExits(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", cfg)
	t.Setenv("HOME", cfg)
	t.Setenv("WS1_ALLOW_DISK_SECRETS", "1")
	t.Setenv("WS1_FORCE_INTERACTIVE", "1")

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_client"}`, 401)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("WS1_BASE_URL", srv.URL)

	stub := &StubPrompter{
		AskAnswers: map[string]string{
			"Tenant hostname":     "x",
			"Client ID":           "cid",
			"Client ID (retry)":   "cid2",
		},
		SecretAnswers: []string{"a", "b", "c"},
		PickIndex:     []int{2},
	}

	opts := SetupOptions{Profile: "operator", AuthURL: srv.URL + "/oauth", Quick: true}
	err := RunSetup(context.Background(), opts, stub)
	if err == nil {
		t.Fatal("expected error after 3 failed attempts")
	}
	if !strings.Contains(err.Error(), "auth") {
		t.Errorf("error = %v, want auth-related", err)
	}
}

func TestSetupOGFallbackOnPermissionDenied(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", cfg)
	t.Setenv("HOME", cfg)
	t.Setenv("WS1_ALLOW_DISK_SECRETS", "1")
	t.Setenv("WS1_FORCE_INTERACTIVE", "1")

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
	})
	mux.HandleFunc("/api/system/groups/search", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"forbidden"}`, 403)
	})
	mux.HandleFunc("/api/mdm/devices/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Devices":[],"Total":0}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("WS1_BASE_URL", srv.URL)

	stub := &StubPrompter{
		AskAnswers: map[string]string{
			"Tenant hostname": "x",
			"Client ID":       "cid",
			"OG ID":           "9999",
		},
		SecretAnswers: []string{"sec"},
		PickIndex:     []int{2},
	}
	opts := SetupOptions{Profile: "operator", AuthURL: srv.URL + "/oauth", Quick: true, SkipSmoke: true}
	if err := RunSetup(context.Background(), opts, stub); err != nil {
		t.Fatalf("RunSetup: %v", err)
	}
	og, _ := auth.CurrentOG()
	if og != "9999" {
		t.Errorf("og = %q, want 9999 (from fallback prompt)", og)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/ws1/... -run 'TestSetupOAuth|TestSetupOG' -v`
Expected: FAIL — wizard currently fails on first 401 with no retry; OG fallback works for 403 but the test exercises retries that don't exist.

- [ ] **Step 3: Modify** `cmd/ws1/setup.go` — replace the validation block (Step 5 in `RunSetup`) with a retry loop. Find:

```go
	// Step 5: Validate.
	if !opts.SkipValidate {
		spin := p.Spinner("Validating against " + opts.AuthURL + "...")
		client := api.New(auth.NewOAuthClient(&prof))
		_, err := client.Source.Token(ctx)
		if err != nil {
			spin.Done(false, "Auth failed: "+err.Error())
			return fmt.Errorf("auth: %w", err)
		}
		spin.Done(true, "Token issued")
	}
```

Replace with:

```go
	// Step 5: Validate (with up to 3 retries on auth failure).
	if !opts.SkipValidate {
		const maxAttempts = 3
		var lastErr error
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			spin := p.Spinner("Validating against " + opts.AuthURL + "...")
			client := api.New(auth.NewOAuthClient(&prof))
			_, err := client.Source.Token(ctx)
			if err == nil {
				spin.Done(true, "Token issued")
				lastErr = nil
				break
			}
			lastErr = err
			spin.Done(false, fmt.Sprintf("Auth failed (attempt %d/%d): %v", attempt, maxAttempts, err))
			if attempt == maxAttempts {
				break
			}
			// Re-prompt creds. Other fields keep their current values.
			newID, perr := p.Ask("Client ID (retry)", opts.ClientID)
			if perr != nil {
				return perr
			}
			newSec, perr := p.AskSecret("Client Secret (retry)")
			if perr != nil {
				return perr
			}
			opts.ClientID = newID
			opts.ClientSecret = newSec
			prof.ClientID = newID
			if err := auth.SaveProfile(prof); err != nil {
				return err
			}
			if err := auth.SaveClientSecret(profileName, newID, newSec); err != nil {
				return err
			}
		}
		if lastErr != nil {
			return fmt.Errorf("auth failed after %d attempts: %w", maxAttempts, lastErr)
		}
	}
```

- [ ] **Step 4: The OG-list fallback already works** (the existing pickOG already calls p.Ask("OG ID", "") on error), but verify the test passes by running it:

Run: `go test ./cmd/ws1/... -run TestSetupOGFallback -v`
Expected: PASS.

- [ ] **Step 5: Run all retry tests**

Run: `go test ./cmd/ws1/... -run 'TestSetupOAuth|TestSetupOG|TestSetupQuick' -v`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/ws1/setup.go cmd/ws1/setup_test.go
git commit -m "$(cat <<'EOF'
feat(cmd/setup): retry on auth failure (3 attempts) + OG fallback

OAuth validation now retries up to 3 times. Each failed attempt
re-prompts for client_id + client_secret (other fields keep their
current values). After three failures, exit with the last auth error.

OG list fetch fallback (already in pickOG) is now under test: when
the OG-search op returns 403, pickOG falls through to a freeform
"OG ID:" prompt and accepts any non-empty value.

Three new tests: oauth-retry-then-succeed, oauth-three-failures-exits,
og-fallback-on-403.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Setup wizard — `--advanced` multi-profile picker

**Files:**
- Modify: `cmd/ws1/setup.go` (advanced flow)
- Modify: `cmd/ws1/setup_test.go`

Spec §4.2: when `--advanced` is set, show a comma-separated picker for which profiles to configure (`ro,operator,admin`), then run the credential block per profile. After all profiles are configured, OG fetch runs once using the operator profile (preferred) → admin → ro.

- [ ] **Step 1: Write the failing test**:

```go
func TestSetupAdvancedConfiguresMultipleProfiles(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", cfg)
	t.Setenv("HOME", cfg)
	t.Setenv("WS1_ALLOW_DISK_SECRETS", "1")
	t.Setenv("WS1_FORCE_INTERACTIVE", "1")

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
	})
	mux.HandleFunc("/api/system/groups/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"LocationGroups":[{"Id":4067,"Uuid":"u","Name":"EMEA-Pilot"}],"Total":1}`))
	})
	mux.HandleFunc("/api/mdm/devices/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Devices":[],"Total":0}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("WS1_BASE_URL", srv.URL)

	stub := &StubPrompter{
		AskAnswers: map[string]string{
			"Tenant hostname":               "x",
			"Profiles to configure":         "operator,ro",
			"Client ID for operator":        "op-id",
			"Client ID for ro":              "ro-id",
		},
		SecretAnswers: []string{"op-sec", "ro-sec"},
		PickIndex:     []int{2 /*region na*/, 1 /*OG*/},
	}

	opts := SetupOptions{AuthURL: srv.URL + "/oauth", Quick: false, SkipSmoke: true}
	if err := RunSetup(context.Background(), opts, stub); err != nil {
		t.Fatalf("RunSetup: %v", err)
	}

	profiles, _ := auth.LoadProfiles()
	if len(profiles) != 2 {
		t.Fatalf("len(profiles) = %d, want 2", len(profiles))
	}
	names := map[string]bool{}
	for _, p := range profiles {
		names[p.Name] = true
	}
	if !names["operator"] || !names["ro"] {
		t.Errorf("profiles = %v, want operator+ro", names)
	}
	// Active profile defaults to ro when ro is in the set.
	active, _ := auth.Active()
	if active != "ro" {
		t.Errorf("active = %q, want ro", active)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ws1/... -run TestSetupAdvanced -v`
Expected: FAIL — Quick mode short-circuits to a single profile.

- [ ] **Step 3: Refactor `RunSetup`** in `cmd/ws1/setup.go`. Extract the per-profile credential + validation flow into a helper. Replace the entire body of `RunSetup` with:

```go
func RunSetup(ctx context.Context, opts SetupOptions, p Prompter) error {
	// Step 1: Tenant.
	tenant, err := promptIfEmpty(p, "Tenant hostname", opts.Tenant, "")
	if err != nil {
		return err
	}
	opts.Tenant = tenant

	// Step 2: Region (skipped if AuthURL was supplied directly).
	if opts.AuthURL == "" {
		if opts.Region == "" {
			region, err := pickRegion(p)
			if err != nil {
				return err
			}
			opts.Region = region
		}
		url, ok := regionToAuthURL(opts.Region)
		if !ok {
			return fmt.Errorf("unknown region %q", opts.Region)
		}
		opts.AuthURL = url
	}

	// Step 3: Profiles to configure.
	profileNames, err := selectProfilesToConfigure(p, opts)
	if err != nil {
		return err
	}

	// Step 4: For each profile, prompt for credentials and validate.
	configured := []auth.Profile{}
	for _, name := range profileNames {
		prof, err := configureOneProfile(ctx, p, opts, name)
		if err != nil {
			return err
		}
		configured = append(configured, prof)
	}

	// Step 5: OG selection. Use the most-privileged configured profile
	// for the OG fetch (operator > admin > ro), per spec §4.2.
	pickerProf := selectOGFetchProfile(configured)
	og, err := pickOG(ctx, p, &pickerProf, opts.OG)
	if err != nil {
		return err
	}
	if err := auth.SetOG(og); err != nil {
		return fmt.Errorf("save OG: %w", err)
	}

	// Step 6: Active profile. Prefer ro for safety; else operator;
	// else first configured (matches spec §4.2 + SKILL.md principle).
	if auth.IsInteractive() {
		active := selectActiveProfile(profileNames)
		if err := auth.SwitchActive(active, true); err != nil {
			return fmt.Errorf("set active: %w", err)
		}
	}

	// Step 7: Smoke test using the most-privileged profile.
	if !opts.SkipSmoke {
		runSmokeTest(ctx, p, &pickerProf)
	}
	return nil
}

// selectProfilesToConfigure returns the list of profile names to
// configure. Quick mode returns just opts.Profile (default operator).
// Advanced mode prompts for a comma-separated list.
func selectProfilesToConfigure(p Prompter, opts SetupOptions) ([]string, error) {
	if opts.Quick {
		name := opts.Profile
		if name == "" {
			name = "operator"
		}
		return []string{name}, nil
	}
	answer, err := p.Ask("Profiles to configure", "operator")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(answer, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		name := strings.TrimSpace(p)
		if name == "" {
			continue
		}
		if !auth.IsValidProfile(name) {
			return nil, fmt.Errorf("unknown profile %q (want one of ro/operator/admin)", name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		out = []string{"operator"}
	}
	return out, nil
}

// configureOneProfile prompts for one profile's credentials, validates
// them, persists them, and returns the resulting Profile. On
// validation failure, retries up to 3 times.
func configureOneProfile(ctx context.Context, p Prompter, opts SetupOptions, name string) (auth.Profile, error) {
	clientIDLabel := "Client ID"
	clientSecretLabel := "Client Secret"
	if !opts.Quick {
		clientIDLabel = "Client ID for " + name
		clientSecretLabel = "Client Secret for " + name
	}

	clientID := opts.ClientID
	clientSecret := opts.ClientSecret
	if !opts.Quick {
		clientID = ""
		clientSecret = ""
	}
	if clientID == "" {
		var err error
		clientID, err = p.Ask(clientIDLabel, "")
		if err != nil {
			return auth.Profile{}, err
		}
	}
	if clientSecret == "" {
		var err error
		clientSecret, err = p.AskSecret(clientSecretLabel)
		if err != nil {
			return auth.Profile{}, err
		}
	}

	prof := auth.Profile{
		Name: name, Tenant: opts.Tenant,
		APIURL: "https://" + opts.Tenant, AuthURL: opts.AuthURL,
		ClientID: clientID,
	}
	if err := auth.SaveProfile(prof); err != nil {
		return auth.Profile{}, err
	}
	if err := auth.SaveClientSecret(name, clientID, clientSecret); err != nil {
		return auth.Profile{}, err
	}

	if opts.SkipValidate {
		return prof, nil
	}
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		spin := p.Spinner(fmt.Sprintf("Validating %s against %s...", name, opts.AuthURL))
		client := api.New(auth.NewOAuthClient(&prof))
		_, err := client.Source.Token(ctx)
		if err == nil {
			spin.Done(true, "Token issued for "+name)
			return prof, nil
		}
		lastErr = err
		spin.Done(false, fmt.Sprintf("Auth failed (attempt %d/%d): %v", attempt, maxAttempts, err))
		if attempt == maxAttempts {
			break
		}
		newID, perr := p.Ask(clientIDLabel+" (retry)", clientID)
		if perr != nil {
			return auth.Profile{}, perr
		}
		newSec, perr := p.AskSecret(clientSecretLabel + " (retry)")
		if perr != nil {
			return auth.Profile{}, perr
		}
		clientID = newID
		clientSecret = newSec
		prof.ClientID = newID
		_ = auth.SaveProfile(prof)
		_ = auth.SaveClientSecret(name, newID, newSec)
	}
	return auth.Profile{}, fmt.Errorf("auth failed after %d attempts for %s: %w", maxAttempts, name, lastErr)
}

// selectOGFetchProfile picks operator > admin > ro from the configured
// list. Falls back to the first profile if none of those names is
// present (shouldn't happen — selectProfilesToConfigure validates).
func selectOGFetchProfile(profiles []auth.Profile) auth.Profile {
	for _, want := range []string{"operator", "admin", "ro"} {
		for _, p := range profiles {
			if p.Name == want {
				return p
			}
		}
	}
	return profiles[0]
}

// selectActiveProfile prefers ro (safer default; matches SKILL.md
// principle stack) then operator, else falls back to the first
// configured.
func selectActiveProfile(names []string) string {
	for _, want := range []string{"ro", "operator"} {
		for _, n := range names {
			if n == want {
				return n
			}
		}
	}
	return names[0]
}
```

Add `"strings"` to the import block at the top of the file.

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/ws1/... -run 'TestSetup' -v`
Expected: every TestSetup* PASS, including the new advanced test.

- [ ] **Step 5: Commit**

```bash
git add cmd/ws1/setup.go cmd/ws1/setup_test.go
git commit -m "$(cat <<'EOF'
feat(cmd/setup): --advanced multi-profile picker

ws1 setup --advanced prompts for a comma-separated list of profiles
to configure (ro, operator, admin), then runs the credential +
validation block once per profile. After all profiles are saved, the
OG fetch runs once using operator (preferred) → admin → ro per the
design's preference rule for highest-privilege OG-list access.

Active profile defaults to ro when ro is among the configured set
(safer for agent sessions; matches SKILL.md principle stack); else
operator; else first configured.

Refactor: per-profile flow extracted into configureOneProfile; the
existing Quick path uses the same helper with the unprefixed
"Client ID" / "Client Secret" labels.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Setup wizard — non-interactive mode + reconfigure

**Files:**
- Modify: `cmd/ws1/setup.go` (existing-config defaults + non-interactive branch)
- Modify: `cmd/ws1/setup_test.go`

Two related capabilities: bracketed defaults from existing config (re-running setup is the reconfigure path), and non-interactive bootstrap (CI provides every flag, no prompts, no active-profile change).

- [ ] **Step 1: Write the failing tests**:

```go
func TestSetupNonInteractiveAllFlags(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", cfg)
	t.Setenv("HOME", cfg)
	t.Setenv("WS1_ALLOW_DISK_SECRETS", "1")
	t.Setenv("WS1_FORCE_NONINTERACTIVE", "1")

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	opts := SetupOptions{
		Profile:      "operator",
		Tenant:       "x.example.com",
		AuthURL:      srv.URL + "/oauth",
		ClientID:     "cid",
		ClientSecret: "csec",
		OG:           "12345",
		Quick:        true,
		SkipSmoke:    true,
	}
	stub := &StubPrompter{} // never called
	if err := RunSetup(context.Background(), opts, stub); err != nil {
		t.Fatalf("RunSetup: %v", err)
	}
	// Profile written.
	profiles, _ := auth.LoadProfiles()
	if len(profiles) != 1 {
		t.Fatalf("profiles = %v", profiles)
	}
	// Active profile NOT changed under non-interactive (stays ro per
	// auth.Active default).
	active, _ := auth.Active()
	if active != "ro" {
		t.Errorf("active = %q, want ro (non-interactive must not flip)", active)
	}
	og, _ := auth.CurrentOG()
	if og != "12345" {
		t.Errorf("og = %q", og)
	}
	if len(stub.AskedLabels) != 0 {
		t.Errorf("non-interactive should not call Ask; saw %v", stub.AskedLabels)
	}
}

func TestSetupReconfigureUsesExistingAsDefault(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("WS1_CONFIG_DIR", cfg)
	t.Setenv("HOME", cfg)
	t.Setenv("WS1_ALLOW_DISK_SECRETS", "1")
	t.Setenv("WS1_FORCE_INTERACTIVE", "1")

	// Pre-seed an existing profile.
	_ = auth.SaveProfile(auth.Profile{
		Name: "operator", Tenant: "old.example.com",
		APIURL: "https://old.example.com",
		AuthURL: "https://na.uemauth.workspaceone.com/connect/token",
		ClientID: "old-cid",
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
	})
	mux.HandleFunc("/api/system/groups/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"LocationGroups":[{"Id":1,"Uuid":"u","Name":"Global"}],"Total":1}`))
	})
	mux.HandleFunc("/api/mdm/devices/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Devices":[],"Total":0}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("WS1_BASE_URL", srv.URL)

	// Stub: keep the existing tenant by sending empty input
	// (Ask returns the default when answer not in AskAnswers).
	stub := &StubPrompter{
		AskAnswers: map[string]string{
			"Client ID": "new-cid",
		},
		SecretAnswers: []string{"new-sec"},
		PickIndex:     []int{2 /*na*/, 1 /*OG*/},
	}

	opts := SetupOptions{Profile: "operator", AuthURL: srv.URL + "/oauth", Quick: true, SkipSmoke: true}
	if err := RunSetup(context.Background(), opts, stub); err != nil {
		t.Fatalf("RunSetup: %v", err)
	}

	profiles, _ := auth.LoadProfiles()
	if len(profiles) != 1 || profiles[0].Tenant != "old.example.com" {
		t.Errorf("expected existing tenant kept; got %+v", profiles)
	}
	// Verify the existing tenant was offered as the Ask default.
	found := false
	for _, l := range stub.AskedLabels {
		if l == "Tenant hostname" {
			found = true
		}
	}
	if !found {
		t.Errorf("Tenant hostname was never prompted; got labels %v", stub.AskedLabels)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/ws1/... -run 'TestSetupNonInteractive|TestSetupReconfigure' -v`
Expected: FAIL — non-interactive branch still flips active; reconfigure path doesn't read existing profile.

- [ ] **Step 3: Modify** `cmd/ws1/setup.go` to honor existing config + non-interactive guard. Replace the start of `RunSetup` (the comment above Step 1) with:

```go
func RunSetup(ctx context.Context, opts SetupOptions, p Prompter) error {
	// Pre-fill from existing config (if any) so re-running setup
	// becomes the reconfigure path. Existing values take precedence
	// only when opts is empty for that field.
	preFillFromExisting(&opts)

	// Step 1: Tenant.
```

Then add the `preFillFromExisting` helper at the bottom of the file:

```go
// preFillFromExisting reads the existing profile (if any) named
// opts.Profile (default "operator") and copies its tenant + auth_url
// + client_id into opts when those fields are empty. Acts as the
// reconfigure-friendly default-seeding step.
func preFillFromExisting(opts *SetupOptions) {
	name := opts.Profile
	if name == "" {
		name = "operator"
	}
	prof, err := auth.FindProfile(name)
	if err != nil || prof == nil {
		return
	}
	if opts.Tenant == "" {
		opts.Tenant = prof.Tenant
	}
	if opts.AuthURL == "" {
		opts.AuthURL = prof.AuthURL
	}
	if opts.ClientID == "" {
		opts.ClientID = prof.ClientID
	}
}
```

Then change the Tenant prompt in step 1 of RunSetup to pass the existing value as the default:

Find:

```go
	tenant, err := promptIfEmpty(p, "Tenant hostname", opts.Tenant, "")
```

Replace with:

```go
	tenant, err := p.Ask("Tenant hostname", opts.Tenant)
```

(`Ask` already returns the default when input is empty; this lets the user `<enter>` to keep the existing value when reconfiguring.)

Wait — but we also need to skip the prompt when `opts.Tenant` was supplied via flag in non-interactive mode. The cobra Run-func already validates required flags non-interactively; here we know either `opts.Tenant` came from flag or from preFill. Distinguishing the two is awkward. Cleanest: pass `opts.Tenant` as the prompt's default value when it's set, regardless of source:

```go
	if opts.Tenant != "" && !auth.IsInteractive() {
		// Non-interactive: keep the value as-is.
	} else {
		tenant, err := p.Ask("Tenant hostname", opts.Tenant)
		if err != nil {
			return err
		}
		opts.Tenant = tenant
	}
```

Apply the same gating to the Region prompt (only prompt in interactive mode), and to the credential prompts (already gated by `if clientID == ""`).

Replace the entire region block:

```go
	// Step 2: Region (skipped if AuthURL was supplied directly).
	if opts.AuthURL == "" {
		if opts.Region == "" {
			region, err := pickRegion(p)
			if err != nil {
				return err
			}
			opts.Region = region
		}
		url, ok := regionToAuthURL(opts.Region)
		if !ok {
			return fmt.Errorf("unknown region %q", opts.Region)
		}
		opts.AuthURL = url
	}
```

with:

```go
	// Step 2: Region (only prompt interactively; non-interactive must
	// supply --region or --auth-url).
	if opts.AuthURL == "" {
		if opts.Region == "" {
			if !auth.IsInteractive() {
				return fmt.Errorf("non-interactive: --region or --auth-url required")
			}
			region, err := pickRegion(p)
			if err != nil {
				return err
			}
			opts.Region = region
		}
		url, ok := regionToAuthURL(opts.Region)
		if !ok {
			return fmt.Errorf("unknown region %q", opts.Region)
		}
		opts.AuthURL = url
	}
```

And the active-profile step (step 6 in the refactored RunSetup): the existing code already gates it on `auth.IsInteractive()`, so non-interactive callers correctly skip it. No change needed.

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/ws1/... -run 'TestSetup' -v`
Expected: every TestSetup* PASS.

- [ ] **Step 5: Smoke-build the binary by hand and check the help text**:

Run:
```
go build -o /tmp/ws1 ./cmd/ws1 && /tmp/ws1 setup --help | head -20
```
Expected: shows "ws1 setup walks through the first-run configuration..." plus all flags including `--advanced`, `--skip-validate`, `--skip-smoke-test`.

- [ ] **Step 6: Commit**

```bash
git add cmd/ws1/setup.go cmd/ws1/setup_test.go
git commit -m "$(cat <<'EOF'
feat(cmd/setup): non-interactive mode + reconfigure-from-existing

Two related additions:

1. preFillFromExisting copies tenant + auth_url + client_id from the
   existing profile (if any) into SetupOptions, so re-running setup
   offers the prior values as bracketed defaults. <enter> keeps each.
   Setup is now the install path AND the reconfigure path.

2. Non-interactive mode (WS1_FORCE_NONINTERACTIVE=1 or no TTY): every
   required value must come from a flag. Prompts are skipped. Active
   profile is NOT flipped — CLAUDE.md decision #5 is strict; CI must
   follow up with `ws1 profile use <name>` from a terminal.

Two new tests cover both paths.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Integration test + documentation polish

**Files:**
- Create: `test/integration/setup_test.go`
- Modify: `docs/superpowers/specs/2026-04-30-ws1-setup-onboarding-design.md` (mark status implemented)
- Modify: `cmd/ws1/setup.go` (final exit summary block)

End-to-end: real binary, real mock, real stdin pipe. Confirms the wizard works against a server that looks like WS1.

- [ ] **Step 1: Add the exit summary block** to `cmd/ws1/setup.go`. Find the very end of `RunSetup` (after the smoke test):

```go
	// Step 7: Smoke test using the most-privileged profile.
	if !opts.SkipSmoke {
		runSmokeTest(ctx, p, &pickerProf)
	}
	return nil
}
```

Replace with:

```go
	// Step 7: Smoke test using the most-privileged profile.
	if !opts.SkipSmoke {
		runSmokeTest(ctx, p, &pickerProf)
	}

	printExitSummary(profileNames, configured, og)
	return nil
}

// printExitSummary writes the final "Setup complete" block to stderr.
// Stays out of stdout so emitAndExit's envelope remains the only line
// on stdout for downstream parsers.
func printExitSummary(profileNames []string, configured []auth.Profile, og string) {
	if len(configured) == 0 {
		return
	}
	tenant := configured[0].Tenant
	active, _ := auth.Active()
	fmt.Fprintln(stderrWriter, "──────────────────────────────────────────────────")
	fmt.Fprintln(stderrWriter, "Setup complete.")
	fmt.Fprintln(stderrWriter)
	fmt.Fprintf(stderrWriter, "  Profiles configured: %s\n", strings.Join(profileNames, ", "))
	fmt.Fprintf(stderrWriter, "  Active profile:      %s\n", active)
	fmt.Fprintf(stderrWriter, "  Tenant:              %s\n", tenant)
	if og != "" {
		fmt.Fprintf(stderrWriter, "  OG context:          %s\n", og)
	}
	fmt.Fprintln(stderrWriter)
	fmt.Fprintln(stderrWriter, "Try:")
	fmt.Fprintln(stderrWriter, "  ws1 doctor")
	fmt.Fprintln(stderrWriter, "  ws1 ops list | jq '.data.count'")
	fmt.Fprintln(stderrWriter, "  ws1 mdmv1 devices search --pagesize 5")
	fmt.Fprintln(stderrWriter, "──────────────────────────────────────────────────")
}
```

- [ ] **Step 2: Write the integration test** at `test/integration/setup_test.go`:

```go
package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xyzbuilds/ws1-uem-agent/internal/envelope"
	"github.com/xyzbuilds/ws1-uem-agent/test/mockws1"
)

func TestSetupIntegrationAgainstMock(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "ws1")
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/ws1")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}
	srv := mockws1.New().Start()
	defer srv.Close()

	cfg := filepath.Join(tmp, "ws1cfg")
	_ = os.MkdirAll(cfg, 0o700)

	// Drive the binary in non-interactive mode so we don't need a PTY.
	c := exec.Command(bin, "setup",
		"--profile", "operator",
		"--tenant", "demo.awmdm.com",
		"--auth-url", srv.URL+"/oauth",
		"--client-id", "cid",
		"--client-secret", "csec",
		"--og", "4067",
		"--skip-smoke-test",
	)
	c.Env = append(os.Environ(),
		"WS1_BASE_URL="+srv.URL,
		"WS1_CONFIG_DIR="+cfg,
		"WS1_ALLOW_DISK_SECRETS=1",
		"WS1_FORCE_NONINTERACTIVE=1",
		"HOME="+cfg,
	)
	var out, errOut bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errOut
	if err := c.Run(); err != nil {
		t.Fatalf("setup: %v\nstdout: %s\nstderr: %s", err, out.String(), errOut.String())
	}

	// Parse stdout envelope.
	stdout := strings.TrimSpace(out.String())
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout: %s", err, stdout)
	}
	if !env.OK {
		t.Errorf("ok=false: %+v", env.Error)
	}
	if env.Operation != "ws1.setup" {
		t.Errorf("op = %q", env.Operation)
	}

	// Disk side-effects.
	mustHaveProfileFile(t, cfg, "operator")
	if og := mustReadFile(t, filepath.Join(cfg, "og")); og != "4067\n" {
		t.Errorf("og = %q", og)
	}
}

// TestSetupIntegrationOAuthRoundTrip drives an interactive-style wizard
// via stdin pipe. Exercises the OAuth round-trip (no --skip-validate)
// and the OG list fetch.
func TestSetupIntegrationOAuthRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "ws1")
	if err := exec.Command("go", "build", "-o", bin, "../../cmd/ws1").Run(); err != nil {
		t.Fatalf("build: %v", err)
	}
	srv := mockws1.New().Start()
	defer srv.Close()

	cfg := filepath.Join(tmp, "ws1cfg")
	_ = os.MkdirAll(cfg, 0o700)

	c := exec.Command(bin, "setup",
		"--profile", "operator",
		"--auth-url", srv.URL+"/oauth",
		"--skip-smoke-test",
	)
	c.Env = append(os.Environ(),
		"WS1_BASE_URL="+srv.URL,
		"WS1_CONFIG_DIR="+cfg,
		"WS1_ALLOW_DISK_SECRETS=1",
		"WS1_FORCE_INTERACTIVE=1",
		"HOME="+cfg,
	)
	stdin, _ := c.StdinPipe()
	var out, errOut bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errOut
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wizard prompts: tenant, region, client id, client secret, og pick.
	// Mock OG list returns 3 entries; we pick the third (EMEA-Pilot).
	_, _ = io.WriteString(stdin, "demo.awmdm.com\n")
	_, _ = io.WriteString(stdin, "2\n")           // region: na
	_, _ = io.WriteString(stdin, "cid\n")
	_, _ = io.WriteString(stdin, "csec\n")        // tests AskSecret on pipe
	_, _ = io.WriteString(stdin, "3\n")           // OG: EMEA-Pilot
	_ = stdin.Close()

	if err := c.Wait(); err != nil {
		t.Fatalf("setup failed: %v\nstdout: %s\nstderr: %s", err, out.String(), errOut.String())
	}
	if og := strings.TrimSpace(mustReadFile(t, filepath.Join(cfg, "og"))); og != "4067" {
		t.Errorf("og = %q, want 4067 (EMEA-Pilot)", og)
	}
}

func mustHaveProfileFile(t *testing.T, cfg, name string) {
	t.Helper()
	b := mustReadFile(t, filepath.Join(cfg, "profiles.yaml"))
	if !strings.Contains(b, "name: "+name) {
		t.Errorf("profiles.yaml missing %q\nbody: %s", name, b)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
```

- [ ] **Step 3: Run the integration tests**

Run: `go test ./test/integration/... -v -run TestSetupIntegration`
Expected: both `TestSetupIntegrationAgainstMock` and `TestSetupIntegrationOAuthRoundTrip` PASS.

- [ ] **Step 4: Run the full test suite + lint**

Run:
```
go test ./...
go vet ./...
golangci-lint run
```
Expected: all PASS / 0 issues.

- [ ] **Step 5: Update the spec status header**. Open `docs/superpowers/specs/2026-04-30-ws1-setup-onboarding-design.md` and find:

```markdown
**Status:** Approved for implementation, 2026-04-30.
```

Replace with:

```markdown
**Status:** Implemented (commit `<latest commit sha>`), 2026-04-30.
```

(Use `git log --oneline -1` to get the latest sha and substitute it.)

- [ ] **Step 6: Commit**

```bash
git add cmd/ws1/setup.go test/integration/setup_test.go docs/superpowers/specs/2026-04-30-ws1-setup-onboarding-design.md
git commit -m "$(cat <<'EOF'
feat(cmd/setup): exit summary + integration tests

Final pieces of the onboarding wizard:

1. Exit summary block printed to stderr after a successful run.
   Lists configured profiles, active profile, tenant, OG, then a
   "Try:" hint with three concrete commands. Stays on stderr so the
   stdout envelope remains the only stdout line for parsers.

2. Two integration tests in test/integration/setup_test.go:
   - non-interactive bootstrap (every flag supplied, no TTY)
   - interactive flow over stdin pipe (drives tenant + region pick
     + creds + OG pick)
   Both confirm disk side-effects: profiles.yaml has the entry,
   og file has the picked id, stdout envelope ok=true.

Spec doc status flipped from Approved to Implemented.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review

**Spec coverage check:**

| Spec section | Covered by |
|---|---|
| §3 Q1 (`ws1 setup` shape) | Tasks 6 + register in root.go |
| §3 Q2 (multi-profile picker behind `--advanced`) | Task 8 |
| §3 Q3 (validation + OG fetch + fallback) | Tasks 6 + 7 |
| §4.1 Quick / Advanced split | Tasks 6 + 8 |
| §4.2 wizard flow walkthrough | Tasks 6 + 8 (advanced flow) + 10 (exit block) |
| §4.3 spinner Braille / ASCII fallback | Task 3 |
| §4.3 single-char shortcut UI | Task 1 (`PickByLetter`) — actual usage in re-run-overwrite path lives in task 9's reconfigure flow |
| §4.4 code touch points | All tasks |
| §4.5 OG-list op + fallback | Task 6's `fetchOGList` |
| §4.6 error fallback table | Task 7 |
| §4.7 non-interactive mode | Task 9 |
| §4.8 `ws1 status` companion | Task 5 |
| §4.9 testing | Tasks 1–10 (each task TDD; task 10 integration) |

**Gap:** Spec §4.6 mentions the "Profile <name> exists. [O]verwrite / [k]eep / [c]ancel" prompt for re-runs. Current plan doesn't wire `PickByLetter` into the reconfigure flow — `preFillFromExisting` silently re-uses values. **Decision:** for v0.1, silent reuse is the simpler path and matches the bracketed-default pattern used elsewhere; the explicit overwrite prompt is parked. Add a note in the spec's open-questions section if it surfaces as a real friction. (Plan unchanged; calling this out explicitly so the implementer knows it's intentional.)

**Placeholder scan:** none.

**Type consistency:** `Prompter`, `PickItem`, `ByLetterItem`, `Spinner`, `SetupOptions`, `RunSetup` all defined once and referenced consistently. `OrgGroup`, `ogRow`, and the JSON `LocationGroups` field name match across mock + wizard + tests.

**Scope:** one feature, one wizard, one companion command. Bounded. 10 tasks, each independently committable.

---

## Sub-decisions resolved during planning

1. **OG-list op identifier (priority):** `systemv2.organizationgroups.organizationgroupsearch` → `systemv1.organizationgroups.locationgroupsearch`. Both at `GET /api/system/groups/search`. Wizard tries each in order via `generated.Ops` lookup.

2. **Spinner UTF-8 detection:** check `LC_ALL`, `LC_CTYPE`, `LANG` env (in that order, first non-empty wins) for substring `UTF-8` or `UTF8` (case-insensitive). Braille frames `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏` if matched, ASCII `| / - \` otherwise.

3. **Mock OG fixtures:** Global (id 1, uuid `70a00000-0000-0000-0000-000000000001`), EMEA (id 2042, uuid `8b300000-0000-0000-0000-000000000001`), EMEA-Pilot (id 4067, uuid `c9100000-0000-0000-0000-000000000001`).

4. **Re-run overwrite prompt vs silent reuse:** silent reuse for v0.1 (bracketed defaults; `<enter>` keeps existing). Explicit `[O]verwrite/[k]eep/[c]ancel` left as a follow-up if friction surfaces.
