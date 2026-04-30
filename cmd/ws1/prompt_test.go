package main

import (
	"errors"
	"io"
	"os"
	"strings"
	"sync"
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

// driveTTYPrompter writes input to an in-process pipe wired to a
// TTYPrompter, runs fn (which calls Ask/Pick/etc.), and returns
// captured output for assertion.
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
