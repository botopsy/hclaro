package preflight

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hclaro/internal/plan"
)

func base() Input {
	match := true
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	ctx := Recorded{Engine: "terraform", Version: "1.9.8", Workspace: "demo"}
	return Input{
		Now: now, Recorded: ctx, Current: ctx, FingerprintMatch: true, ArtifactRetained: true, DigestMatch: &match,
		Digest: strings.Repeat("a", 64), ExpiresAt: now.Add(30 * time.Minute), Backend: "local", ExecutionMode: "local-cli",
		CanApply: true, EngineSupported: true, Kind: "plan", Summary: map[string]int{"create": 1},
		Changes: []plan.Change{{Address: "null_resource.a", Action: plan.Create, Type: "null_resource", Provider: "registry.terraform.io/hashicorp/null"}},
		Git:     GitResult{State: GitClean}, Identity: Identity{State: "not_requested"}, Policy: DefaultPolicy(),
		PolicyInfo: PolicyInfo{Source: "default", Version: 1, Valid: true},
	}
}

func get(t *testing.T, checks []Check, id string) Check {
	t.Helper()
	for _, c := range checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("check %s missing", id)
	return Check{}
}

func blockers(checks []Check) []string {
	out := []string{}
	for _, c := range checks {
		if c.Blocks() {
			out = append(out, c.ID)
		}
	}
	return out
}

func TestEvaluateCleanInputHasNoBlockersOrWarnings(t *testing.T) {
	checks := Evaluate(base())
	for _, c := range checks {
		if c.Blocks() || c.NeedsAcknowledgement() {
			t.Fatalf("%+v", c)
		}
		if c.Status == "" || c.Severity == "" || c.Evidence == "" || c.Source == "" {
			t.Fatalf("incomplete %+v", c)
		}
	}
	// No AWS resources and no AWS profile: no identity check.
	for _, c := range checks {
		if c.ID == CheckAWS {
			t.Fatal("identity check without AWS")
		}
	}
}

func TestEvaluateBlocksEveryCriticalMismatch(t *testing.T) {
	mismatch := false
	for name, tc := range map[string]struct {
		mut   func(*Input)
		check string
	}{
		"workspace":   {func(in *Input) { in.Current.Workspace = "prod" }, CheckContext},
		"version":     {func(in *Input) { in.Current.Version = "1.10.0" }, CheckContext},
		"engine":      {func(in *Input) { in.Current.Engine = "tofu" }, CheckContext},
		"fingerprint": {func(in *Input) { in.FingerprintMatch = false }, CheckFingerprint},
		"profile":     {func(in *Input) { in.ProfileReason = "The execution profile changed." }, CheckProfile},
		"digest":      {func(in *Input) { in.DigestMatch = &mismatch }, CheckArtifact},
		"unhashable":  {func(in *Input) { in.DigestMatch = nil }, CheckArtifact},
		"gone":        {func(in *Input) { in.ArtifactRetained = false }, CheckArtifact},
		"expired":     {func(in *Input) { in.ExpiresAt = in.Now }, CheckTTL},
		"ineligible":  {func(in *Input) { in.Ineligible = "errored" }, CheckEligible},
		"remote":      {func(in *Input) { in.Backend, in.ExecutionMode = "remote", "remote-exec" }, CheckBackend},
		"unverified":  {func(in *Input) { in.Backend, in.ExecutionMode = "unknown", "unsupported" }, CheckBackend},
		"no apply":    {func(in *Input) { in.CanApply = false }, CheckBackend},
		"unsupported": {func(in *Input) { in.EngineSupported = false }, CheckEngine},
		"running":     {func(in *Input) { in.Running = true }, CheckConflict},
		"locked":      {func(in *Input) { in.LockHolder = "init" }, CheckConflict},
		"loading":     {func(in *Input) { in.Loading = true }, CheckConflict},
		"policy":      {func(in *Input) { in.PolicyInfo = PolicyInfo{Source: PolicyFile, Error: "bad"} }, CheckPolicy},
	} {
		t.Run(name, func(t *testing.T) {
			in := base()
			tc.mut(&in)
			checks := Evaluate(in)
			c := get(t, checks, tc.check)
			if !c.Blocks() || c.Source != "built-in" {
				t.Fatalf("%+v", c)
			}
		})
	}
}

