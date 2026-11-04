package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

func Token() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Guard uses an exact socket authority, not a suffix or a client-supplied forwarded host.
func Guard(host, token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Host != host || r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+host || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "forbidden local origin", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "read-only application", http.StatusMethodNotAllowed)
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) > 0 || len(r.URL.RawQuery) > 4096 {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			authorization := r.Header.Get("Authorization")
			got := strings.TrimPrefix(authorization, "Bearer ")
			if !strings.HasPrefix(authorization, "Bearer ") || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				http.Error(w, "session authorization required", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
