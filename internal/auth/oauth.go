package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Token is a bearer token + lifecycle metadata. Tokens are NOT persisted to
// disk in v0 — they live in the CLI process memory for the duration of one
// invocation, matching the "single-shot CLI process" model from spec
// section 2.
type Token struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresIn   int       `json:"expires_in"` // seconds, from server
	ObtainedAt  time.Time `json:"-"`
}

// Expired returns true if the token has crossed its expiration window
// minus a 60s safety margin.
func (t *Token) Expired() bool {
	if t == nil || t.AccessToken == "" {
		return true
	}
	exp := t.ObtainedAt.Add(time.Duration(t.ExpiresIn-60) * time.Second)
	return time.Now().After(exp)
}

// TokenSource is anything that can yield a current bearer token. In tests
// we substitute a mock; in production it's *OAuthClient.
type TokenSource interface {
	Token(ctx context.Context) (*Token, error)
	BaseURL() string
	// TenantCode returns the value to send in the `aw-tenant-code` header
	// on every API request; empty string is acceptable for tests/mock mode.
	TenantCode() string
}

// OAuthClient implements TokenSource for the WS1 client-credentials grant.
type OAuthClient struct {
	Profile *Profile
	HTTP    *http.Client

	mu        sync.Mutex
	cached    *Token
	clientSec string
}

// NewOAuthClient constructs a client. The client_secret is fetched lazily
// via the keychain so a non-interactive invocation that doesn't actually
// need the secret (e.g. `ws1 --version`) doesn't trigger a keychain prompt.
func NewOAuthClient(p *Profile) *OAuthClient {
	return &OAuthClient{
		Profile: p,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// BaseURL returns the API base URL for this profile, honouring WS1_BASE_URL
// for tests and the demo's mock server.
func (c *OAuthClient) BaseURL() string {
	if v := os.Getenv("WS1_BASE_URL"); v != "" {
		return v
	}
	return strings.TrimRight(c.Profile.APIURL, "/")
}

// TenantCode returns the aw-tenant-code header value for this profile.
// Empty if not set.
func (c *OAuthClient) TenantCode() string {
	if v := os.Getenv("WS1_TENANT_CODE"); v != "" {
		return v
	}
	if c.Profile == nil {
		return ""
	}
	return c.Profile.TenantCode
}

// Token returns a non-expired bearer token, fetching a new one as needed.
//
// Mock-mode shortcut: if WS1_MOCK_TOKEN is set, return it without hitting
// any auth URL. This is what the demo and tests use to bypass real OAuth.
func (c *OAuthClient) Token(ctx context.Context) (*Token, error) {
	if v := os.Getenv("WS1_MOCK_TOKEN"); v != "" {
		return &Token{
			AccessToken: v,
			TokenType:   "Bearer",
			ExpiresIn:   3600,
			ObtainedAt:  time.Now(),
		}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached != nil && !c.cached.Expired() {
		return c.cached, nil
	}
	if c.clientSec == "" {
		s, err := GetClientSecret(c.Profile.Name, c.Profile.ClientID)
		if err != nil {
			return nil, err
		}
		c.clientSec = s
	}
	t, err := c.fetchToken(ctx)
	if err != nil {
		return nil, err
	}
	c.cached = t
	return t, nil
}

func (c *OAuthClient) fetchToken(ctx context.Context) (*Token, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", c.Profile.ClientID)
	form.Set("client_secret", c.clientSec)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Profile.AuthURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("oauth token: status %d: %s", resp.StatusCode, truncate(string(body), 256))
	}
	var t Token
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("oauth token: parse: %w", err)
	}
	t.ObtainedAt = time.Now()
	return &t, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// MockTokenSource is a TokenSource that returns the same token forever; used
// by tests and the demo runner.
type MockTokenSource struct {
	BaseURLValue    string
	TokenValue      string
	TenantCodeValue string
}

// Token implements TokenSource.
func (m *MockTokenSource) Token(ctx context.Context) (*Token, error) {
	return &Token{
		AccessToken: m.TokenValue,
		TokenType:   "Bearer",
		ExpiresIn:   3600,
		ObtainedAt:  time.Now(),
	}, nil
}

// BaseURL implements TokenSource.
func (m *MockTokenSource) BaseURL() string { return m.BaseURLValue }

// TenantCode implements TokenSource.
func (m *MockTokenSource) TenantCode() string { return m.TenantCodeValue }