func TestUnknownIsNeverPass(t *testing.T) {
	in := base()
	in.Git = GitResult{State: GitUnavailable}
	c := get(t, Evaluate(in), CheckGit)
	if c.Status != Unknown || !c.NeedsAcknowledgement() {
		t.Fatalf("%+v", c)
	}
	in.Policy.Rules.GitDirty = Block
	if c := get(t, Evaluate(in), CheckGit); !c.Blocks() {
		t.Fatalf("unknown Git status must block under a blocking rule: %+v", c)
	}
	// Outside a repository there is nothing to be dirty: info unless the policy blocks.
	in = base()
	in.Git = GitResult{State: GitNotRepo}
	if c := get(t, Evaluate(in), CheckGit); c.Status != Unknown || c.Severity != Info {
		t.Fatalf("%+v", c)
	}
	in.Policy.Rules.GitDirty = Block
	if c := get(t, Evaluate(in), CheckGit); !c.Blocks() {
		t.Fatalf("%+v", c)
	}
}

func TestProductionLabels(t *testing.T) {
	for _, tc := range []struct {
		workspace, profile, aws string
		hit                     bool
	}{
		{"demo", "", "", false},
		{"prod", "", "", true},
		{"eu-prod", "", "", true},
		{"PRODUCTION", "", "", true},
		{"product", "", "", false},
		{"staging", "Production EU", "", true},
		{"default", "", "acme-prod", true},
	} {
		in := base()
		in.Current.Workspace, in.Recorded.Workspace, in.ProfileName, in.AWSProfile = tc.workspace, tc.workspace, tc.profile, tc.aws
		c := get(t, Evaluate(in), CheckProduction)
		if (c.Status == Fail) != tc.hit || c.Severity != Warn {
			t.Fatalf("%+v: %+v", tc, c)
		}
	}
}

func TestDestructiveLimitAndInfo(t *testing.T) {
	in := base()
	in.Destructive = true
	in.Changes = []plan.Change{{Address: "a.b", Action: plan.Delete}, {Address: "a.c", Action: plan.Replace}, {Address: "a.d", Action: plan.Unknown}}
	c := get(t, Evaluate(in), CheckDestructive)
	if c.Severity != Info || c.Status != Fail || !strings.Contains(c.Evidence, "3 object") {
		t.Fatalf("%+v", c)
	}
	in.Policy.Rules.DestructiveLimit = &LimitRule{Severity: Block, Max: 2}
	if c := get(t, Evaluate(in), CheckDestructive); !c.Blocks() || c.Source != "policy" {
		t.Fatalf("%+v", c)
	}
	in.Policy.Rules.DestructiveLimit.Max = 3
	if c := get(t, Evaluate(in), CheckDestructive); c.Status != Pass {
		t.Fatalf("%+v", c)
	}
}

func TestImpactMarksRequestedAndCollateral(t *testing.T) {
	changes := []plan.Change{
		{Address: "aws_instance.web[0]", Action: plan.Replace, Type: "aws_instance"},
		{Address: "aws_instance.web[1]", Action: plan.Replace, Type: "aws_instance"},
		{Address: "aws_instance.web[1]", Action: plan.Delete, Deposed: "00000001"},
		{Address: "aws_lb_target_group_attachment.web[0]", Action: plan.Replace},
		{Address: "aws_route53_record.web", Action: plan.Update},
		{Address: "data.aws_ami.x", Action: plan.Read},
		{Address: "aws_s3_bucket.logs", Action: plan.NoOp},
	}
	im := ImpactOf("replace", []string{"aws_instance.web[0]", "aws_instance.web[1]"}, changes, nil, 0)
	if im.Total != 5 || im.Extra != 3 || im.DestructiveCount != 4 || len(im.HighRisk) != 4 || !im.UsesAWS {
		t.Fatalf("%+v", im)
	}
	if !im.Changes[0].Requested || !im.Changes[1].Requested || im.Changes[2].Requested || !im.Changes[2].Deposed || im.Changes[3].Requested {
		t.Fatalf("%+v", im.Changes)
	}
	in := base()
	in.Kind, in.Requested, in.Changes = "replace", []string{"aws_instance.web[0]", "aws_instance.web[1]"}, changes
	c := get(t, Evaluate(in), CheckScope)
	if !c.NeedsAcknowledgement() || !strings.Contains(c.Evidence, "2 requested replacement(s)") || !strings.Contains(c.Evidence, "3 other") {
		t.Fatalf("%+v", c)
	}
	in.Changes = changes[:2]
	if c := get(t, Evaluate(in), CheckScope); c.Status != Pass {
		t.Fatalf("%+v", c)
	}
}

