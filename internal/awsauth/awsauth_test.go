package awsauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"hclaro/internal/awscli"
	"hclaro/internal/engine"
	"hclaro/internal/ids"
	"hclaro/internal/profiles"
)

// Every test here runs the synthetic aws, aws-vault and engine (scripts/fake-*) from a
// private PATH with a sandboxed home and AWS configuration: the real AWS CLI, aws-vault,
// ~/.aws and AWS are unreachable.

var canaries = []string{"DO_NOT_LEAK_HCLARO_TEST_SECRET", "AKIA_TEST_CANARY", "SESSION_TOKEN_TEST_CANARY", "user_code", "awsapps.example"}

type fakes struct {
	bin, awsSpy, vaultSpy, engineSpy string
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

var (
	buildOnce sync.Once
	builtDir  string
	buildErr  error
)

// buildFakes builds the synthetic tools once per test binary.
func buildFakes() (string, error) {
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "hclaro-awsauth-fakes-")
		if err != nil {
			buildErr = err
			return
		}
		builtDir = dir
		for name, src := range map[string]string{"aws": "hclaro/scripts/fake-aws", "aws-vault": "hclaro/scripts/fake-aws-vault", "tofu": "hclaro/scripts/fake-engine"} {
			build := exec.Command("go", "build", "-o", filepath.Join(dir, exe(name)), src)
			build.Env = append(os.Environ(), "CGO_ENABLED=0")
			if out, err := build.CombinedOutput(); err != nil {
				buildErr = fmt.Errorf("%v %s", err, out)
				return
			}
		}
	})
	return builtDir, buildErr
}

func TestMain(m *testing.M) {
	code := m.Run()
	if builtDir != "" {
		os.RemoveAll(builtDir)
	}
	os.Exit(code)
}

func setup(t *testing.T, tools ...string) fakes {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the synthetic tools")
	}
	built, err := buildFakes()
	if err != nil {
		t.Fatal(err)
	}
	// Each test gets its own PATH holding only the tools it asks for.
	bin := t.TempDir()
	for _, name := range tools {
		b, err := os.ReadFile(filepath.Join(built, exe(name)))
		if err != nil || os.WriteFile(filepath.Join(bin, exe(name)), b, 0o755) != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	f := fakes{bin: bin, awsSpy: filepath.Join(home, "aws-spy"), vaultSpy: filepath.Join(home, "vault-spy"), engineSpy: filepath.Join(home, "engine-spy")}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(home, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(home, "credentials"))
	t.Setenv("HCLARO_FAKE_AWS_SPY", f.awsSpy)
	t.Setenv("HCLARO_FAKE_VAULT_SPY", f.vaultSpy)
	t.Setenv("HCLARO_FAKE_SPY", f.engineSpy)
	for _, k := range []string{"HCLARO_FAKE_AWS", "HCLARO_FAKE_AWS_LOGIN", "HCLARO_FAKE_AWS_LOGIN_CMD", "HCLARO_FAKE_VAULT", "AWS_PROFILE", "AWS_ACCESS_KEY_ID", "TF_CLI_ARGS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("HCLARO_FAKE_AWS_ACCOUNT", "111122223333")
	t.Setenv("HCLARO_FAKE_AWS_ROLE", "Deploy")
	return f
}

const projectA, projectB = "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"

// target composes a profile's environment exactly as the Hub does.
func target(t *testing.T, project string, p profiles.Profile) Target {
	t.Helper()
	if p.ID == "" {
		p.ID = ids.New()
	}
	p.ProjectID, p.Revision = project, max(p.Revision, 1)
	r := profiles.Resolve(p, t.TempDir(), os.Environ())
	environ, err := profiles.Compose(os.Environ(), r)
	if err != nil {
		t.Fatal(err)
	}
	return Target{ProjectID: project, Context: r, Environ: environ}
}

// plantInherited sets every inherited credential source an explicit AWS profile must
// suppress, with synthetic values.
func plantInherited(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"AWS_ACCESS_KEY_ID": "AKIA_TEST_CANARY", "AWS_SECRET_ACCESS_KEY": "DO_NOT_LEAK_HCLARO_TEST_SECRET", "AWS_SESSION_TOKEN": "SESSION_TOKEN_TEST_CANARY",
		"AWS_PROFILE": "inherited", "AWS_DEFAULT_PROFILE": "inherited", "AWS_ROLE_ARN": "arn:aws:iam::999999999999:role/Other",
		"AWS_WEB_IDENTITY_TOKEN_FILE": filepath.Join(t.TempDir(), "token"), "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "/v2/credentials/synthetic",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI": "http://127.0.0.1:9/synthetic", "AWS_CONTAINER_AUTHORIZATION_TOKEN": "SESSION_TOKEN_TEST_CANARY",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": filepath.Join(t.TempDir(), "auth"), "AWS_VAULT": "inherited",
	} {
		t.Setenv(k, v)
	}
}

