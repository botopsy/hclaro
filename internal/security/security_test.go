package security

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTokensAndRequestBounds(t *testing.T) {
	one, err := Token()
	if err != nil {
		t.Fatal(err)
	}
	two, err := Token()
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 64 || one == two {
		t.Fatal("weak or reused token")
	}
	handler := Guard("127.0.0.1:9273", one, two, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		name, host, origin, site, auth, body, path string
		code                                       int
	}{
		{name: "same origin", host: "127.0.0.1:9273", auth: "Bearer " + one, path: "/api/v1/health", code: 204},
		{name: "suffix attack", host: "127.0.0.1.evil:9273", auth: "Bearer " + one, path: "/api/v1/health", code: 403},
		{name: "other local host", host: "localhost:9273", auth: "Bearer " + one, path: "/api/v1/health", code: 403},
		{name: "cross-site metadata", host: "127.0.0.1:9273", site: "cross-site", auth: "Bearer " + one, path: "/api/v1/health", code: 403},
		{name: "scheme required", host: "127.0.0.1:9273", auth: one, path: "/api/v1/health", code: 401},
		{name: "GET body", host: "127.0.0.1:9273", auth: "Bearer " + one, body: "unexpected", path: "/api/v1/health", code: 400},
		{name: "bounded query", host: "127.0.0.1:9273", auth: "Bearer " + one, path: "/api/v1/resources?query=" + strings.Repeat("a", 4096), code: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://127.0.0.1:9273"+tc.path, strings.NewReader(tc.body))
			r.Host = tc.host
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			r.Header.Set("Authorization", tc.auth)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("status %d", w.Code)
			}
		})
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:9273/api/v1/health", nil)
	r.ContentLength = -1
	r.TransferEncoding = []string{"chunked"}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("chunked body accepted")
	}
}