func TestAWSIdentityCheck(t *testing.T) {
	in := base()
	in.AWSProfile = "prod-admin"
	c := get(t, Evaluate(in), CheckAWS)
	// A profile name proves nothing: not verified is unknown, not pass.
	if c.Status != Unknown || c.Severity != Info || !strings.Contains(c.Evidence, "does not infer") {
		t.Fatalf("%+v", c)
	}
	in.Identity = Identity{State: "verified", Account: "123456789012", ARN: "arn:aws:iam::123456789012:user/ci"}
	if c := get(t, Evaluate(in), CheckAWS); c.Status != Pass {
		t.Fatalf("%+v", c)
	}
	in.Policy.Rules.AWSAccounts = &AccountRule{Severity: Block, Allowed: []string{"999999999999"}}
	if c := get(t, Evaluate(in), CheckAWS); !c.Blocks() || c.Source != "policy" {
		t.Fatalf("%+v", c)
	}
	in.Identity = Identity{State: "failed", Message: "nope"}
	if c := get(t, Evaluate(in), CheckAWS); !c.Blocks() || c.Status != Unknown {
		t.Fatalf("%+v", c)
	}
}

func TestParsePolicy(t *testing.T) {
	good := `{"version":1,"rules":{"gitDirty":"block","productionLabels":{"severity":"block","patterns":["live-*"]},"destructiveLimit":{"severity":"warn","max":5},"awsAccounts":{"severity":"block","allowed":["123456789012"]}}}`
	p, err := ParsePolicy([]byte(good))
	if err != nil || p.Rules.GitDirty != Block || p.Rules.ProductionLabels.Patterns[0] != "live-*" || p.Rules.DestructiveLimit.Max != 5 || p.Rules.AWSAccounts.Allowed[0] != "123456789012" {
		t.Fatalf("%+v %v", p, err)
	}
	// Omitted rules keep defaults.
	p, err = ParsePolicy([]byte(`{"version":1}`))
	if err != nil || p.Rules.GitDirty != Warn || len(p.Rules.ProductionLabels.Patterns) == 0 {
		t.Fatalf("%+v %v", p, err)
	}
	for _, bad := range []string{
		``, `[]`, `{}`, `{"version":2}`, `{"version":"1"}`, `{"version":1}{"version":1}`,
		`{"version":1,"rules":{"exec":"rm -rf /"}}`,
		`{"version":1,"rules":{"gitDirty":"fatal"}}`,
		`{"version":1,"rules":{"productionLabels":{"severity":"warn","patterns":["$(id)"]}}}`,
		`{"version":1,"rules":{"productionLabels":{"severity":"warn","patterns":["a/b"]}}}`,
		`{"version":1,"rules":{"destructiveLimit":{"severity":"off","max":1}}}`,
		`{"version":1,"rules":{"destructiveLimit":{"severity":"block","max":-1}}}`,
		`{"version":1,"rules":{"awsAccounts":{"severity":"block","allowed":[]}}}`,
		`{"version":1,"rules":{"awsAccounts":{"severity":"block","allowed":["12345"]}}}`,
		`{"version":1,"url":"https://example.com/policy.json"}`,
	} {
		if _, err := ParsePolicy([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestLoadPolicyFailsClosed(t *testing.T) {
	root := t.TempDir()
	if _, info := LoadPolicy(root); !info.Valid || info.Source != "default" {
		t.Fatalf("%+v", info)
	}
	dir := filepath.Join(root, ".hclaro")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(file, []byte(`{"version":1,"rules":{"gitDirty":"block"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, info := LoadPolicy(root); !info.Valid || info.Source != PolicyFile || p.Rules.GitDirty != Block {
		t.Fatalf("%+v", info)
	}
	if err := os.WriteFile(file, []byte(`{"version":1,"rules":{"gitDirty":"DO_NOT_LEAK_HCLARO_TEST_SECRET"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p, info := LoadPolicy(root)
	if info.Valid || strings.Contains(info.Error, "DO_NOT_LEAK") || p.Rules.GitDirty != Warn {
		t.Fatalf("%+v", info)
	}
	if err := os.WriteFile(file, []byte(strings.Repeat(" ", maxPolicy+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, info := LoadPolicy(root); info.Valid {
		t.Fatal("oversized policy accepted")
	}
	// A symlinked policy file is refused rather than followed.
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(file)
	if err := os.Symlink(target, file); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, info := LoadPolicy(root); info.Valid {
		t.Fatal("symlinked policy accepted")
	}
}

func TestParsePorcelain(t *testing.T) {
	out := []byte(" M main.tf\x00?? notes.txt\x00R  new.tf\x00old.tf\x00A  token=DO_NOT_LEAK.tf\x00")
	r := ParsePorcelain(out)
	if r.State != GitDirty || r.Modified != 3 || r.Untracked != 1 {
		t.Fatalf("%+v", r)
	}
	if strings.Join(r.Paths, ",") != "main.tf,notes.txt,new.tf" || !r.Truncated {
		t.Fatalf("%+v", r.Paths)
	}
	if r := ParsePorcelain(nil); r.State != GitClean {
		t.Fatalf("%+v", r)
	}
}

func TestParseIdentity(t *testing.T) {
	id := ParseIdentity([]byte(`{"UserId":"AIDAEXAMPLE","Account":"123456789012","Arn":"arn:aws:sts::123456789012:assumed-role/deploy/s1"}`))
	if id.State != "verified" || id.Account != "123456789012" || id.ARN != "arn:aws:sts::123456789012:assumed-role/deploy/s1" {
		t.Fatalf("%+v", id)
	}
	// The user ID is never returned; a mismatched ARN is withheld.
	if id := ParseIdentity([]byte(`{"UserId":"AIDA","Account":"123456789012","Arn":"arn:aws:iam::999999999999:user/x"}`)); id.ARN != "ARN withheld" {
		t.Fatalf("%+v", id)
	}
	for _, bad := range []string{`{}`, `{"Account":"12"}`, `not json`} {
		if id := ParseIdentity([]byte(bad)); id.State != "failed" {
			t.Fatalf("%s: %+v", bad, id)
		}
	}
}

// The real Git probe against a disposable repository, when Git is installed.
func TestGitStatusDisposableRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	if r := GitStatus(context.Background(), root); r.State != GitNotRepo && r.State != GitClean && r.State != GitDirty {
		// TempDir may sit inside a repository on some machines; only "not a repo" or a status is valid.
		t.Fatalf("%+v", r)
	}
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", repo)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if err := cmd.Run(); err != nil {
		t.Skip("git init failed")
	}
	if r := GitStatus(context.Background(), repo); r.State != GitClean {
		t.Fatalf("%+v", r)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.tf"), []byte("# x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := GitStatus(context.Background(), repo); r.State != GitDirty || r.Untracked != 1 || r.Paths[0] != "main.tf" {
		t.Fatalf("%+v", r)
	}
}

// The documented example policy parses.
func TestExamplePolicyFixture(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "fixtures", "preflight", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePolicy(b); err != nil {
		t.Fatal(err)
	}
}

func TestSourceCheck(t *testing.T) {
	base := func(src *SourceFacts) Check {
		in := Input{Source: src}
		for _, c := range Evaluate(in) {
			if c.ID == CheckSource {
				return c
			}
		}
		return Check{}
	}
	if c := base(nil); c.ID != "" {
		t.Fatalf("check without a fingerprint: %+v", c)
	}
	for name, tc := range map[string]struct {
		src      SourceFacts
		severity Severity
		status   Status
	}{
		"unchanged":     {SourceFacts{Planned: "commit abc", Current: "commit abc"}, Block, Pass},
		"files changed": {SourceFacts{FilesChanged: true}, Block, Fail},
		"both changed":  {SourceFacts{FilesChanged: true, HeadChanged: true, Planned: "commit a", Current: "commit b"}, Block, Fail},
		"head moved":    {SourceFacts{HeadChanged: true, Planned: "commit a", Current: "commit b"}, Warn, Fail},
		"unknown":       {SourceFacts{Unknown: true}, Block, Unknown},
	} {
		c := base(&tc.src)
		if c.Severity != tc.severity || c.Status != tc.status || c.Evidence == "" {
			t.Fatalf("%s: %+v", name, c)
		}
		if tc.status != Pass && c.Remediation == "" {
			t.Fatalf("%s: no remediation", name)
		}
	}
}
