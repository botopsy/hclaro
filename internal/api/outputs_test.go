package api

import (
	"fmt"
	"strings"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/engine/enginetest"
)

func TestOutputValuesAndReveal(t *testing.T) {
	fake := &enginetest.Fake{VersionValue: "1.9.8", OutputsOut: engine.Capture{Stdout: fixtures.Outputs}}
	s, p := toolServer(t, fake)
	cid := s.open(p.ID, "dev")
	base := "/api/v1/contexts/" + cid + "/outputs"

	for _, tc := range []struct {
		name string
		opts []opt
		code int
	}{
		{"no session", []opt{without("Authorization")}, 401},
		{"no csrf", []opt{without("X-Hclaro-CSRF")}, 403},
		{"cross-site", []opt{with("Origin", "https://evil.example")}, 403},
	} {
		for _, path := range []string{"/values", "/db_password/reveal"} {
			if w := s.do("POST", base+path, `{}`, tc.opts...); w.Code != tc.code {
				t.Fatalf("%s %s: %d", tc.name, path, w.Code)
			}
		}
	}
	if len(fake.Calls()) != 0 {
		t.Fatalf("refused requests ran the engine: %v", fake.Calls())
	}

	w := s.do("POST", base+"/values", `{}`)
	body := w.Body.String()
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v %s", w.Code, w.Header(), body)
	}
	// Plain outputs show their values; sensitive and secret-looking ones do not.
	for _, want := range []string{`network-dev-logs`, `"name":"db_password","type":"string","sensitive":true,"hidden":true`, `"name":"connection_string","type":"string","sensitive":false,"hidden":true`, `"unlisted":1`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	for _, secret := range []string{fixtures.Canary, "plain-unflagged-secret-7f3a@"} {
		if strings.Contains(body, secret) {
			t.Errorf("%q shown without a reveal:\n%s", secret, body)
		}
	}

	// A run's operation record still holds no values.
	op := s.do("POST", "/api/v1/projects/"+p.ID+"/operations/outputs-metadata", fmt.Sprintf(`{"contextId":%q}`, cid))
	if op.Code != 202 {
		t.Fatalf("%d %s", op.Code, op.Body)
	}
	done := s.waitOp(decode[map[string]any](t, op)["id"].(string))
	if b := fmt.Sprint(done); strings.Contains(b, "network-dev-logs") || strings.Contains(b, fixtures.Canary) {
		t.Fatalf("operation holds a value: %s", b)
	}

	w = s.do("POST", base+"/db_password/reveal", `{}`)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), fixtures.Canary) {
		t.Fatalf("reveal: %d %s", w.Code, w.Body)
	}
	if w := s.do("POST", base+"/nope/reveal", `{}`); w.Code != 404 || !strings.Contains(w.Body.String(), "output_not_found") {
		t.Fatalf("unknown output: %d %s", w.Code, w.Body)
	}
	if w := s.do("POST", "/api/v1/contexts/"+strings.Repeat("0", 32)+"/outputs/values", `{}`); w.Code != 404 {
		t.Fatalf("closed context: %d", w.Code)
	}
}
