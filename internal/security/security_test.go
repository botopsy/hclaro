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
	handler := Guard("127.0.0.1:9273", one, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
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
