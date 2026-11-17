package api

import (
	"errors"
	"net/http"
	"time"

	"hclaro/internal/awsauth"
	"hclaro/internal/profiles"
	"hclaro/internal/projects"
)

// authError writes an awsauth refusal with its diagnostic code.
func authError(w http.ResponseWriter, hub *projects.Hub, err error) {
	var e *awsauth.Error
	if errors.As(err, &e) {
		code := 409
		if e.Code == awsauth.CodeError {
			code = 400
		}
		writeJSON(w, code, map[string]string{"error": "auth_refused", "code": e.Code, "reason": e.Message})
		return
	}
	if errors.Is(err, profiles.ErrNotFound) {
		reasonJSON(w, 404, "profile_not_found", "This execution profile no longer exists. Reload the profile list.")
		return
	}
	projectError(w, hub, err)
}

// authRoutes serve AWS authentication (M20). Tool and profile discovery are local; verify
// and sign-in run only on these explicit POSTs. Requests name a registered project and a
// stored profile, never a path, command, profile option or environment value. Responses
// carry profile names, the STS account and ARN, timestamps and codes, never credentials or
// CLI output.
func authRoutes(mux *http.ServeMux, hub *projects.Hub) {
	// Local detection runs a few short AWS CLI commands; verify waits up to 20 seconds for STS.
	const authTimeout = 45 * time.Second
	available := func(w http.ResponseWriter) bool {
		if hub.Auth == nil {
			reasonJSON(w, 409, "auth_unavailable", "AWS authentication is not available in this session.")
			return false
		}
		return true
	}
	target := func(w http.ResponseWriter, r *http.Request) (awsauth.Target, bool) {
		if !available(w) {
			return awsauth.Target{}, false
		}
		if hub.Profiles == nil {
			reasonJSON(w, 409, "profiles_unavailable", "Execution profiles are not available in this session.")
			return awsauth.Target{}, false
		}
		id := r.PathValue("pid")
		if !profiles.ValidID(id) {
			reasonJSON(w, 404, "profile_not_found", "This execution profile no longer exists. Reload the profile list.")
			return awsauth.Target{}, false
		}
		t, err := hub.AuthTarget(r.PathValue("id"), id)
		if err != nil {
			authError(w, hub, err)
			return awsauth.Target{}, false
		}
		return t, true
	}
	mux.HandleFunc("GET /api/v1/auth/aws/tools", func(w http.ResponseWriter, r *http.Request) {
		if !available(w) {
			return
		}
		ctx, cancel := withDeadline(w, r, authTimeout)
		defer cancel()
		writeJSON(w, 200, hub.Auth.DetectTools(ctx, r.URL.Query().Get("refresh") == "1"))
	})
	mux.HandleFunc("GET /api/v1/auth/aws/profiles", func(w http.ResponseWriter, r *http.Request) {
		if !available(w) {
			return
		}
		ctx, cancel := withDeadline(w, r, authTimeout)
		defer cancel()
		names, err := hub.Auth.Profiles(ctx)
		if err != nil {
			authError(w, hub, err)
			return
		}
		writeJSON(w, 200, map[string]any{"profiles": names})
	})
	mux.HandleFunc("GET /api/v1/projects/{id}/execution-profiles/{pid}/aws", func(w http.ResponseWriter, r *http.Request) {
		t, ok := target(w, r)
		if !ok {
			return
		}
		ctx, cancel := withDeadline(w, r, authTimeout)
		defer cancel()
		writeJSON(w, 200, hub.Auth.Status(ctx, t))
	})
	mux.HandleFunc("POST /api/v1/projects/{id}/execution-profiles/{pid}/aws/verify", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		t, ok := target(w, r)
		if !ok {
			return
		}
		ctx, cancel := withDeadline(w, r, authTimeout)
		defer cancel()
		s, err := hub.Auth.Verify(ctx, t)
		if err != nil {
			authError(w, hub, err)
			return
		}
		writeJSON(w, 200, s)
	})
	mux.HandleFunc("POST /api/v1/projects/{id}/execution-profiles/{pid}/aws/login", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode string `json:"mode"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.Mode != awsauth.ModeSSO && body.Mode != awsauth.ModeLogin {
			errorJSON(w, 400, "invalid_request")
			return
		}
		t, ok := target(w, r)
		if !ok {
			return
		}
		ctx, cancel := withDeadline(w, r, authTimeout)
		defer cancel()
		s, err := hub.Auth.StartLogin(ctx, t, body.Mode)
		if err != nil {
			authError(w, hub, err)
			return
		}
		writeJSON(w, 202, s)
	})
	mux.HandleFunc("POST /api/v1/projects/{id}/execution-profiles/{pid}/aws/login/cancel", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		t, ok := target(w, r)
		if !ok {
			return
		}
		writeJSON(w, 200, map[string]bool{"cancelled": hub.Auth.CancelLogin(t)})
	})
}
