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
