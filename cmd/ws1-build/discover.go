package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/net/html"
)

// discover hits a tenant's /api/help, parses the HTML index, and emits a
// JSON listing of every available API section. See docs/spec-acquisition.md
// section "Discovery procedure" for the rules.
//
// The index page typically does not require auth; if the tenant is
// configured otherwise we surface a clear error.
func newDiscoverCmd() *cobra.Command {
	var tenant, outPath string
	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Discover API sections by parsing tenant's /api/help",
		RunE: func(cmd *cobra.Command, args []string) error {
			if tenant == "" {
				return fmt.Errorf("--tenant is required (e.g. as1831.awmdm.com)")
			}
			result, err := discoverSections(tenant)
			if err != nil {
				return err
			}
			b, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return err
			}
			b = append(b, '\n')
			if outPath == "-" || outPath == "" {
				_, err = os.Stdout.Write(b)
				return err
			}
			return os.WriteFile(outPath, b, 0o644)
		},
	}
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant hostname (e.g. as1831.awmdm.com)")
	cmd.Flags().StringVar(&outPath, "out", "-", "output path; '-' for stdout")
	return cmd
}

// DiscoveryResult is the payload written to .build/sections.json.
type DiscoveryResult struct {
	Tenant             string             `json:"tenant"`
	APIExplorerVersion string             `json:"api_explorer_version"`
	DiscoveredAt       string             `json:"discovered_at"`
	Sections           []DiscoveredSection `json:"sections"`
}

type DiscoveredSection struct {
	DisplayName string `json:"display_name"`
	Slug        string `json:"slug"`
	SpecURL     string `json:"spec_url"`
}

func discoverSections(tenant string) (*DiscoveryResult, error) {
	indexURL := "https://" + tenant + "/api/help"
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(indexURL)
	if err != nil {
		return nil, fmt.Errorf("discover: GET %s: %w", indexURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("DISCOVERY_AUTH_REQUIRED: tenant /api/help requires auth (status %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("discover: GET %s: status %d", indexURL, resp.StatusCode)
	}
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("DISCOVERY_STRUCTURE_UNRECOGNIZED: parse HTML: %w", err)
	}
	sections, footerText := walkIndexHTML(doc, tenant)
	if len(sections) == 0 {
		return nil, fmt.Errorf("DISCOVERY_STRUCTURE_UNRECOGNIZED: no api sections found at %s", indexURL)
	}
	version := extractAPIExplorerVersion(footerText)
	return &DiscoveryResult{
		Tenant:             tenant,
		APIExplorerVersion: version,
		DiscoveredAt:       time.Now().UTC().Format(time.RFC3339),
		Sections:           sections,
	}, nil
}

// walkIndexHTML extracts every <a href="/api/help/Docs/Explore?urls.primaryName=NAME">
// link and the footer text. Returns sections in the order they appear in the
// document so output is stable.
func walkIndexHTML(n *html.Node, tenant string) ([]DiscoveredSection, string) {
	var sections []DiscoveredSection
	var footerBuf strings.Builder
	seen := map[string]bool{}
	var walk func(*html.Node, bool)
	walk = func(node *html.Node, inFooter bool) {
		if node == nil {
			return
		}
		if node.Type == html.ElementNode {
			if strings.EqualFold(node.Data, "footer") {
				inFooter = true
			}
			if strings.EqualFold(node.Data, "a") {
				href := attrValue(node, "href")
				if name, ok := parsePrimaryName(href); ok {
					slug := slugify(name)
					if !seen[slug] {
						seen[slug] = true
						sections = append(sections, DiscoveredSection{
							DisplayName: name,
							Slug:        slug,
							SpecURL:     "https://" + tenant + "/api/help/Docs/" + slug,
						})
					}
				}
			}
		}
		if inFooter && node.Type == html.TextNode {
			footerBuf.WriteString(node.Data)
			footerBuf.WriteString(" ")
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inFooter)
		}
	}
	walk(n, false)
	return sections, footerBuf.String()
}

func attrValue(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// parsePrimaryName extracts the URL-encoded display name from an <a href>
// pointing at the Swagger UI explorer.
func parsePrimaryName(href string) (string, bool) {
	if !strings.Contains(href, "/api/help/Docs/Explore") {
		return "", false
	}
	q := href
	if i := strings.Index(href, "?"); i >= 0 {
		q = href[i+1:]
	}
	values, err := url.ParseQuery(q)
	if err != nil {
		return "", false
	}
	name := values.Get("urls.primaryName")
	if name == "" {
		return "", false
	}
	return name, true
}

// slugify implements the rule from docs/spec-acquisition.md:
//  1. Lowercase
//  2. Strip the literal substring "api"
//  3. Strip whitespace
//  4. Append "v1" if the result doesn't already end in v<digits>.
func slugify(displayName string) string {
	s := strings.ToLower(displayName)
	s = strings.ReplaceAll(s, "api", "")
	s = strings.Join(strings.Fields(s), "")
	if !versionTailRE.MatchString(s) {
		s = s + "v1"
	}
	return s
}

var versionTailRE = regexp.MustCompile(`v\d+$`)

var apiExplorerVersionRE = regexp.MustCompile(`API Explorer\s+([0-9]+(?:\.[0-9]+)+)`)

func extractAPIExplorerVersion(footerText string) string {
	m := apiExplorerVersionRE.FindStringSubmatch(footerText)
	if len(m) < 2 {
		return "unknown"
	}
	return m[1]
}
