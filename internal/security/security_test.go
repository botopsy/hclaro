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
	origin, id := "http://127.0.0.1:9273", strings.Repeat("0", 32)
	// Every shape the router may serve is guarded the same way, so the checks are exercised on
	// one path per method and the path list only proves the guard does not depend on it.
	paths := []string{"/api/v1/plans", "/api/v1/plans/" + id + "/apply", "/api/v1/contexts/" + id + "/plans/" + id + "/apply", "/api/v1/not-a-route"}
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		for _, path := range paths {
			for _, tc := range []struct {
				name, origin, site, csrf, contentType, body, auth string
				code                                              int
			}{
				{"accepted", origin, "same-origin", csrf, "application/json", "{}", token, 202},
				{"json charset", origin, "", csrf, "application/json; charset=utf-8", "{}", token, 202},
				{"origin required", "", "same-origin", csrf, "application/json", "{}", token, 403},
				{"foreign origin", "http://evil.example", "same-origin", csrf, "application/json", "{}", token, 403},
				{"localhost origin", "http://localhost:9273", "", csrf, "application/json", "{}", token, 403},
				{"same-site is not same-origin", origin, "same-site", csrf, "application/json", "{}", token, 403},
				{"cross-site", origin, "cross-site", csrf, "application/json", "{}", token, 403},
				{"csrf required", origin, "same-origin", "", "application/json", "{}", token, 403},
				{"csrf must match", origin, "same-origin", token, "application/json", "{}", token, 403},
				{"form rejected", origin, "same-origin", csrf, "application/x-www-form-urlencoded", "a=1", token, 415},
				{"text rejected", origin, "same-origin", csrf, "text/plain", "{}", token, 415},
				{"empty body", origin, "same-origin", csrf, "application/json", "", token, 413},
				{"large body", origin, "same-origin", csrf, "application/json", strings.Repeat(" ", MaxBody+1), token, 413},
				{"session required", origin, "same-origin", csrf, "application/json", "{}", "", 401},
			} {
				t.Run(method+" "+tc.name+" "+path, func(t *testing.T) {
					r := httptest.NewRequest(method, "http://127.0.0.1:9273"+path, strings.NewReader(tc.body))
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
	}
	// Other methods, and mutating methods outside /api/, never reach the handler.
	for _, tc := range []struct{ method, path string }{{"PUT", "/api/v1/plans"}, {"OPTIONS", "/api/v1/plans"}, {"POST", "/"}, {"DELETE", "/index.html"}} {
		r := httptest.NewRequest(tc.method, "http://127.0.0.1:9273"+tc.path, strings.NewReader("{}"))
		r.Header.Set("Origin", origin)
		r.Header.Set("X-Hclaro-CSRF", csrf)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 405 {
			t.Fatalf("%s %s: status %d, want 405", tc.method, tc.path, w.Code)
		}
	}
}

// A page served from another localhost port (same site, different origin) or a rebound DNS
// name tries to read the session's CSRF token, read the snapshot and start an apply (M24.12).
// It never has the bearer token, which lives only in the private URL's fragment and the
// tab's sessionStorage; even a guessed token is refused because its Origin or Host differs.
// No response grants CORS, so a browser would not let it read anything either.
func TestMaliciousLocalPage(t *testing.T) {
	token, csrf := strings.Repeat("a", 64), strings.Repeat("b", 64)
	reached := false
	handler := Guard("127.0.0.1:9273", token, csrf, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	apply := "/api/v1/contexts/" + strings.Repeat("0", 32) + "/plans/" + strings.Repeat("1", 32) + "/apply"
	for _, tc := range []struct {
		name, method, host, path, origin, site, auth string
		code                                         int
	}{
		{"read csrf token without session", "GET", "127.0.0.1:9273", "/api/v1/session", "http://127.0.0.1:8000", "same-site", "", 403},
		{"read csrf token no-cors", "GET", "127.0.0.1:9273", "/api/v1/session", "", "same-site", "", 401},
		{"read snapshot with guessed token", "GET", "127.0.0.1:9273", "/api/v1/workspace", "http://127.0.0.1:8000", "same-site", token, 403},
		{"localhost alias", "GET", "127.0.0.1:9273", "/api/v1/session", "http://localhost:9273", "same-site", token, 403},
		{"dns rebinding", "GET", "evil.example:9273", "/api/v1/session", "", "", token, 403},
		{"rebinding to localhost name", "GET", "localhost:9273", "/api/v1/session", "", "", token, 403},
		{"apply from other port", "POST", "127.0.0.1:9273", apply, "http://127.0.0.1:8000", "same-site", token, 403},
		{"apply from null origin", "POST", "127.0.0.1:9273", apply, "null", "cross-site", token, 403},
		{"cors preflight", "OPTIONS", "127.0.0.1:9273", apply, "http://127.0.0.1:8000", "same-site", "", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			r := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, strings.NewReader(map[bool]string{true: "{}"}[tc.method == "POST"]))
			r.Host = tc.host
			for k, v := range map[string]string{"Origin": tc.origin, "Sec-Fetch-Site": tc.site, "X-Hclaro-CSRF": csrf, "Content-Type": "application/json", "Access-Control-Request-Method": "POST"} {
				if v != "" && (tc.method != "GET" || k == "Origin" || k == "Sec-Fetch-Site") {
					r.Header.Set(k, v)
				}
			}
			if tc.auth != "" {
				r.Header.Set("Authorization", "Bearer "+tc.auth)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.code || reached {
				t.Fatalf("status %d (reached %v), want %d", w.Code, reached, tc.code)
			}
			for k := range w.Header() {
				if strings.HasPrefix(k, "Access-Control-") {
					t.Fatalf("CORS header %s", k)
				}
			}
			if strings.Contains(w.Body.String(), csrf) || strings.Contains(w.Body.String(), token) {
				t.Fatal("token in refusal")
			}
		})
	}
}
