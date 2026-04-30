package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

// fetch pulls each section's OpenAPI document, validates the shape,
// pretty-prints the JSON for diff-friendliness, and writes
// spec/<slug>.json plus an updated spec/VERSION.
//
// Per docs/build-pipeline.md stage 2, JSON keys are NOT sorted (preserves
// OpenAPI document order). Pretty-printing is the only normalisation.
//
// Auth note: WS1's `/api/help/Docs/<slug>` endpoints are publicly readable
// — same as the index page they're linked from. The --token / WS1_TOKEN
// is therefore optional. If supplied, it's sent as a Bearer header so
// tenants that have locked down the explorer can still be served. If a
// 401 is returned, fetch surfaces FETCH_AUTH_FAILED so the maintainer
// knows to provide a token.
func newFetchCmd() *cobra.Command {
	var sectionsPath, token, outDir string
	var retries int
	cmd := &cobra.Command{
		Use:   "fetch",
		Short: "Pull each section's OpenAPI spec into spec/",
		RunE: func(cmd *cobra.Command, args []string) error {
			disc, err := loadDiscoveryResult(sectionsPath)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				return err
			}
			versionEntries, err := fetchAllSections(disc, token, outDir, retries)
			if err != nil {
				return err
			}
			versionFile := struct {
				Tenant             string         `yaml:"tenant"`
				APIExplorerVersion string         `yaml:"api_explorer_version"`
				FetchedAt          string         `yaml:"fetched_at"`
				Sections           []sectionEntry `yaml:"sections"`
			}{
				Tenant:             disc.Tenant,
				APIExplorerVersion: disc.APIExplorerVersion,
				FetchedAt:          time.Now().UTC().Format(time.RFC3339),
				Sections:           versionEntries,
			}
			b, err := yaml.Marshal(versionFile)
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(outDir, "VERSION"), b, 0o644)
		},
	}
	cmd.Flags().StringVar(&sectionsPath, "sections", ".build/sections.json", "discovery output from `ws1-build discover`")
	cmd.Flags().StringVar(&token, "token", os.Getenv("WS1_TOKEN"), "optional bearer for tenants that lock down /api/help; defaults to $WS1_TOKEN")
	cmd.Flags().StringVar(&outDir, "out", "spec/", "output directory for spec files and VERSION")
	cmd.Flags().IntVar(&retries, "retries", 3, "max retries on transient network errors")
	return cmd
}

type sectionEntry struct {
	Slug   string `yaml:"slug"`
	SHA256 string `yaml:"sha256"`
}

func loadDiscoveryResult(path string) (*DiscoveryResult, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sections file %s: %w", path, err)
	}
	var d DiscoveryResult
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("parse sections file %s: %w", path, err)
	}
	return &d, nil
}

func fetchAllSections(d *DiscoveryResult, token, outDir string, retries int) ([]sectionEntry, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	out := make([]sectionEntry, 0, len(d.Sections))
	for _, s := range d.Sections {
		body, err := fetchOne(client, s.SpecURL, token, retries)
		if err != nil {
			return nil, err
		}
		// Validate shape: re-marshal pretty.
		var generic map[string]any
		if err := json.Unmarshal(body, &generic); err != nil {
			return nil, fmt.Errorf("FETCH_INVALID_FORMAT: %s: %w", s.SpecURL, err)
		}
		if v, _ := generic["openapi"].(string); !strings.HasPrefix(v, "3.0") {
			return nil, fmt.Errorf("FETCH_INVALID_FORMAT: %s: openapi version %q not 3.0.x", s.SpecURL, v)
		}
		paths, _ := generic["paths"].(map[string]any)
		if len(paths) == 0 {
			return nil, fmt.Errorf("FETCH_INVALID_FORMAT: %s: empty paths", s.SpecURL)
		}
		pretty, err := json.MarshalIndent(generic, "", "  ")
		if err != nil {
			return nil, err
		}
		pretty = append(pretty, '\n')
		path := filepath.Join(outDir, s.Slug+".json")
		if err := os.WriteFile(path, pretty, 0o644); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(pretty)
		out = append(out, sectionEntry{Slug: s.Slug, SHA256: hex.EncodeToString(sum[:])})
		fmt.Fprintf(os.Stderr, "fetch: %s (%d bytes)\n", path, len(pretty))
	}
	return out, nil
}

func fetchOne(client *http.Client, specURL, token string, retries int) ([]byte, error) {
	var lastErr error
	backoff := time.Second
	for attempt := 0; attempt <= retries; attempt++ {
		req, err := http.NewRequest(http.MethodGet, specURL, nil)
		if err != nil {
			return nil, err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			resp.Body.Close()
			return nil, fmt.Errorf("FETCH_AUTH_FAILED: %s: 401 unauthorized (check token)", specURL)
		case resp.StatusCode >= 500:
			resp.Body.Close()
			lastErr = fmt.Errorf("FETCH_NETWORK_ERROR: %s: status %d", specURL, resp.StatusCode)
			time.Sleep(backoff)
			backoff *= 2
			continue
		case resp.StatusCode >= 400:
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("fetch: %s: status %d: %s", specURL, resp.StatusCode, truncate(string(b), 256))
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		return body, nil
	}
	return nil, fmt.Errorf("FETCH_NETWORK_ERROR: %s: %w", specURL, lastErr)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
