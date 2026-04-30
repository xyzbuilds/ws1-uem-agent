package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
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
	PickIndex     []int // 1-indexed
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

func (s *stubSpinner) Done(_ bool, _ string) {
	// no-op: stub captures only the label
}

func (s *StubPrompter) Spinner(label string) Spinner {
	s.Spins = append(s.Spins, label)
	return &stubSpinner{parent: s, label: label}
}

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
	// reader is lazily initialized on first use and reused across calls
	// so that its internal buffer survives across multiple Ask/Pick calls
	// on the same Prompter (critical for Pick's retry loop).
	reader *bufio.Reader
}

// NewTTYPrompter wires stdin + stderr.
func NewTTYPrompter() *TTYPrompter {
	return &TTYPrompter{In: os.Stdin, Out: os.Stderr}
}

// r returns the shared bufio.Reader, creating it on first call.
func (p *TTYPrompter) r() *bufio.Reader {
	if p.reader == nil {
		p.reader = bufio.NewReader(p.In)
	}
	return p.reader
}

func (p *TTYPrompter) Ask(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.Out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.Out, "%s: ", label)
	}
	line, err := p.r().ReadString('\n')
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
	line, err := p.r().ReadString('\n')
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
		line, err := p.r().ReadString('\n')
		if err != nil && line == "" {
			return PickItem{}, err
		}
		line = strings.TrimSpace(line)
		n, convErr := strconv.Atoi(line)
		if convErr != nil || n < 1 || n > len(options) {
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

func (n *noopSpinner) Done(_ bool, _ string) {}
