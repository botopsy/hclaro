package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hclaro/internal/engine"
	"hclaro/internal/projects"
	"hclaro/internal/state"
)

// Tests for M30 (reveal one redacted value). Synthetic CLI and state only.

// redactedLeaf finds the first resource attribute the server redacted, as a reveal path.
func redactedLeaf(t *testing.T, s *scopedServer, cid string) (rid string, path []any) {
	t.Helper()
	var walk func(v any, at []any) []any
	walk = func(v any, at []any) []any {
		switch x := v.(type) {
		case string:
			if x == state.Redacted {
				return at
			}
		case map[string]any:
			for k, item := range x {
				if p := walk(item, append(append([]any{}, at...), k)); p != nil {
					return p
				}
			}
		case []any:
			for i, item := range x {
				if p := walk(item, append(append([]any{}, at...), float64(i))); p != nil {
					return p
				}
			}
		}
		return nil
	}
	page := decode[struct {
		Resources []state.Resource `json:"resources"`
	}](t, s.do("GET", "/api/v1/contexts/"+cid+"/resources?limit=200", ""))
	for _, r := range page.Resources {
		detail := decode[state.Resource](t, s.do("GET", "/api/v1/contexts/"+cid+"/resources/"+r.ID, ""))
		if p := walk(detail.Attributes, nil); p != nil {
			return r.ID, p
		}
	}
	t.Fatal("no redacted attribute in the synthetic state")
	return "", nil
}

func reveal(s *scopedServer, cid, rid string, path []any, opts ...opt) (int, map[string]any, string) {
	s.t.Helper()
	b, _ := json.Marshal(map[string]any{"path": path})
	w := s.do("POST", "/api/v1/contexts/"+cid+"/resources/"+rid+"/reveal", string(b), opts...)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != 429 && w.Header().Get("Cache-Control") != "no-store" {
		s.t.Fatalf("reveal response is cacheable: %q", w.Header().Get("Cache-Control"))
	}
	return w.Code, body, w.Body.String()
}

func TestRevealReturnsOneRedactedScalar(t *testing.T) {
	s := newScopedServer(t)
	cid := s.openLoaded("network", "dev", "")
	rid, path := redactedLeaf(t, s, cid)
	code, body, raw := reveal(s, cid, rid, path)
	if v, _ := body["value"].(string); code != 200 || v == "" || v == state.Redacted {
		t.Fatalf("reveal: %d %s", code, raw)
	}
	// The resource detail still shows nothing but the redacted marker.
	detail := s.do("GET", "/api/v1/contexts/"+cid+"/resources/"+rid, "").Body.String()
	if strings.Contains(detail, body["value"].(string)) {
		t.Fatal("a reveal changed what the resource detail shows")
	}
}

func TestRevealRefusals(t *testing.T) {
	s := newScopedServer(t)
	cid := s.openLoaded("network", "dev", "")
	rid, path := redactedLeaf(t, s, cid)
	other := s.openLoaded("network", "staging", "")
	for _, tc := range []struct {
		name string
		cid  string
		rid  string
		path []any
		want int
		code string
	}{
		{"unknown resource", cid, strings.Repeat("0", 32), path, 404, "resource_not_found"},
		{"resource of another context", other, rid, path, 404, "resource_not_found"},
		{"closed context", strings.Repeat("f", 64), rid, path, 404, "context_closed"},
		{"missing path", cid, rid, []any{"no_such_attribute"}, 404, "path_not_found"},
		{"empty path", cid, rid, []any{}, 404, "path_not_found"},
		{"too deep", cid, rid, make([]any, 65), 404, "path_not_found"},
	} {
		code, body, raw := reveal(s, tc.cid, tc.rid, tc.path)
		if code != tc.want || body["error"] != tc.code {
			t.Errorf("%s: %d %s", tc.name, code, raw)
		}
	}
	// A bad body is refused before any read.
	wantError(t, s.do("POST", "/api/v1/contexts/"+cid+"/resources/"+rid+"/reveal", `{"path":["a"],"extra":1}`), 400, "invalid_request")
}