func spyLines(t *testing.T, path string) [][]string {
	t.Helper()
	b, _ := os.ReadFile(path)
	out := [][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line != "" {
			out = append(out, strings.Split(line, "\x00"))
		}
	}
	return out
}

// noCanary fails when any planted secret or sign-in detail is in v's JSON.
func noCanary(t *testing.T, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	for _, c := range canaries {
		if strings.Contains(string(b), c) {
			t.Fatalf("%s leaked: %s", c, b)
		}
	}
}

func TestDiscoveryListsNamesOnly(t *testing.T) {
	setup(t, "aws")
	m := New()
	names, err := m.Profiles(context.Background())
	if err != nil || !slices.Equal(names, []string{"dev", "prod", "sso-dev"}) {
		t.Fatalf("%v %v", names, err)
	}
	noCanary(t, names)
	tools := m.DetectTools(context.Background(), true)
	if !tools.AWS.Supported || tools.AWS.Version != "2.17.0" || !tools.SSOLogin || tools.Login || tools.Vault.Available {
		t.Fatalf("%+v", tools)
	}
}

func TestMissingToolsAreActionable(t *testing.T) {
	setup(t)
	m := New()
	if _, err := m.Profiles(context.Background()); err == nil || err.(*Error).Code != CodeToolMissing {
		t.Fatalf("%v", err)
	}
	tg := target(t, projectA, profiles.Profile{Name: "dev", AWSProfile: "dev"})
	if s := m.Status(context.Background(), tg); s.State != ToolMissing || s.Code != CodeToolMissing {
		t.Fatalf("%+v", s)
	}
	vault := target(t, projectA, profiles.Profile{Name: "v", AWSProfile: "prod", AWSAuth: profiles.AWSVault})
	s, err := m.Verify(context.Background(), vault)
	if err != nil || s.State != ToolMissing {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestVerifyDirectAWSCLI(t *testing.T) {
	f := setup(t, "aws")
	// Inherited static keys, roles, web identity, container credentials and an enclosing
	// aws-vault session must not override the chosen profile.
	plantInherited(t)
	m := New()
	tg := target(t, projectA, profiles.Profile{Name: "dev", AWSProfile: "dev", AWSExpectedAccount: "111122223333", AWSExpectedRole: "arn:aws:sts::111122223333:assumed-role/Deploy/*"})
	if s := m.Status(context.Background(), tg); s.State != NotChecked || s.CheckedAt != "" {
		t.Fatalf("a profile name is not a session: %+v", s)
	}
	s, err := m.Verify(context.Background(), tg)
	if err != nil || s.State != Verified || s.Account != "111122223333" || s.ARN != "arn:aws:sts::111122223333:assumed-role/Deploy/demo" || s.CheckedAt == "" {
		t.Fatalf("%+v %v", s, err)
	}
	noCanary(t, s)
	var sts []string
	for _, l := range spyLines(t, f.awsSpy) {
		if slices.Contains(l, "get-caller-identity") {
			sts = l
		}
	}
	if len(sts) < 5 || sts[0] != "AWS_PROFILE=dev" || sts[1] != "KEYS=false" || sts[2] != "SET=" || sts[3] != "sts" || sts[4] != "get-caller-identity" {
		t.Fatalf("sts call %q", sts)
	}
}

// The preflight's STS probe keeps working with AWS CLI v1, which rejects --no-cli-pager.
func TestIdentityProbeWorksWithCLIv1(t *testing.T) {
	setup(t, "aws")
	t.Setenv("HCLARO_FAKE_AWS", "v1")
	if id := awscli.AWSIdentity(context.Background(), nil); id.State != "verified" || id.Account != "111122223333" {
		t.Fatalf("%+v", id)
	}
}

func TestVerifyFailureIsNotVerified(t *testing.T) {
	setup(t, "aws")
	t.Setenv("HCLARO_FAKE_AWS", "expired")
	m := New()
	s, err := m.Verify(context.Background(), target(t, projectA, profiles.Profile{Name: "dev", AWSProfile: "dev"}))
	if err != nil || s.State != AuthRequired || s.Code != CodeAuthRequired || s.Account != "" {
		t.Fatalf("%+v %v", s, err)
	}
	noCanary(t, s)
}

func TestExpectedAccountMismatch(t *testing.T) {
	setup(t, "aws")
	t.Setenv("HCLARO_FAKE_AWS_ACCOUNT", "444455556666")
	m := New()
	s, _ := m.Verify(context.Background(), target(t, projectA, profiles.Profile{Name: "prod", AWSProfile: "prod", AWSExpectedAccount: "111122223333"}))
	if s.State != Mismatch || s.Code != CodeAccountMismatch {
		t.Fatalf("%+v", s)
	}
	// A role guard is not a substring match.
	t.Setenv("HCLARO_FAKE_AWS_ACCOUNT", "111122223333")
	t.Setenv("HCLARO_FAKE_AWS_ROLE", "DeployAdmin")
	s, _ = m.Verify(context.Background(), target(t, projectA, profiles.Profile{Name: "prod", AWSProfile: "prod", AWSExpectedRole: "arn:aws:sts::111122223333:assumed-role/Deploy/*"}))
	if s.State != Mismatch {
		t.Fatalf("%+v", s)
	}
}

func waitLogin(t *testing.T, m *Manager, tg Target) Status {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		s := m.Status(context.Background(), tg)
		if s.Login != nil && s.Login.State != LoginRunning {
			return s
		}
	}
	t.Fatalf("sign-in did not finish: %+v", m.Status(context.Background(), tg).Login)
	return Status{}
}

func TestSSOLoginIsRecheckedNotAssumed(t *testing.T) {
	f := setup(t, "aws")
	m := New()
	tg := target(t, projectA, profiles.Profile{Name: "sso", AWSProfile: "sso-dev"})
	if s := m.Status(context.Background(), tg); s.ProfileKind != KindSSO || !s.CanSSOLogin || s.CanLogin {
		t.Fatalf("%+v", s)
	}
	before := m.Epoch(projectA, tg.Context.ProfileID)
	if _, err := m.StartLogin(context.Background(), tg, ModeSSO); err != nil {
		t.Fatal(err)
	}
	s := waitLogin(t, m, tg)
	if s.Login.State != LoginDone || s.State != NotChecked || s.Account != "" || !strings.Contains(s.Login.Message, "verify identity") {
		t.Fatalf("a completed sign-in must not claim verified: %+v %+v", s, s.Login)
	}
	if m.Epoch(projectA, tg.Context.ProfileID) <= before {
		t.Fatal("sign-in did not invalidate earlier reviews")
	}
	noCanary(t, s)
	var login []string
	for _, l := range spyLines(t, f.awsSpy) {
		if slices.Contains(l, "login") {
			login = l[3:]
		}
	}
	if !slices.Equal(login, []string{"sso", "login", "--profile", "sso-dev"}) {
		t.Fatalf("argv %q", login)
	}
	// A profile without IAM Identity Center gets no SSO sign-in.
	dev := target(t, projectA, profiles.Profile{Name: "dev", AWSProfile: "dev"})
	if s := m.Status(context.Background(), dev); s.ProfileKind != KindOther || s.CanSSOLogin {
		t.Fatalf("%+v", s)
	}
	if _, err := m.StartLogin(context.Background(), dev, ModeSSO); err == nil || err.(*Error).Code != CodeLoginUnsupported {
		t.Fatalf("%v", err)
	}
}

func TestLoginCancellationAndFailure(t *testing.T) {
	setup(t, "aws")
	t.Setenv("HCLARO_FAKE_AWS_LOGIN", "hang")
	m := New()
	tg := target(t, projectA, profiles.Profile{Name: "sso", AWSProfile: "sso-dev"})
	if _, err := m.StartLogin(context.Background(), tg, ModeSSO); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartLogin(context.Background(), tg, ModeSSO); err == nil || err.(*Error).Code != CodeLoginRunning {
		t.Fatalf("second sign-in: %v", err)
	}
	if !m.CancelLogin(tg) {
		t.Fatal("not cancelled")
	}
	if s := waitLogin(t, m, tg); s.Login.State != LoginCanceled || s.State == Verified {
		t.Fatalf("%+v", s.Login)
	}
	t.Setenv("HCLARO_FAKE_AWS_LOGIN", "fail")
	// The sign-in runs with the composed environment, so compose it again.
	tg = target(t, projectA, profiles.Profile{ID: tg.Context.ProfileID, Name: "sso", AWSProfile: "sso-dev"})
	if _, err := m.StartLogin(context.Background(), tg, ModeSSO); err != nil {
		t.Fatal(err)
	}
	s := waitLogin(t, m, tg)
	if s.Login.State != LoginFailed || s.Login.Code != CodeInteractive {
		t.Fatalf("%+v", s.Login)
	}
	noCanary(t, s)
}

func TestAWSLoginIsCapabilityGatedAndDistinct(t *testing.T) {
	f := setup(t, "aws")
	m := New()
	tg := target(t, projectA, profiles.Profile{Name: "dev", AWSProfile: "dev"})
	if _, err := m.StartLogin(context.Background(), tg, ModeLogin); err == nil || err.(*Error).Code != CodeLoginUnsupported {
		t.Fatalf("aws login on an unsupported CLI: %v", err)
	}
	t.Setenv("HCLARO_FAKE_AWS_LOGIN_CMD", "1")
	if tools := m.DetectTools(context.Background(), true); !tools.Login {
		t.Fatalf("%+v", tools)
	}
	if _, err := m.StartLogin(context.Background(), tg, ModeLogin); err != nil {
		t.Fatal(err)
	}
	waitLogin(t, m, tg)
	for _, l := range spyLines(t, f.awsSpy) {
		if slices.Contains(l, "sso") {
			t.Fatalf("aws login fell back to sso: %q", l)
		}
	}
}

// aws-vault runs the STS probe and the engine with the same wrapper and argument order, and
// the credentials it passes to its child never reach Hclaro's results.
func TestAWSVaultWrapsProbeAndEngine(t *testing.T) {
	f := setup(t, "aws", "aws-vault", "tofu")
	plantInherited(t)
	m := New()
	tg := target(t, projectA, profiles.Profile{Name: "prod", AWSProfile: "prod", AWSAuth: profiles.AWSVault})
	// Start both tools once so the timed STS probe does not include a cold start.
	m.DetectTools(context.Background(), true)
	s, err := m.Verify(context.Background(), tg)
	if err != nil || s.State != Verified || s.Method != profiles.AWSVault {
		t.Fatalf("%+v %v", s, err)
	}
	noCanary(t, s)
	if s.CanSSOLogin || s.CanLogin || len(s.Commands) != 1 || s.Commands[0].Command != "aws-vault exec prod -- aws sts get-caller-identity" {
		t.Fatalf("%+v", s)
	}
	notExec := func(l []string) bool { return l[0] != "exec" }
	lines := slices.DeleteFunc(spyLines(t, f.vaultSpy), notExec)
	if len(lines) == 0 || !slices.Equal(lines[0][:3], []string{"exec", "prod", "--"}) || filepath.Base(lines[0][3]) != exe("aws") || !slices.Equal(lines[0][4:7], []string{"sts", "get-caller-identity", "--output"}) {
		t.Fatalf("vault argv %q", lines)
	}
	// The aws child saw the vault's credentials only: no AWS_PROFILE, and none of the
	// inherited role, web identity or container variables (AWS_VAULT is the vault's own).
	for _, l := range spyLines(t, f.awsSpy) {
		if slices.Contains(l, "get-caller-identity") && (l[0] != "AWS_PROFILE=" || l[1] != "KEYS=true" || l[2] != "SET=AWS_SECRET_ACCESS_KEY,AWS_SESSION_TOKEN,AWS_VAULT") {
			t.Fatalf("aws child env %q", l[:3])
		}
	}
	// The engine runs through the same wrapper; the version query does not.
	root := t.TempDir()
	cli := &engine.CLI{Name: "tofu", Executable: filepath.Join(f.bin, exe("tofu")), Root: root, Environ: tg.Environ}
	if _, err := cli.Version(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = cli.Plan(context.Background(), engine.PlanRequest{Out: filepath.Join(root, "p.tfplan")}, func([]byte) {})
	lines = slices.DeleteFunc(spyLines(t, f.vaultSpy), func(l []string) bool { return l[0] != "exec" })
	last := lines[len(lines)-1]
	if len(lines) != 2 || !slices.Equal(last[:3], []string{"exec", "prod", "--"}) || filepath.Base(last[3]) != exe("tofu") || last[4] != "plan" || !slices.Contains(last, "-input=false") {
		t.Fatalf("engine through vault %q", lines)
	}
	// A locked vault is an interactive case, not a verified session.
	t.Setenv("HCLARO_FAKE_VAULT", "locked")
	tg = target(t, projectA, profiles.Profile{ID: tg.Context.ProfileID, Name: "prod", AWSProfile: "prod", AWSAuth: profiles.AWSVault})
	s, _ = m.Verify(context.Background(), tg)
	if s.State != AuthRequired || s.Code != CodeInteractive {
		t.Fatalf("%+v", s)
	}
	noCanary(t, s)
	if _, err := m.StartLogin(context.Background(), tg, ModeSSO); err == nil || err.(*Error).Code != CodeInteractive {
		t.Fatalf("aws-vault sign-in: %v", err)
	}
}

func TestProjectsDoNotShareChecksOrLogins(t *testing.T) {
	setup(t, "aws")
	t.Setenv("HCLARO_FAKE_AWS_LOGIN", "hang")
	m := New()
	a := target(t, projectA, profiles.Profile{Name: "dev", AWSProfile: "sso-dev"})
	b := target(t, projectB, profiles.Profile{ID: a.Context.ProfileID, Name: "prod", AWSProfile: "sso-dev"})
	if _, err := m.Verify(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if s := m.Status(context.Background(), b); s.State != NotChecked {
		t.Fatalf("project B saw project A's check: %+v", s)
	}
	if _, err := m.StartLogin(context.Background(), b, ModeSSO); err != nil {
		t.Fatal(err)
	}
	if s := m.Status(context.Background(), a); s.Login != nil || s.State != Verified {
		t.Fatalf("project A saw project B's sign-in: %+v", s)
	}
	m.CancelLogin(b)
	waitLogin(t, m, b)
	// Editing the profile (a new revision) drops the check.
	a.Context.Revision++
	if s := m.Status(context.Background(), a); s.State != NotChecked {
		t.Fatalf("%+v", s)
	}
}

func TestJudgeClassifiesUnknownIdentity(t *testing.T) {
	r := profiles.Resolved{AWSProfile: "dev", AWSExpectedAccount: "111122223333"}
	for kind, want := range map[string]string{"timeout": Unknown, "network": Unknown, "credentials_expired": AuthRequired, "access_denied": AccessError, "vault_failed": AuthRequired} {
		if state, _, _ := judge(r, awscli.Identity{State: "failed", Kind: kind}); state != want {
			t.Errorf("%s: %s", kind, state)
		}
	}
	if state, _, _ := judge(r, awscli.Identity{State: "verified", Account: "111122223333", ARN: "ARN withheld"}); state != Verified {
		t.Errorf("account-only guard: %s", state)
	}
	r.AWSExpectedRole = "arn:aws:sts::111122223333:assumed-role/Deploy/*"
	if state, _, _ := judge(r, awscli.Identity{State: "verified", Account: "111122223333", ARN: "ARN withheld"}); state != Mismatch {
		t.Errorf("withheld ARN matched a role guard: %s", state)
	}
}

func TestErrorType(t *testing.T) {
	var e *Error
	if !errors.As(error(&Error{CodeError, "x"}), &e) || e.Error() != "x" {
		t.Fatal()
	}
}

// Shutdown cancels running sign-ins, so none outlives Hclaro.
func TestCloseCancelsSignIns(t *testing.T) {
	setup(t, "aws")
	t.Setenv("HCLARO_FAKE_AWS_LOGIN", "hang")
	m := New()
	tg := target(t, projectA, profiles.Profile{Name: "sso", AWSProfile: "sso-dev"})
	if _, err := m.StartLogin(context.Background(), tg, ModeSSO); err != nil {
		t.Fatal(err)
	}
	m.Close(10 * time.Second)
	if l := m.Status(context.Background(), tg).Login; l == nil || l.State != LoginCanceled {
		t.Fatalf("%+v", l)
	}
}

// A detection cut short by a cancelled request is not cached as "missing".
func TestCancelledDetectionIsNotCached(t *testing.T) {
	setup(t, "aws")
	m := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if tools := m.DetectTools(ctx, false); tools.AWS.Supported {
		t.Fatalf("%+v", tools)
	}
	if tools := m.DetectTools(context.Background(), false); !tools.AWS.Supported {
		t.Fatalf("cancelled detection was cached: %+v", tools)
	}
}

// The epoch counts a new principal even with failed checks in between (A → failure → B), and
// ignores a new session name of the same role.
func TestEpochFollowsTheLastVerifiedIdentity(t *testing.T) {
	answers := []awscli.Identity{
		{State: "verified", Account: "111122223333", ARN: "arn:aws:sts::111122223333:assumed-role/Deploy/s1"},
		{State: "verified", Account: "111122223333", ARN: "arn:aws:sts::111122223333:assumed-role/Deploy/s2"},
		{State: "failed", Kind: "credentials_expired"},
		{State: "verified", Account: "444455556666", ARN: "arn:aws:sts::444455556666:assumed-role/Deploy/s3"},
	}
	m := New()
	m.Run = func(context.Context, []string, string, ...string) ([]byte, []byte, error) {
		return []byte("aws-cli/2.17.0 Python/3.12 (synthetic)"), nil, nil
	}
	i := 0
	m.Probe = func(context.Context, []string) awscli.Identity { i++; return answers[i-1] }
	tg := Target{ProjectID: projectA, Context: profiles.Resolved{ProfileID: ids.New(), Revision: 1, AWSProfile: "dev", AWSAuth: profiles.AWSCLI}}
	want := []uint64{0, 0, 0, 1}
	for n := range answers {
		if _, err := m.Verify(context.Background(), tg); err != nil {
			t.Fatal(err)
		}
		if got := m.Epoch(projectA, tg.Context.ProfileID); got != want[n] {
			t.Fatalf("after check %d: epoch %d, want %d", n+1, got, want[n])
		}
	}
}

// AWS CLI v1 can still verify (sign-in needs v2), so it is not reported as a missing tool.
func TestCLIv1CanVerify(t *testing.T) {
	setup(t, "aws")
	t.Setenv("HCLARO_FAKE_AWS", "v1")
	m := New()
	tg := target(t, projectA, profiles.Profile{Name: "dev", AWSProfile: "sso-dev"})
	if s := m.Status(context.Background(), tg); s.State != NotChecked || s.CanSSOLogin || s.Tools.AWS.Supported {
		t.Fatalf("%+v", s)
	}
	if s, err := m.Verify(context.Background(), tg); err != nil || s.State != Verified {
		t.Fatalf("%+v %v", s, err)
	}
}

// A sign-in that clears the result does not let a second identity check run alongside.
func TestOneIdentityCheckAtATime(t *testing.T) {
	m := New()
	m.Run = func(ctx context.Context, _ []string, name string, args ...string) ([]byte, []byte, error) {
		switch strings.Join(args, " ") {
		case "--version":
			return []byte("aws-cli/2.17.0 Python/3.12 (synthetic)"), nil, nil
		case "configure get sso_session --profile dev":
			return []byte("corp"), nil, nil
		case "sso login --profile dev":
			<-ctx.Done()
			return nil, nil, ctx.Err()
		}
		return nil, nil, errors.New("exit status 1")
	}
	release := make(chan struct{})
	m.Probe = func(context.Context, []string) awscli.Identity {
		<-release
		return awscli.Identity{State: "verified", Account: "111122223333", ARN: "arn:aws:iam::111122223333:user/a"}
	}
	tg := Target{ProjectID: projectA, Context: profiles.Resolved{ProfileID: ids.New(), Revision: 1, AWSProfile: "dev", AWSAuth: profiles.AWSCLI}}
	done := make(chan struct{})
	go func() { _, _ = m.Verify(context.Background(), tg); close(done) }()
	for deadline := time.Now().Add(5 * time.Second); m.Status(context.Background(), tg).State != Checking; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("check did not start")
		}
	}
	if _, err := m.StartLogin(context.Background(), tg, ModeSSO); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Verify(context.Background(), tg); err == nil || err.(*Error).Code != CodeLoginRunning {
		t.Fatalf("second check ran alongside: %v", err)
	}
	close(release)
	<-done
	// The check that started before the sign-in answered afterwards: it is discarded.
	if s := m.Status(context.Background(), tg); s.State == Verified || s.Account != "" {
		t.Fatalf("a check from before the sign-in marked the profile verified: %+v", s)
	}
	m.Close(5 * time.Second)
}

// M1/L2: an identity check whose probe answers after a sign-in started, or after its profile
// was forgotten, changes nothing: no result, no verified identity, no epoch change.
func TestStaleIdentityCheckIsDiscarded(t *testing.T) {
	for _, interrupt := range []string{"sign-in", "forget"} {
		t.Run(interrupt, func(t *testing.T) {
			m := New()
			m.Run = func(ctx context.Context, _ []string, _ string, args ...string) ([]byte, []byte, error) {
				switch strings.Join(args, " ") {
				case "--version":
					return []byte("aws-cli/2.17.0 Python/3.12 (synthetic)"), nil, nil
				case "configure get sso_session --profile dev":
					return []byte("corp"), nil, nil
				case "sso login --profile dev":
					return nil, nil, nil
				}
				return nil, nil, errors.New("exit status 1")
			}
			started, release := make(chan struct{}), make(chan struct{})
			m.Probe = func(context.Context, []string) awscli.Identity {
				close(started)
				<-release
				return awscli.Identity{State: "verified", Account: "111122223333", ARN: "arn:aws:sts::111122223333:assumed-role/Deploy/old"}
			}
			tg := Target{ProjectID: projectA, Context: profiles.Resolved{ProfileID: ids.New(), Revision: 1, AWSProfile: "dev", AWSAuth: profiles.AWSCLI}}
			type result struct {
				s   Status
				err error
			}
			done := make(chan result)
			go func() { s, err := m.Verify(context.Background(), tg); done <- result{s, err} }()
			<-started
			if interrupt == "sign-in" {
				if _, err := m.StartLogin(context.Background(), tg, ModeSSO); err != nil {
					t.Fatal(err)
				}
				waitLogin(t, m, tg)
			} else {
				m.Forget(projectA, tg.Context.ProfileID)
			}
			epoch := m.Epoch(projectA, tg.Context.ProfileID)
			close(release)
			r := <-done
			if e, ok := r.err.(*Error); !ok || e.Code != CodeContextStale {
				t.Fatalf("stale check returned %+v %v", r.s, r.err)
			}
			m.mu.Lock()
			_, hasCheck := m.checks[tg.key()]
			_, hasVerified := m.verified[tg.key()]
			m.mu.Unlock()
			if hasCheck || hasVerified || m.Epoch(projectA, tg.Context.ProfileID) != epoch {
				t.Fatalf("stale check recorded: check %v verified %v", hasCheck, hasVerified)
			}
			if s := m.Status(context.Background(), tg); s.State != NotChecked {
				t.Fatalf("%+v", s)
			}
		})
	}
}

// aws-vault 6.3.1 reads an MFA code from stdin (checked with the real binary in a sandbox).
// Hclaro gives every child empty stdin, so a profile that needs a code fails at once as an
// interactive case instead of waiting, for the STS check and for engine commands alike.
func TestAWSVaultMFAFailsFastWithoutAPrompt(t *testing.T) {
	f := setup(t, "aws", "aws-vault", "tofu")
	t.Setenv("HCLARO_FAKE_VAULT", "mfa")
	m := New()
	tg := target(t, projectA, profiles.Profile{Name: "prod", AWSProfile: "prod", AWSAuth: profiles.AWSVault})
	start := time.Now()
	s, err := m.Verify(context.Background(), tg)
	if err != nil || s.State != AuthRequired || s.Code != CodeInteractive || time.Since(start) > 10*time.Second {
		t.Fatalf("%+v %v after %v", s, err, time.Since(start))
	}
	noCanary(t, s)
	if strings.Contains(s.Message, "mfa/test") {
		t.Fatalf("prompt text leaked: %q", s.Message)
	}
	cli := &engine.CLI{Name: "tofu", Executable: filepath.Join(f.bin, exe("tofu")), Root: t.TempDir(), Environ: tg.Environ}
	start = time.Now()
	if err := cli.Plan(context.Background(), engine.PlanRequest{Out: filepath.Join(t.TempDir(), "p")}, func([]byte) {}); err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("plan through an MFA vault: %v after %v", err, time.Since(start))
	}
}

