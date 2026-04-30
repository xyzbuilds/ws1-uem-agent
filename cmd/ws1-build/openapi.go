package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// openAPIDoc is the subset of OpenAPI 3.0.1 that ws1-build cares about.
// We deliberately don't pull in a full OpenAPI library: we only need
// servers/paths/operations/parameters, and the spec format is stable.
type openAPIDoc struct {
	OpenAPI string                     `json:"openapi"`
	Info    openAPIInfo                `json:"info"`
	Servers []openAPIServer            `json:"servers"`
	Paths   map[string]openAPIPathItem `json:"paths"`
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
	Tags        []string                       `json:"tags"`
	Summary     string                         `json:"summary"`
	Description string                         `json:"description"`
	OperationID string                         `json:"operationId"`
	Parameters  []openAPIParameter             `json:"parameters"`
	RequestBody *openAPIRequest                `json:"requestBody"`
	Responses   map[string]openAPIResponseItem `json:"responses"`
}

// openAPIResponseItem captures the parts of a response object we need
// to derive the per-op Accept version. We don't unmarshal into a typed
// schema because content-type keys are dynamic (e.g.
// "application/json;version=2").
type openAPIResponseItem struct {
	Description string                     `json:"description"`
	Content     map[string]json.RawMessage `json:"content"`
}