func TestRevealNeedsSessionOriginAndCSRF(t *testing.T) {
	s := newScopedServer(t)
	cid := s.openLoaded("network", "dev", "")
	rid, path := redactedLeaf(t, s, cid)
	b, _ := json.Marshal(map[string]any{"path": path})
	url := "/api/v1/contexts/" + cid + "/resources/" + rid + "/reveal"
	for _, tc := range []struct {
		name string
		opts []opt
		code int
	}{
		{"no session", []opt{without("Authorization")}, 401},
		{"no csrf", []opt{without("X-Hclaro-CSRF")}, 403},
		{"wrong csrf", []opt{with("X-Hclaro-CSRF", strings.Repeat("c", 64))}, 403},
		{"cross-site origin", []opt{with("Origin", "https://evil.example")}, 403},
		{"wrong host", []opt{func(r *http.Request) { r.Host = "evil.example:9273" }}, 403},
	} {
		w := s.do("POST", url, string(b), tc.opts...)
		if w.Code != tc.code || strings.Contains(w.Body.String(), `"value"`) {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body)
		}
	}
	if w := s.do("GET", url, ""); w.Code == 200 {
		t.Fatalf("GET reveal answered 200: %s", w.Body)
	}
}

func TestRevealIsRateLimited(t *testing.T) {
	s := newScopedServer(t)
	cid := s.openLoaded("network", "dev", "")
	rid, path := redactedLeaf(t, s, cid)
	for i := 0; i < revealLimit; i++ {
		if code, _, raw := reveal(s, cid, rid, path); code != 200 {
			t.Fatalf("reveal %d: %d %s", i, code, raw)
		}
	}
	if code, body, raw := reveal(s, cid, rid, path); code != 429 || body["error"] != "reveal_rate_limited" || strings.Contains(raw, `"value"`) {
		t.Fatalf("over the limit: %d %s", code, raw)
	}
}

// deniedAfterOpen reads state once, then answers like a backend that refuses access.
type deniedAfterOpen struct {
	projects.Adapter
	reads int
}

func (d *deniedAfterOpen) State(ctx context.Context) ([]byte, error) {
	if d.reads++; d.reads > 1 {
		return nil, engine.ErrReadCredentials
	}
	return d.Adapter.State(ctx)
}

func TestRevealDeniedByBackendIsTyped(t *testing.T) {
	s := newScopedServer(t)
	open := s.hub.Engines
	s.hub.Engines = func(root, preference, selected string, environ []string) (string, string, projects.Adapter, error) {
		name, exe, a, err := open(root, preference, selected, environ)
		return name, exe, &deniedAfterOpen{Adapter: a}, err
	}
	cid := s.openLoaded("network", "dev", "")
	rid, path := redactedLeaf(t, s, cid)
	code, body, raw := reveal(s, cid, rid, path)
	if code != 403 || body["error"] != "reveal_denied" || strings.Contains(raw, `"value"`) {
		t.Fatalf("denied: %d %s", code, raw)
	}
}

func TestRevealLeavesNoTraceOnDisk(t *testing.T) {
	s := newScopedServer(t)
	cid := s.openLoaded("network", "dev", "")
	rid, path := redactedLeaf(t, s, cid)
	_, body, _ := reveal(s, cid, rid, path)
	secret, _ := body["value"].(string)
	if len(secret) < 4 {
		t.Skip("synthetic value too short to search for")
	}
	// Nothing Hclaro wrote (registry, history, logs, temp files) contains the value.
	for _, root := range []string{s.dir, s.hub.TempDir} {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.Contains(p, "/fixtures/") {
				return nil
			}
			if b, _ := os.ReadFile(p); strings.Contains(string(b), secret) && !strings.HasSuffix(p, ".tf") && !strings.HasSuffix(p, ".json") && !strings.HasSuffix(p, ".tfstate") {
				t.Errorf("revealed value found in %s", p)
			}
			return nil
		})
	}
}