// awscli.Local refuses any command not on the sign-in allowlist before starting a process (B01).
func TestRunRefusesArgvOffTheAllowlist(t *testing.T) {
	f := setup(t, "aws", "aws-vault")
	ctx := context.Background()
	for _, c := range []struct {
		name string
		args []string
	}{
		{"aws", []string{"s3", "rm", "s3://bucket", "--recursive"}},
		{"aws", []string{"sso", "login", "--profile", "-x"}},
		{"aws", []string{"sso", "login", "--profile", "dev", "--no-browser"}},
		{"aws", []string{"configure", "get", "aws_secret_access_key", "--profile", "dev"}},
		{"aws", []string{"configure", "set", "region", "x"}},
		{"aws", []string{"login", "--profile", "dev\x00"}},
		{"aws-vault", []string{"exec", "dev", "--", "sh"}},
		{"sh", []string{"--version"}},
	} {
		if _, _, err := awscli.Local(ctx, nil, c.name, c.args...); !errors.Is(err, awscli.ErrRefusedArgs) {
			t.Fatalf("%s %q: %v", c.name, c.args, err)
		}
	}
	if len(spyLines(t, f.awsSpy)) != 0 || len(spyLines(t, f.vaultSpy)) != 0 {
		t.Fatal("a refused command started a process")
	}
	for _, args := range [][]string{{"--version"}, {"configure", "list-profiles"}, {"configure", "get", "sso_session", "--profile", "sso-dev"}} {
		if _, _, err := awscli.Local(ctx, nil, "aws", args...); errors.Is(err, awscli.ErrRefusedArgs) {
			t.Fatalf("%q refused", args)
		}
	}
	if n := len(spyLines(t, f.awsSpy)); n != 3 {
		t.Fatalf("%d allowed calls recorded, want 3", n)
	}
}

