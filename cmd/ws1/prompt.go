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
