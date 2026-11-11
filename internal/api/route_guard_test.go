package api

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// security.Guard treats every POST, PATCH and DELETE under /api/ as mutating and leaves the
// choice of routes to the router. These tests drive the real handler with full credentials
// to prove the router is the second allowlist: read-only routes refuse mutating methods,
// intents that were never routes stay unrouted, and a malformed ID never reaches a lookup.

var mutatingRoute = regexp.MustCompile(`"((?:POST|PATCH|DELETE) /api/[^"]*)"`)

// mutatingPatterns reads the registered mutating patterns from the source, so a new route is
// covered without editing this test. The routes registered with a built prefix or in a
// loop are added by hand.
func mutatingPatterns(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range mutatingRoute.FindAllStringSubmatch(string(src), -1) {
			if !strings.HasSuffix(m[1], "/") {
				seen[m[1]] = true
			}
		}
	}
	for _, p := range []string{"/api/v1", "/api/v1/projects/{id}"} {
		for _, r := range []string{"/git/status", "/git/diff", "/editor/open"} {
			seen["POST "+p+r] = true
		}
	}
	for _, tool := range []string{"validate", "fmt-check", "providers", "outputs-metadata"} {
		seen["POST /api/v1/projects/{id}/operations/"+tool] = true
	}
	var out []string
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	if len(out) < 50 {
		t.Fatalf("only %d mutating routes found; the source scan is broken", len(out))
	}
	return out
}

func TestReadOnlyRoutesAreNotMutable(t *testing.T) {
	s := newHubServer(t)
	for _, path := range []string{"/api/v1/health", "/api/v1/workspace", "/api/v1/resources", "/api/v1/resources/x"} {
		for _, method := range []string{"POST", "PATCH", "DELETE"} {
			if w := s.do(method, path, "{}"); w.Code != 404 {
				t.Errorf("%s %s: %d, want 404", method, path, w.Code)
			}
		}
	}
}

// Intents that were never routes. Each answers a 4xx without running anything.
func TestUnroutedMutationsAreRefused(t *testing.T) {
	s := newHubServer(t)
	id := strings.Repeat("0", 32)
	for _, path := range []string{
		"/api/v1/plans/" + id + "/destroy",
		"/api/v1/git/checkout",
		"/api/v1/editor/run",
		"/api/v1/imports/write",
		"/api/v1/projects/" + id + "/git/commit",
		"/api/v1/contexts/" + id + "/plans/" + id + "/destroy",
		"/api/v1/contexts/" + id + "/discovery/run",
		"/api/v1/contexts/" + id + "/imports/run",
		"/api/v1/contexts/" + strings.Repeat("0", 64) + "/plans",
		"/api/v1/contexts/" + id + "/plans/not-an-id/apply",
	} {
		for _, method := range []string{"POST", "PATCH", "DELETE"} {
			if w := s.do(method, path, "{}"); w.Code < 400 || w.Code >= 500 {
				t.Errorf("%s %s: %d %s", method, path, w.Code, w.Body)
			}
		}
	}
}

// Go unescapes path wildcards, so an encoded slash or dot segment arrives in PathValue. No
// mutating route may accept one, and nothing may be removed by one.
func TestMalformedIDsNeverSucceed(t *testing.T) {
	s := newHubServer(t)
	project := s.add("network")
	bad := []string{"%2e%2e", "%2F", "..%2Fx", strings.Repeat("A", 32), strings.Repeat("0", 31), strings.Repeat("0", 64), "%00", "x%20y", "%E2%82%AC"}
	for _, pattern := range mutatingPatterns(t) {
		method, path, _ := strings.Cut(pattern, " ")
		if !strings.Contains(path, "{") {
			continue // no ID to malform, and browse would list a real directory
		}
		for _, b := range bad {
			p := regexp.MustCompile(`\{[a-z]+\}`).ReplaceAllString(path, b)
			if u, err := url.ParseRequestURI(p); err != nil || u.Path == "" {
				continue
			}
			if w := s.do(method, p, "{}"); w.Code < 300 && w.Code >= 200 || w.Code >= 500 {
				t.Errorf("%s %s: %d %s", method, p, w.Code, w.Body)
			}
		}
	}
	if _, err := s.hub.Registry.Get(project.ID); err != nil {
		t.Fatalf("a malformed ID removed or changed the registered project: %v", err)
	}
}
