package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"mime"
	"net/http"
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

func equal(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// refuse answers in the API's {error, reason} shape with fixed text; nothing from the request
// is echoed.
func refuse(w http.ResponseWriter, status int, code, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "reason": reason})
}

// Guard uses an exact socket authority, not a suffix or a client-supplied forwarded host.
// Every API request needs the session bearer token; mutating requests additionally need an
// exact same-origin Origin header, a JSON body and the per-launch CSRF token header. Which
// routes exist is the router's decision: the guard only classifies by method (POST, PATCH
// and DELETE under /api/ mutate; GET and HEAD read; everything else is refused).
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
			refuse(w, http.StatusForbidden, "forbidden_origin", "This request did not come from the Hclaro page on this address. Open the private URL printed by the running Hclaro CLI.")
			return
		}
		if len(r.URL.RawQuery) > 4096 || len(r.TransferEncoding) > 0 {
			refuse(w, http.StatusBadRequest, "invalid_request", "The request was refused. Reload the page and try again.")
			return
		}
		switch {
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
			if r.ContentLength != 0 {
				refuse(w, http.StatusBadRequest, "invalid_request", "The request was refused. Reload the page and try again.")
				return
			}
		case strings.HasPrefix(r.URL.Path, "/api/") && (r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodDelete):
			fetchSite := r.Header.Get("Sec-Fetch-Site")
			if r.Header.Get("Origin") != origin || fetchSite != "" && fetchSite != "same-origin" {
				refuse(w, http.StatusForbidden, "forbidden_origin", "This request did not come from the Hclaro page on this address. Open the private URL printed by the running Hclaro CLI.")
				return
			}
			if !equal(r.Header.Get("X-Hclaro-CSRF"), csrf) {
				refuse(w, http.StatusForbidden, "csrf_invalid", "The page's request token is missing or out of date. Reload the page and try again.")
				return
			}
			if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
				refuse(w, http.StatusUnsupportedMediaType, "json_required", "The request must send a JSON body.")
				return
			}
			if r.ContentLength <= 0 || r.ContentLength > MaxBody {
				refuse(w, http.StatusRequestEntityTooLarge, "body_size", "The request body is empty or too large.")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
		default:
			w.Header().Set("Allow", "GET, HEAD")
			refuse(w, http.StatusMethodNotAllowed, "method_not_allowed", "This request method is not allowed.")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			authorization := r.Header.Get("Authorization")
			if !strings.HasPrefix(authorization, "Bearer ") || !equal(strings.TrimPrefix(authorization, "Bearer "), token) {
				refuse(w, http.StatusUnauthorized, "unauthorized", "This session is locked. Open the private URL printed by the running Hclaro CLI.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
