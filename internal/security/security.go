package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"mime"
	"net/http"
	"regexp"
	"strings"
)

// MaxBody bounds request bodies for the few mutating routes.
const MaxBody = 16 << 10

func Token() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// mutating lists the only routes that accept POST: start a plan, approve a reviewed plan,
// apply it with that one-time approval, and cancel a running operation.
var mutating = regexp.MustCompile(`^/api/v1/(plans|plans/[0-9a-f]{32}/(approval|apply)|operations/[0-9a-f]{32}/cancel)$`)

func equal(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// Guard uses an exact socket authority, not a suffix or a client-supplied forwarded host.
// Every API request needs the session bearer token; mutating requests additionally need an
// exact same-origin Origin header, a JSON body and the per-launch CSRF token header.
func Guard(host, token, csrf string, next http.Handler) http.Handler {
	origin := "http://" + host
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Host != host || r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "forbidden local origin", http.StatusForbidden)
			return
		}
		if len(r.URL.RawQuery) > 4096 || len(r.TransferEncoding) > 0 {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		switch {
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
			if r.ContentLength != 0 {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
		case r.Method == http.MethodPost && mutating.MatchString(r.URL.Path):
			fetchSite := r.Header.Get("Sec-Fetch-Site")
			if r.Header.Get("Origin") != origin || fetchSite != "" && fetchSite != "same-origin" {
				http.Error(w, "forbidden local origin", http.StatusForbidden)
				return
			}
			if !equal(r.Header.Get("X-Hclaro-CSRF"), csrf) {
				http.Error(w, "missing or invalid request token", http.StatusForbidden)
				return
			}
			if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
				http.Error(w, "JSON body required", http.StatusUnsupportedMediaType)
				return
			}
			if r.ContentLength <= 0 || r.ContentLength > MaxBody {
				http.Error(w, "request body size not accepted", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
		default:
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			authorization := r.Header.Get("Authorization")
			if !strings.HasPrefix(authorization, "Bearer ") || !equal(strings.TrimPrefix(authorization, "Bearer "), token) {
				http.Error(w, "session authorization required", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
