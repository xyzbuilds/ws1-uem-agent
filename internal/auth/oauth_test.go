package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOAuthClientFetchesToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if g := r.PostForm.Get("grant_type"); g != "client_credentials" {
			t.Errorf("grant_type = %q", g)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-123",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &OAuthClient{
		Profile: &Profile{
			Name: ProfileOperator, Tenant: "fake", APIURL: srv.URL,
			AuthURL: srv.URL + "/oauth", ClientID: "cid",
		},
		HTTP:      srv.Client(),
		clientSec: "csec",
	}
	tok, err := c.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.AccessToken != "tok-123" {
		t.Errorf("AccessToken = %q", tok.AccessToken)
	}
}

func TestOAuthClientCachesUntilExpiry(t *testing.T) {
	hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &OAuthClient{
		Profile:   &Profile{Name: ProfileOperator, AuthURL: srv.URL + "/oauth", ClientID: "cid"},
		HTTP:      srv.Client(),
		clientSec: "x",
	}
	for i := 0; i < 3; i++ {
		_, err := c.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
	}
	if hits != 1 {
		t.Errorf("token endpoint hit %d times; expected 1 due to cache", hits)
	}
}

func TestTokenExpiredEdge(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		t    *Token
		want bool
	}{
		{"nil", nil, true},
		{"empty access token", &Token{}, true},
		{"fresh", &Token{AccessToken: "x", ExpiresIn: 3600, ObtainedAt: now}, false},
		{"60s before expiry counts as expired", &Token{AccessToken: "x", ExpiresIn: 30, ObtainedAt: now}, true},
		{"way past", &Token{AccessToken: "x", ExpiresIn: 60, ObtainedAt: now.Add(-time.Hour)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.t.Expired(); got != tc.want {
				t.Errorf("Expired = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMockTokenSourceShortcut(t *testing.T) {
	t.Setenv("WS1_MOCK_TOKEN", "mock-tok")
	c := NewOAuthClient(&Profile{Name: ProfileOperator, ClientID: "cid"})
	tok, err := c.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.AccessToken != "mock-tok" {
		t.Errorf("AccessToken = %q, want mock-tok", tok.AccessToken)
	}
}

func TestBaseURLOverride(t *testing.T) {
	t.Setenv("WS1_BASE_URL", "http://127.0.0.1:9999")
	c := NewOAuthClient(&Profile{APIURL: "https://prod.example.com"})
	if got := c.BaseURL(); got != "http://127.0.0.1:9999" {
		t.Errorf("BaseURL = %q", got)
	}
}
