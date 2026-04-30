// Package api wraps the WS1 REST API. Given an operation name from the
// compiled-in catalog (internal/generated.Ops) and an args map, it builds
// and executes the HTTP request and returns the raw response.
//
// The caller (cmd/ws1) is responsible for parsing the response body shape
// and emitting the correct envelope flavor. This package stays generic so
// new operations don't need a new wrapper function — they just need a
// metadata row in ops_index.go.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xyzbuilds/ws1-uem-agent/internal/auth"
	"github.com/xyzbuilds/ws1-uem-agent/internal/generated"
)

// Args is the binding from a parameter name (matching ParamMeta.Name) to
// its runtime value. Path parameters must be present; query parameters are
// optional and skipped if missing.
type Args map[string]any

// Response is the unparsed HTTP outcome.
type Response struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// JSON unmarshals the body into target. Convenience wrapper.
func (r *Response) JSON(target any) error {
	if len(r.Body) == 0 {
		return nil
	}
	return json.Unmarshal(r.Body, target)
}

// Client holds the shared HTTP client + token source.
type Client struct {
	Source auth.TokenSource
	HTTP   *http.Client

	// AcceptVersion is the WS1 API version requested via the
	// `Accept: application/json;version=<N>` content negotiation. v1 endpoints
	// only respond when version=1; v2 endpoints want version=2. The CLI
	// resolves this per-op by inspecting the section slug at request time.
	AcceptVersion string
}

// New constructs a Client with a sensible default HTTP timeout.
func New(src auth.TokenSource) *Client {
	return &Client{
		Source:        src,
		HTTP:          &http.Client{Timeout: 30 * time.Second},
		AcceptVersion: "2",
	}
}

// MaxRetryAfterWait caps how long the rate-limit retry will sleep before
// giving up. WS1's docs aren't always specific about Retry-After upper
// bounds; we don't want a runaway header value to hang the CLI.
const MaxRetryAfterWait = 30 * time.Second

// Do executes the named op with the given args. Network errors return
// (nil, err); HTTP-level errors (4xx/5xx) are surfaced via Response.StatusCode
// so callers can map them to the correct envelope error code.
//
// Rate limit handling: on HTTP 429, sleep up to MaxRetryAfterWait honoring
// the Retry-After header, then retry once. If still 429, return the 429
// response so the caller can map it to RATE_LIMITED. Per the user's
// principle: honor rate limit; don't pile on with repeated retries.
func (c *Client) Do(ctx context.Context, op string, args Args) (*Response, error) {
	resp, err := c.doOnce(ctx, op, args)
	if err != nil || resp.StatusCode != http.StatusTooManyRequests {
		return resp, err
	}
	wait := retryAfterDuration(resp.Headers)
	if wait > MaxRetryAfterWait {
		wait = MaxRetryAfterWait
	}
	if wait <= 0 {
		// Header missing or unparseable — fall through to single retry
		// with a small fixed backoff.
		wait = 2 * time.Second
	}
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		return resp, ctx.Err()
	}
	return c.doOnce(ctx, op, args)
}

// doOnce performs one HTTP attempt without rate-limit retries. Do() wraps
// it so the retry policy is in one place.
func (c *Client) doOnce(ctx context.Context, op string, args Args) (*Response, error) {
	meta, ok := generated.Ops[op]
	if !ok {
		return nil, fmt.Errorf("api: unknown op %q", op)
	}
	u, err := buildURL(c.Source.BaseURL(), meta, args)
	if err != nil {
		return nil, err
	}

	var bodyReader io.Reader
	if meta.HasRequestBody {
		body := buildBody(meta, args)
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("api: marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, meta.HTTPMethod, u, bodyReader)
	if err != nil {
		return nil, err
	}
	tok, err := c.Source.Token(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", tok.TokenType+" "+tok.AccessToken)
	// Per Omnissa REST API conventions, API version is in the Accept
	// content-type parameter. With OAuth client-credentials the bearer is
	// sufficient identity — `aw-tenant-code` is only needed for Basic Auth,
	// which v1 doesn't support.
	if c.AcceptVersion != "" {
		req.Header.Set("Accept", "application/json;version="+c.AcceptVersion)
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if meta.HasRequestBody {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &Response{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		Body:       respBody,
	}, nil
}

// retryAfterDuration parses the Retry-After header per RFC 7231 §7.1.3:
// either an integer (seconds) or an HTTP-date. We support the common
// integer form and a small subset of relative date forms by punting to
// 0 on parse failure (caller falls back to a fixed wait).
func retryAfterDuration(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	// Try integer seconds first — overwhelmingly the common form.
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	// HTTP-date form — let the caller fall back; date-shaped values
	// rarely appear in practice and the simple wait is fine.
	return 0
}

// buildURL composes the absolute URL from base + meta.BasePath + path
// template + query params.
//
//	base + base_path + filled-path-template + ?<query string>
func buildURL(baseURL string, meta generated.OpMeta, args Args) (string, error) {
	if baseURL == "" {
		return "", errors.New("api: base URL is empty")
	}
	pathTpl := meta.PathTemplate
	for _, p := range meta.Parameters {
		if p.In != "path" {
			continue
		}
		v, ok := args[p.Name]
		if !ok {
			return "", fmt.Errorf("api: missing required path parameter %q", p.Name)
		}
		pathTpl = strings.ReplaceAll(pathTpl, "{"+p.Name+"}", url.PathEscape(toStr(v)))
	}
	full := strings.TrimRight(baseURL, "/") + meta.BasePath + pathTpl

	q := url.Values{}
	for _, p := range meta.Parameters {
		if p.In != "query" {
			continue
		}
		v, ok := args[p.Name]
		if !ok || isEmpty(v) {
			continue
		}
		q.Set(p.Name, toStr(v))
	}
	if encoded := q.Encode(); encoded != "" {
		full += "?" + encoded
	}
	return full, nil
}

// buildBody picks every arg whose name is NOT a declared path/query param
// and packages them into the JSON request body. This way the CLI surface
// can stay flat (every arg is a flag) and the binding decides where each
// arg goes.
func buildBody(meta generated.OpMeta, args Args) map[string]any {
	declared := map[string]bool{}
	for _, p := range meta.Parameters {
		declared[p.Name] = true
	}
	out := map[string]any{}
	for k, v := range args {
		if declared[k] {
			continue
		}
		out[k] = v
	}
	return out
}

func toStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case nil:
		return true
	}
	return false
}
