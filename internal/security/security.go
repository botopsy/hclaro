package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
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

// BootstrapPath is the one API route the guard serves without the bearer token: the page
// trades the launch link's one-time nonce for the session token there.
const BootstrapPath = "/api/v1/session/bootstrap"

const bootstrapLifetime = 2 * time.Minute

// Bootstrap is a one-time launch credential. The CLI hands the browser opener a URL with this
// nonce instead of the session token, because a process's argv is readable by other local
// users on many systems; the nonce works once and only briefly.
type Bootstrap struct {
	mu      sync.Mutex
	nonce   string
	expires time.Time
	used    bool
	// OnReuse runs when the nonce is presented after it was used: another process may have
	// raced the browser for it.
	OnReuse func()
}

func NewBootstrap() (*Bootstrap, error) {
	n, err := Token()
	if err != nil {
		return nil, err
	}
	return &Bootstrap{nonce: n, expires: time.Now().Add(bootstrapLifetime)}, nil
}

func (b *Bootstrap) Nonce() string { return b.nonce }

func (b *Bootstrap) exchange(w http.ResponseWriter, r *http.Request, token string) {
	var body struct {
		Bootstrap string `json:"bootstrap"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		refuse(w, http.StatusBadRequest, "invalid_request", "The request was refused. Reload the page and try again.")
		return
	}
	b.mu.Lock()
	match := equal(body.Bootstrap, b.nonce)
	ok := match && !b.used && time.Now().Before(b.expires)
	reused := match && b.used
	if match {
		b.used = true
	}
	b.mu.Unlock()
	if reused && b.OnReuse != nil {
		b.OnReuse()
	}
	if !ok {
		refuse(w, http.StatusUnauthorized, "bootstrap_invalid", "This launch link was already used or has expired. Open the private URL printed by the running Hclaro CLI.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
}

// Guard uses an exact socket authority, not a suffix or a client-supplied forwarded host.
// Every API request needs the session bearer token; mutating requests additionally need an
// exact same-origin Origin header, a JSON body and the per-launch CSRF token header. Which
// routes exist is the router's decision: the guard only classifies by method (POST, PATCH
// and DELETE under /api/ mutate; GET and HEAD read; everything else is refused). With boot
// set, POST BootstrapPath gets the same origin and body checks but no token or CSRF check.
func Guard(host, token, csrf string, boot *Bootstrap, next http.Handler) http.Handler {
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
			bootstrap := boot != nil && r.Method == http.MethodPost && r.URL.Path == BootstrapPath
			if !bootstrap && !equal(r.Header.Get("X-Hclaro-CSRF"), csrf) {
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
			if bootstrap {
				boot.exchange(w, r, token)
				return
			}
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