func TestMutatingRequests(t *testing.T) {
	token, csrf := strings.Repeat("a", 64), strings.Repeat("b", 64)
	handler := Guard("127.0.0.1:9273", token, csrf, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202) }))
	origin, op := "http://127.0.0.1:9273", "/api/v1/operations/"+strings.Repeat("0", 32)+"/cancel"
	for _, tc := range []struct {
		name, method, path, origin, site, csrf, contentType, body, auth string
		code                                                            int
	}{
		{"start plan", "POST", "/api/v1/plans", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"cancel", "POST", op, origin, "", csrf, "application/json; charset=utf-8", "{}", token, 202},
		{"origin required", "POST", "/api/v1/plans", "", "same-origin", csrf, "application/json", "{}", token, 403},
		{"foreign origin", "POST", "/api/v1/plans", "http://evil.example", "same-origin", csrf, "application/json", "{}", token, 403},
		{"localhost origin", "POST", "/api/v1/plans", "http://localhost:9273", "", csrf, "application/json", "{}", token, 403},
		{"same-site is not same-origin", "POST", "/api/v1/plans", origin, "same-site", csrf, "application/json", "{}", token, 403},
		{"csrf token required", "POST", "/api/v1/plans", origin, "same-origin", "", "application/json", "{}", token, 403},
		{"csrf token must match", "POST", "/api/v1/plans", origin, "same-origin", token, "application/json", "{}", token, 403},
		{"form posts rejected", "POST", "/api/v1/plans", origin, "same-origin", csrf, "application/x-www-form-urlencoded", "a=1", token, 415},
		{"text posts rejected", "POST", "/api/v1/plans", origin, "same-origin", csrf, "text/plain", "{}", token, 415},
		{"empty body", "POST", "/api/v1/plans", origin, "same-origin", csrf, "application/json", "", token, 413},
		{"large body", "POST", "/api/v1/plans", origin, "same-origin", csrf, "application/json", strings.Repeat(" ", MaxBody+1), token, 413},
		{"session required", "POST", "/api/v1/plans", origin, "same-origin", csrf, "application/json", "{}", "", 401},
		{"apply route", "POST", "/api/v1/plans/" + strings.Repeat("0", 32) + "/apply", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"approval route", "POST", "/api/v1/plans/" + strings.Repeat("0", 32) + "/approval", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"preflight route", "POST", "/api/v1/plans/" + strings.Repeat("0", 32) + "/preflight", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"preflight needs csrf", "POST", "/api/v1/plans/" + strings.Repeat("0", 32) + "/preflight", origin, "same-origin", "", "application/json", "{}", token, 403},
		{"preflight cross-site", "POST", "/api/v1/plans/" + strings.Repeat("0", 32) + "/preflight", origin, "cross-site", csrf, "application/json", "{}", token, 403},
		{"apply needs csrf", "POST", "/api/v1/plans/" + strings.Repeat("0", 32) + "/apply", origin, "same-origin", "", "application/json", "{}", token, 403},
		{"apply needs origin", "POST", "/api/v1/plans/" + strings.Repeat("0", 32) + "/apply", "", "", csrf, "application/json", "{}", token, 403},
		{"apply cross-site", "POST", "/api/v1/plans/" + strings.Repeat("0", 32) + "/apply", origin, "cross-site", csrf, "application/json", "{}", token, 403},
		{"bad plan id apply", "POST", "/api/v1/plans/../apply", origin, "same-origin", csrf, "application/json", "{}", token, 405},
		{"no other plan post", "POST", "/api/v1/plans/" + strings.Repeat("0", 32) + "/destroy", origin, "same-origin", csrf, "application/json", "{}", token, 405},
		{"import preview", "POST", "/api/v1/imports/preview", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"start import", "POST", "/api/v1/imports", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"import needs csrf", "POST", "/api/v1/imports", origin, "same-origin", "", "application/json", "{}", token, 403},
		{"import needs origin", "POST", "/api/v1/imports", "", "", csrf, "application/json", "{}", token, 403},
		{"import preview needs csrf", "POST", "/api/v1/imports/preview", origin, "same-origin", "", "application/json", "{}", token, 403},
		{"no other import post", "POST", "/api/v1/imports/write", origin, "same-origin", csrf, "application/json", "{}", token, 405},
		{"no arbitrary post", "POST", "/api/v1/workspace", origin, "same-origin", csrf, "application/json", "{}", token, 405},
		{"bad operation id", "POST", "/api/v1/operations/../../cancel", origin, "same-origin", csrf, "application/json", "{}", token, 405},
		{"git status", "POST", "/api/v1/git/status", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"git diff", "POST", "/api/v1/git/diff", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"editor open", "POST", "/api/v1/editor/open", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"hub git status", "POST", "/api/v1/projects/" + strings.Repeat("0", 32) + "/git/status", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"hub editor open", "POST", "/api/v1/projects/" + strings.Repeat("0", 32) + "/editor/open", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"git needs csrf", "POST", "/api/v1/git/diff", origin, "same-origin", "", "application/json", "{}", token, 403},
		{"editor needs origin", "POST", "/api/v1/editor/open", "", "", csrf, "application/json", "{}", token, 403},
		{"editor cross-site", "POST", "/api/v1/editor/open", origin, "cross-site", csrf, "application/json", "{}", token, 403},
		{"no git write route", "POST", "/api/v1/git/checkout", origin, "same-origin", csrf, "application/json", "{}", token, 405},
		{"no git commit route", "POST", "/api/v1/projects/" + strings.Repeat("0", 32) + "/git/commit", origin, "same-origin", csrf, "application/json", "{}", token, 405},
		{"no generic run route", "POST", "/api/v1/editor/run", origin, "same-origin", csrf, "application/json", "{}", token, 405},
		// Scoped Hub plans (M24.05): only starting a plan and cancelling an operation of one
		// context; no scoped apply, approval, preflight or generic route yet.
		{"scoped plan", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"scoped cancel", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/operations/" + strings.Repeat("1", 32) + "/cancel", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"scoped plan needs csrf", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans", origin, "same-origin", "", "application/json", "{}", token, 403},
		{"scoped plan needs origin", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans", "", "", csrf, "application/json", "{}", token, 403},
		{"scoped plan cross-site", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans", origin, "cross-site", csrf, "application/json", "{}", token, 403},
		{"scoped plan needs session", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans", origin, "same-origin", csrf, "application/json", "{}", "", 401},
		{"scoped preflight", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/preflight", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"scoped preflight needs csrf", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/preflight", origin, "same-origin", "", "application/json", "{}", token, 403},
		{"scoped preflight cross-site", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/preflight", origin, "cross-site", csrf, "application/json", "{}", token, 403},
		{"scoped preflight needs session", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/preflight", origin, "same-origin", csrf, "application/json", "{}", "", 401},
		{"scoped approval", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/approval", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"scoped approval needs csrf", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/approval", origin, "same-origin", "", "application/json", "{}", token, 403},
		{"scoped approval cross-site", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/approval", origin, "cross-site", csrf, "application/json", "{}", token, 403},
		{"scoped approval needs session", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/approval", origin, "same-origin", csrf, "application/json", "{}", "", 401},
		{"scoped apply", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/apply", origin, "same-origin", csrf, "application/json", "{}", token, 202},
		{"scoped apply needs csrf", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/apply", origin, "same-origin", "", "application/json", "{}", token, 403},
		{"scoped apply cross-site", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/apply", origin, "cross-site", csrf, "application/json", "{}", token, 403},
		{"scoped apply needs session", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/apply", origin, "same-origin", csrf, "application/json", "{}", "", 401},
		{"scoped apply without origin", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/apply", "", "", csrf, "application/json", "{}", token, 403},
		{"scoped apply as form", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/apply", origin, "same-origin", csrf, "text/plain", "{}", token, 415},
		{"scoped destroy is not a route", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/destroy", origin, "same-origin", csrf, "application/json", "{}", token, 405},
		{"bad scoped plan id", "POST", "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/../apply", origin, "same-origin", csrf, "application/json", "{}", token, 405},
		{"bad scoped context id", "POST", "/api/v1/contexts/../plans", origin, "same-origin", csrf, "application/json", "{}", token, 405},
		{"long scoped context id", "POST", "/api/v1/contexts/" + strings.Repeat("0", 64) + "/plans", origin, "same-origin", csrf, "application/json", "{}", token, 405},
		{"put rejected", "PUT", "/api/v1/plans", origin, "same-origin", csrf, "application/json", "{}", token, 405},
		{"delete rejected", "DELETE", op, origin, "same-origin", csrf, "application/json", "{}", token, 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://127.0.0.1:9273"+tc.path, strings.NewReader(tc.body))
			for k, v := range map[string]string{"Origin": tc.origin, "Sec-Fetch-Site": tc.site, "X-Hclaro-CSRF": tc.csrf, "Content-Type": tc.contentType} {
				if v != "" {
					r.Header.Set(k, v)
				}
			}
			if tc.auth != "" {
				r.Header.Set("Authorization", "Bearer "+tc.auth)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("status %d, want %d", w.Code, tc.code)
			}
		})
	}
}