// A sign-in that prints more than Hclaro reads is not treated as completed (B06: the sign-in
// path enforces the output limit like every other AWS CLI call).
func TestSignInOutputOverflowFails(t *testing.T) {
	setup(t, "aws")
	t.Setenv("HCLARO_FAKE_AWS_LOGIN", "flood")
	m := New()
	tg := target(t, projectA, profiles.Profile{Name: "sso", AWSProfile: "sso-dev"})
	if _, err := m.StartLogin(context.Background(), tg, ModeSSO); err != nil {
		t.Fatal(err)
	}
	s := waitLogin(t, m, tg)
	if s.Login.State != LoginFailed || s.Login.Code != CodeError || s.State == Verified {
		t.Fatalf("%+v", s.Login)
	}
	noCanary(t, s)
}

// A profile listing beyond the output limit still returns the first maxProfiles names
// instead of failing (the limit is enforced, but a cut listing is still usable).
func TestLargeProfileListingIsCapped(t *testing.T) {
	setup(t, "aws")
	t.Setenv("HCLARO_FAKE_AWS_LOGIN", "flood")
	names, err := New().Profiles(context.Background())
	if err != nil || len(names) != maxProfiles || names[0] != "generated-profile-00000" {
		t.Fatalf("%d names, %v", len(names), err)
	}
}