type openAPIParameter struct {
	Name        string        `json:"name"`
	In          string        `json:"in"` // path | query | header | cookie
	Required    bool          `json:"required"`
	Schema      openAPISchema `json:"schema"`
	Description string        `json:"description"`
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

// acceptVersionRE matches the version parameter inside an OpenAPI
// response content-type key like "application/json;version=2".
var acceptVersionRE = regexp.MustCompile(`application/json\s*;\s*version=(\d+)`)

// slugVersionRE matches the trailing version digits on a section slug
// (e.g. "mdmv2" -> "2", "systemv1" -> "1"). The section slug IS the
// API version per docs.spec-acquisition.md slugification: every op in
// `mdmv2` runs at version=2, every op in `systemv1` at version=1, etc.
// This is the reliable fallback when an op's response content-types
// don't explicitly declare a version.
var slugVersionRE = regexp.MustCompile(`v(\d+)$`)

// versionFromSlug extracts the version digit from a section slug.
// mdmv4 -> "4", mamv2 -> "2", systemv1 -> "1". Returns "" if the slug
// doesn't end in v<digits> (shouldn't happen with conformant slugs).
func versionFromSlug(slug string) string {
	m := slugVersionRE.FindStringSubmatch(slug)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// extractAcceptVersion walks an op's response content-type keys and
// returns the highest application/json;version=N value, or "" if none
// is declared. Most WS1 ops declare exactly one version that matches
// their section slug; a handful are silent.
func extractAcceptVersion(op openAPIOperation) string {
	highest := 0
	for _, body := range op.Responses {
		for ct := range body.Content {
			m := acceptVersionRE.FindStringSubmatch(ct)
			if len(m) >= 2 {
				n, err := strconvAtoiSafe(m[1])
				if err == nil && n > highest {
					highest = n
				}
			}
		}
	}
	if highest == 0 {
		return ""
	}
	return intToString(highest)
}

// strconvAtoiSafe is a tiny wrapper so we don't pull strconv into the
// import block for one call. Returns (0, err) on parse failure.
func strconvAtoiSafe(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit: %c", c)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

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
//	<section-slug>.<tag-lowercase>.<verb-lowercase>
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

	// Section-level fallback: every op in `mdmv2` runs at version=2, etc.
	// We use slug-derived version because it's how operations are
	// grouped in the first place — guaranteed correct even if a spec
	// file's info.version drifts.
	sectionVersion := versionFromSlug(s.Slug)

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
			opVer := extractAcceptVersion(op)
			if opVer == "" {
				opVer = sectionVersion
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
				AcceptVersion:  opVer,
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
	// AcceptVersion is the value to send in the Accept content-type
	// parameter (`application/json;version=<N>`). Sourced from the
	// op's response content keys; falls back to the section's
	// info.version when the op is silent. WS1's edge gateway 503s
	// on calls without the right version, so this is per-op-mandatory.
	AcceptVersion string
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
	// Pass 1: canonicalize by appending HTTP method when (op, method)
	// pair is unique among rows sharing the op id. We track which
	// (op, method) pairs collide WITH a different method so we know to
	// disambiguate.
	opMethods := map[string]map[string]int{} // op -> method -> count
	for _, r := range rows {
		if opMethods[r.Op] == nil {
			opMethods[r.Op] = map[string]int{}
		}
		opMethods[r.Op][r.HTTPMethod]++
	}
	for i, r := range rows {
		methods := opMethods[r.Op]
		if len(methods) > 1 {
			newOp := r.Op + "." + strings.ToLower(r.HTTPMethod)
			rows[i].Op = newOp
			*warnings = append(*warnings, fmt.Sprintf("collision: %s -> %s (disambiguated by HTTP method)", r.Op, newOp))
		}
	}

	// Pass 2: dedupe true duplicates. If two rows now have the same
	// (op, method, path), they're the same operation appearing twice
	// in the spec (some sections genuinely ship duplicate entries).
	// Keep the first; drop the rest.
	seen := map[string]struct{}{}
	out := rows[:0]
	dedupedOps := map[string]int{}
	for _, r := range rows {
		key := r.Op + "|" + r.HTTPMethod + "|" + r.PathTemplate
		if _, ok := seen[key]; ok {
			dedupedOps[r.Op]++
			continue
		}
		seen[key] = struct{}{}
		out = append(out, r)
	}
	for op, count := range dedupedOps {
		*warnings = append(*warnings, fmt.Sprintf("duplicate-in-spec: %s appeared %d extra time(s); kept first", op, count+1))
	}

	// Pass 3: same op id but different paths (WS1 sometimes ships two
	// distinct endpoints with the same operationId). Append a suffix
	// derived from the path: "list" for collection paths, "by<paramname>"
	// for parameterized ones.
	rows = out
	byOp := map[string][]int{}
	for i, r := range rows {
		byOp[r.Op] = append(byOp[r.Op], i)
	}
	for op, idxs := range byOp {
		if len(idxs) < 2 {
			continue
		}
		for _, i := range idxs {
			r := rows[i]
			suffix := pathSuffixFor(r.PathTemplate)
			if suffix == "" {
				continue
			}
			rows[i].Op = r.Op + "." + suffix
			*warnings = append(*warnings, fmt.Sprintf("path-collision: %s on %s -> %s", op, r.PathTemplate, rows[i].Op))
		}
	}

	// Pass 4: numeric tiebreaker for ops whose suffix-disambiguation still
	// collides (multiple paths producing the same suffix). Append `.altN`
	// in declaration order.
	finalSeen := map[string]int{}
	for i := range rows {
		k := rows[i].Op
		finalSeen[k]++
		if finalSeen[k] > 1 {
			alt := fmt.Sprintf("%s.alt%d", k, finalSeen[k]-1)
			*warnings = append(*warnings, fmt.Sprintf("alt-suffix: %s -> %s (path %s)", k, alt, rows[i].PathTemplate))
			rows[i].Op = alt
		}
	}
	return rows
}

// pathSuffixFor turns a path template into a stable disambiguator. Picks
// "list" for collection paths (no params) and the lowercased name of the
// last path param otherwise.
func pathSuffixFor(p string) string {
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	last := ""
	hasParam := false
	for _, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			hasParam = true
			last = strings.ToLower(s[1 : len(s)-1])
		}
	}
	if !hasParam {
		return "list"
	}
	if last == "" {
		return ""
	}
	// Trim "id"/"uuid" suffixes for cleaner names.
	return "by" + last
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
