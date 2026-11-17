package api

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/awsauth"
	"hclaro/internal/preflight"
)

// authServer is a profile server with an AWS authentication manager whose commands and STS
// probe are synthetic: nothing runs and AWS is never contacted.
func authServer(t *testing.T) (*hubServer, *[]string) {
	t.Helper()
	s := profileServer(t)
	calls := &[]string{}
	m := awsauth.New()
	m.Run = func(ctx context.Context, environ []string, name string, args ...string) ([]byte, []byte, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		switch name + " " + strings.Join(args, " ") {
		case "aws --version":
			return []byte("aws-cli/2.17.0 Python/3.12 (synthetic)"), nil, nil
		case "aws configure list-profiles":
			return []byte("dev\nprod\n" + fixtures.Canary + " AKIA_TEST_CANARY\n"), nil, nil
		case "aws-vault --version":
			return nil, nil, exec.ErrNotFound
		}
		return nil, []byte(fixtures.Canary), errors.New("exit status 1")
	}
	m.Probe = func(ctx context.Context, environ []string) preflight.Identity {
		*calls = append(*calls, "sts")
		return preflight.Identity{State: "verified", Account: "444455556666", ARN: "arn:aws:sts::444455556666:assumed-role/Deploy/s", At: "2026-11-16T00:00:00Z"}
	}
	s.hub.Auth = m
	return s, calls
}

func TestAuthRoutes(t *testing.T) {
	s, calls := authServer(t)
	network, other := s.add("network").ID, s.add("platform").ID
	w := s.do("POST", "/api/v1/projects/"+network+"/execution-profiles", `{"name":"Production","awsProfile":"prod","awsAuth":"aws-cli","awsExpectedAccount":"111122223333"}`)
	if w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	p := decode[profileView](t, w)
	if p.AWSAuth != "aws-cli" || p.AWSExpectedAccount != "111122223333" {
		t.Fatalf("%+v", p)
	}
	item := "/api/v1/projects/" + network + "/execution-profiles/" + p.ID + "/aws"
	noLeak := func(w interface{ String() string }) {
		t.Helper()
		for _, c := range []string{fixtures.Canary, "AKIA_TEST_CANARY"} {
			if strings.Contains(w.String(), c) {
				t.Fatalf("%s leaked: %s", c, w)
			}
		}
	}
	w = s.do("GET", "/api/v1/auth/aws/profiles", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `["dev","prod"]`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	noLeak(w.Body)
	// Viewing the status never contacts AWS.
	w = s.do("GET", item, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"not_checked"`) || strings.Contains(strings.Join(*calls, ","), "sts") {
		t.Fatalf("%d %s %v", w.Code, w.Body, *calls)
	}
	noLeak(w.Body)
	// Verify and sign-in need the CSRF header and the exact origin.
	for _, path := range []string{item + "/verify", item + "/login", item + "/login/cancel"} {
		if w := s.do("POST", path, `{}`, without("X-Hclaro-CSRF")); w.Code != 403 {
			t.Fatalf("%s without CSRF: %d", path, w.Code)
		}
		if w := s.do("POST", path, `{}`, with("Origin", "https://evil.example")); w.Code != 403 {
			t.Fatalf("%s cross-origin: %d", path, w.Code)
		}
	}
	w = s.do("POST", item+"/verify", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"identity_mismatch"`) || !strings.Contains(w.Body.String(), "AUTH_ACCOUNT_MISMATCH") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// No command, path, profile option or environment can be passed.
	for _, body := range []string{`{"mode":"shell"}`, `{"mode":"sso","profile":"--endpoint-url"}`, `{"mode":"sso","command":"env"}`} {
		if w := s.do("POST", item+"/login", body); w.Code != 400 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := s.do("POST", item+"/verify", `{"environ":["AWS_PROFILE=x"]}`); w.Code != 400 {
		t.Fatalf("verify with fields: %d", w.Code)
	}
	// The profile is not SSO (configure get fails), so sign-in is refused with a reason.
	if w := s.do("POST", item+"/login", `{"mode":"sso"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "AUTH_LOGIN_UNSUPPORTED") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// Another project cannot reach this profile.
	if w := s.do("GET", "/api/v1/projects/"+other+"/execution-profiles/"+p.ID+"/aws", ""); w.Code != 404 {
		t.Fatalf("cross-project: %d %s", w.Code, w.Body)
	}
	if w := s.do("POST", "/api/v1/projects/"+network+"/execution-profiles/not-an-id/aws/verify", `{}`); w.Code != 404 {
		t.Fatalf("bad id: %d", w.Code)
	}
	// Option-injection and aws-vault without a profile name are refused when saved.
	for _, body := range []string{`{"name":"x","awsProfile":"--endpoint-url"}`, `{"name":"y","awsAuth":"aws-vault"}`, `{"name":"z","awsAuth":"sh -c id","awsProfile":"dev"}`, `{"name":"w","awsExpectedRole":"Admin"}`} {
		if w := s.do("POST", "/api/v1/projects/"+network+"/execution-profiles", body); w.Code != 400 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
}

func TestAuthRoutesWithoutManager(t *testing.T) {
	s := profileServer(t)
	if w := s.do("GET", "/api/v1/auth/aws/tools", ""); w.Code != 409 || !strings.Contains(w.Body.String(), "auth_unavailable") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
