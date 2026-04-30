package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// openAPIDoc is the subset of OpenAPI 3.0.1 that ws1-build cares about.
// We deliberately don't pull in a full OpenAPI library: we only need
// servers/paths/operations/parameters, and the spec format is stable.
type openAPIDoc struct {
	OpenAPI string                       `json:"openapi"`
	Info    openAPIInfo                  `json:"info"`
	Servers []openAPIServer              `json:"servers"`
	Paths   map[string]openAPIPathItem   `json:"paths"`
}

type openAPIInfo struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

type openAPIServer struct {
	URL string `json:"url"`
}

type openAPIPathItem map[string]json.RawMessage

type openAPIOperation struct {
	Tags        []string           `json:"tags"`
	Summary     string             `json:"summary"`
	Description string             `json:"description"`
	OperationID string             `json:"operationId"`
	Parameters  []openAPIParameter `json:"parameters"`
	RequestBody *openAPIRequest    `json:"requestBody"`
}

type openAPIParameter struct {
	Name     string         `json:"name"`
	In       string         `json:"in"` // path | query | header | cookie
	Required bool           `json:"required"`
	Schema   openAPISchema  `json:"schema"`
	Description string      `json:"description"`
}

type openAPISchema struct {
	Type    string `json:"type"`
	Default any    `json:"default"`
}

type openAPIRequest struct {
	Required bool `json:"required"`
}

// httpVerbs in the order ws1-build emits ambiguity warnings.
var httpVerbs = []string{"get", "post", "put", "patch", "delete", "head", "options"}

// loadSpec reads a single OpenAPI 3.0.1 spec file off disk.
func loadSpec(path string) (*openAPIDoc, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc openAPIDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.0") {
		return nil, fmt.Errorf("%s: unsupported OpenAPI version %q (need 3.0.x)", path, doc.OpenAPI)
	}
	if len(doc.Paths) == 0 {
		return nil, fmt.Errorf("%s: empty paths", path)
	}
	return &doc, nil
}

// loadSpecs walks specDir for *.json files and parses each.
// Returns a stable-sorted slice keyed by section slug (filename without .json).
func loadSpecs(specDir string) ([]loadedSpec, error) {
	entries, err := os.ReadDir(specDir)
	if err != nil {
		return nil, fmt.Errorf("read spec dir %s: %w", specDir, err)
	}
	var out []loadedSpec
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".json")
		doc, err := loadSpec(filepath.Join(specDir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, loadedSpec{Slug: slug, Doc: doc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

type loadedSpec struct {
	Slug string
	Doc  *openAPIDoc
}

// extractOps walks the spec's paths and produces canonical operation rows.
// It implements the slugification + naming rules from docs/spec-acquisition.md
// section "Operation naming":
//
//   <section-slug>.<tag-lowercase>.<verb-lowercase>
//
// where <verb> is the operationId suffix after the underscore, with any
// trailing "Async" stripped.
func extractOps(s loadedSpec) ([]opRow, []string, error) {
	var rows []opRow
	var warnings []string

	// Sort path keys for deterministic codegen output.
	paths := make([]string, 0, len(s.Doc.Paths))
	for p := range s.Doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	basePath := ""
	if len(s.Doc.Servers) > 0 {
		// Strip the tenant hostname; keep only the path segment.
		// Spec example: "https://as1831.awmdm.com/api/mdm" -> "/api/mdm".
		basePath = stripHostFromServerURL(s.Doc.Servers[0].URL)
	}

	for _, path := range paths {
		item := s.Doc.Paths[path]
		for _, verb := range httpVerbs {
			raw, ok := item[verb]
			if !ok {
				continue
			}
			var op openAPIOperation
			if err := json.Unmarshal(raw, &op); err != nil {
				return nil, warnings, fmt.Errorf("%s %s: %w", verb, path, err)
			}
			tag, w := deriveTag(op, path)
			if w != "" {
				warnings = append(warnings, w)
			}
			vverb, w := deriveVerb(op, verb)
			if w != "" {
				warnings = append(warnings, w)
			}
			rows = append(rows, opRow{
				Op:             fmt.Sprintf("%s.%s.%s", s.Slug, tag, vverb),
				Section:        s.Slug,
				Tag:            tag,
				Verb:           vverb,
				HTTPMethod:     strings.ToUpper(verb),
				PathTemplate:   path,
				BasePath:       basePath,
				OperationID:    op.OperationID,
				Summary:        op.Summary,
				Description:    op.Description,
				Parameters:     op.Parameters,
				HasRequestBody: op.RequestBody != nil,
			})
		}
	}

	// Detect verb collisions within (section, tag, verb).
	rows = disambiguateCollisions(rows, &warnings)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Op < rows[j].Op })
	return rows, warnings, nil
}

type opRow struct {
	Op             string
	Section        string
	Tag            string
	Verb           string
	HTTPMethod     string
	PathTemplate   string
	BasePath       string
	OperationID    string
	Summary        string
	Description    string
	Parameters     []openAPIParameter
	HasRequestBody bool
}

func deriveTag(op openAPIOperation, path string) (tag string, warning string) {
	if len(op.Tags) > 0 {
		t := strings.ToLower(op.Tags[0])
		if len(op.Tags) > 1 {
			warning = fmt.Sprintf("operation %q has %d tags; using first (%q)", op.OperationID, len(op.Tags), op.Tags[0])
		}
		return t, warning
	}
	// Fallback: first non-empty path segment.
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, p := range parts {
		if p != "" && !strings.HasPrefix(p, "{") {
			return strings.ToLower(p), fmt.Sprintf("operation %q has no tags; derived %q from path", op.OperationID, p)
		}
	}
	return "untagged", fmt.Sprintf("operation %q has no tags and no usable path segment", op.OperationID)
}

func deriveVerb(op openAPIOperation, httpVerb string) (verb string, warning string) {
	if op.OperationID == "" {
		return strings.ToLower(httpVerb), fmt.Sprintf("path entry has no operationId; using HTTP verb %q", httpVerb)
	}
	v := op.OperationID
	if i := strings.Index(v, "_"); i >= 0 {
		v = v[i+1:]
	}
	v = strings.TrimSuffix(v, "Async")
	if v == "" {
		return strings.ToLower(httpVerb), ""
	}
	return strings.ToLower(v), ""
}

func disambiguateCollisions(rows []opRow, warnings *[]string) []opRow {
	seen := map[string]int{}
	for _, r := range rows {
		seen[r.Op]++
	}
	if len(seen) == len(rows) {
		return rows
	}
	for i, r := range rows {
		if seen[r.Op] > 1 {
			rows[i].Op = r.Op + "." + strings.ToLower(r.HTTPMethod)
			*warnings = append(*warnings, fmt.Sprintf("collision: %s -> %s (disambiguated by HTTP method)", r.Op, rows[i].Op))
		}
	}
	return rows
}

func stripHostFromServerURL(u string) string {
	if u == "" {
		return ""
	}
	// Conservative: strip "scheme://host" prefix; keep everything from the
	// first "/" after the host.
	if !strings.Contains(u, "://") {
		if strings.HasPrefix(u, "/") {
			return u
		}
		return "/" + u
	}
	// Find the first "/" after "://".
	rest := u[strings.Index(u, "://")+3:]
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return ""
	}
	return rest[slash:]
}
