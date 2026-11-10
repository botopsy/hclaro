package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/imports"
	"hclaro/internal/state"
	"hclaro/internal/workspace"
)

const account = "111122223333"

// fakeRunner answers AWS CLI calls from recorded synthetic fixtures and records every argv.
type fakeRunner struct {
	mu       sync.Mutex
	calls    [][]string
	inFlight int
	peak     int
	delay    time.Duration
	// handle overrides the fixture answer; returning nil, nil falls through to fixtures.
	handle func(args []string) ([]byte, error)
	// account is what STS reports.
	account string
}

func (f *fakeRunner) Run(ctx context.Context, environ []string, args []string) ([]byte, error) {
	if !Allowed(args) {
		return nil, &CallError{KindRefused}
	}
	f.mu.Lock()
	f.calls = append(f.calls, slices.Clone(args))
	f.inFlight++
	f.peak = max(f.peak, f.inFlight)
	delay, handle := f.delay, f.handle
	acct := f.account
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, &CallError{KindCancelled}
		}
	}
	if handle != nil {
		if out, err := handle(args); out != nil || err != nil {
			return out, err
		}
	}
	return fixtureAnswer(args, acct)
}

func (f *fakeRunner) recorded() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func option(args []string, name string) string {
	if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func fixture(name string) []byte {
	b, err := fixtures.AWS.ReadFile("aws/" + name)
	if err != nil {
		panic(err)
	}
	return b
}

func fixtureAnswer(args []string, acct string) ([]byte, error) {
	if args[0] == "--version" {
		return []byte("aws-cli/2.17.0 Python/3.12.6 Linux/6 exe/x86_64\n"), nil
	}
	region := option(args, "--region")
	switch args[0] + " " + args[1] {
	case "sts get-caller-identity":
		if acct != "" && acct != account {
			return []byte(fmt.Sprintf(`{"Account":%q,"Arn":"arn:aws:sts::%s:assumed-role/Other/x"}`, acct, acct)), nil
		}
		return fixture("sts-identity.json"), nil
	case "ec2 describe-vpcs":
		if region != "eu-west-1" {
			return []byte(`{"Vpcs":[]}`), nil
		}
		if option(args, "--starting-token") != "" {
			return fixture("ec2-describe-vpcs-2.json"), nil
		}
		return fixture("ec2-describe-vpcs-1.json"), nil
	case "ec2 describe-subnets":
		if region != "eu-west-1" {
			return []byte(`{"Subnets":[]}`), nil
		}
		return fixture("ec2-describe-subnets.json"), nil
	case "ec2 describe-security-groups":
		if region != "eu-west-1" {
			return []byte(`{"SecurityGroups":[]}`), nil
		}
		return fixture("ec2-describe-security-groups.json"), nil
	case "s3api list-buckets":
		return fixture("s3api-list-buckets.json"), nil
	case "rds describe-db-instances":
		if region != "eu-west-1" {
			return []byte(`{"DBInstances":[]}`), nil
		}
		return fixture("rds-describe-db-instances.json"), nil
	}
	return nil, &CallError{KindFailed}
}

func pin() Pin {
	return Pin{ContextFingerprint: "inherited", Workspace: "default", Engine: "terraform", AWSRegion: "eu-west-1"}
}

func newService(r Runner) *Service {
	s := New(r)
	s.Sleep = func(context.Context, time.Duration) error { return nil }
	return s
}

// snapshot builds a ready snapshot from managed resources (type, address, attributes).
func snapshot(resources ...state.Resource) workspace.Snapshot {
	idx := state.Empty()
	for i, r := range resources {
		if r.Mode == "" {
			r.Mode = "managed"
		}
		r.ID = fmt.Sprintf("%032x", i+1)
		idx.Resources = append(idx.Resources, r)
		idx.ByID[r.ID] = r
	}
	return workspace.Snapshot{Status: "ready", Index: idx, Info: workspace.Info{Workspace: "default", Engine: "terraform"}}
}

func res(typ, address string, attrs map[string]any) state.Resource {
	return state.Resource{Type: typ, Address: address, Attributes: attrs}
}

func verify(t *testing.T, s *Service, p Pin) CloudIdentity {
	t.Helper()
	id, err := s.Verify(context.Background(), p, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return id
}

func wait(t *testing.T, s *Service, id string, p Pin, snap workspace.Snapshot) ScanView {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		v, err := s.View(id, p, snap)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status != ScanRunning {
			return v
		}
	}
	t.Fatal("scan did not finish")
	return ScanView{}
}

func scanAll(t *testing.T, s *Service, scope DiscoveryScope, snap workspace.Snapshot) ScanView {
	t.Helper()
	id := verify(t, s, pin())
	scan, err := s.Start(pin(), nil, id.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	return wait(t, s, scan.ID, pin(), snap)
}

func allTypes() []string {
	out := []string{}
	for _, t := range Types {
		out = append(out, t.Type)
	}
	return out
}

func find(v ScanView, typ, id string) *CandidateView {
	for i := range v.Results {
		if v.Results[i].Type == typ && v.Results[i].ResourceID == id {
			return &v.Results[i]
		}
	}
	return nil
}

func TestAllowedRefusesEverythingButReadOnlyCalls(t *testing.T) {
	ok := [][]string{
		{"--version"},
		{"sts", "get-caller-identity", "--output", "json", "--region", "eu-west-1"},
		{"ec2", "describe-vpcs", "--output", "json", "--region", "us-east-1", "--max-items", "100", "--starting-token", "eyJhIjoxfQ=="},
		{"s3api", "list-buckets", "--output", "json"},
		{"rds", "describe-db-instances", "--region", "ap-southeast-2"},
	}
	for _, args := range ok {
		if !Allowed(args) {
			t.Errorf("refused %v", args)
		}
	}
	refused := [][]string{
		{},
		{"ec2", "create-vpc"},
		{"ec2", "delete-vpc", "--vpc-id", "vpc-1"},
		{"ec2", "modify-vpc-attribute"},
		{"ec2", "run-instances"},
		{"s3api", "delete-bucket"},
		{"s3api", "put-bucket-policy"},
		{"s3", "rm", "s3://x"},
		{"rds", "delete-db-instance"},
		{"iam", "list-users"},
		{"configure", "set"},
		{"ec2", "describe-vpcs", "--cli-input-json", "file://x"},
		{"ec2", "describe-vpcs", "--endpoint-url", "http://evil"},
		{"ec2", "describe-vpcs", "--profile", "prod"},
		{"ec2", "describe-vpcs", "--query", "Vpcs"},
		{"ec2", "describe-vpcs", "--region", "eu-west-1; rm -rf /"},
		{"ec2", "describe-vpcs", "--region", "eu-west-1", "--region", "us-east-1"},
		{"ec2", "describe-vpcs", "--max-items", "0"},
		{"ec2", "describe-vpcs", "--output", "text"},
		{"ec2", "describe-vpcs", "--starting-token"},
		{"ec2", "describe-vpcs", "--debug"},
		{"--version", "x"},
	}
	for _, args := range refused {
		if Allowed(args) {
			t.Errorf("allowed %v", args)
		}
	}
	if _, err := (Exec{}).Run(context.Background(), nil, []string{"ec2", "delete-vpc"}); KindOf(err) != KindRefused {
		t.Fatalf("Exec ran a refused command: %v", err)
	}
}

func TestEnvironDisablesPagerPromptsAndRetries(t *testing.T) {
	env := Environ([]string{"AWS_PAGER=less", "aws_cli_auto_prompt=on", "AWS_MAX_ATTEMPTS=10", "AWS_DEFAULT_OUTPUT=table", "AWS_PROFILE=dev", "HOME=/home/x"})
	joined := strings.Join(env, "\n")
	for _, bad := range []string{"less", "=on", "=10", "table"} {
		if strings.Contains(joined, bad) {
			t.Errorf("environment kept %q: %v", bad, env)
		}
	}
	for _, want := range []string{"AWS_PROFILE=dev", "AWS_PAGER=", "AWS_CLI_AUTO_PROMPT=off", "AWS_MAX_ATTEMPTS=1"} {
		if !slices.Contains(env, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"Error when retrieving token from sso: Token has expired and refresh failed":                                KindExpired,
		"An error occurred (ExpiredToken) when calling the GetCallerIdentity operation":                             KindExpired,
		"The config profile (prod) could not be found":                                                              KindProfileNotFound,
		"Unable to locate credentials. You can configure credentials by running \"aws configure\".":                 KindNoCredentials,
		"An error occurred (Throttling) when calling the DescribeVpcs operation: Rate exceeded":                     KindThrottled,
		"An error occurred (RequestLimitExceeded) when calling the DescribeSubnets operation":                       KindThrottled,
		"An error occurred (UnauthorizedOperation) when calling the DescribeVpcs operation: You are not authorized": KindAccessDenied,
		"An error occurred (AccessDenied) when calling the ListBuckets operation: Access Denied":                    KindAccessDenied,
		"An error occurred (InvalidClientTokenId) when calling the GetCallerIdentity operation":                     KindInvalid,
		"An error occurred (AuthFailure) when calling the DescribeVpcs operation: AWS was not able to validate":     KindRegionDisabled,
		"Could not connect to the endpoint URL: \"https://ec2.eu-west-1.amazonaws.com/\"":                           KindNetwork,
		"something else entirely": KindFailed,
	}
	for in, want := range cases {
		if got := Classify(in); got != want {
			t.Errorf("Classify(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestVerifyRequiresCLIv2AndKeepsOnlySafeIdentity(t *testing.T) {
	r := &fakeRunner{handle: func(args []string) ([]byte, error) {
		if args[0] == "--version" {
			return []byte("aws-cli/1.29.0 Python/3.11 Darwin/23 botocore/1.31"), nil
		}
		return nil, nil
	}}
	s := newService(r)
	if _, err := s.Verify(context.Background(), pin(), nil); KindOf(err) != KindUnsupportedCLI {
		t.Fatalf("v1 accepted: %v", err)
	}
	r.handle = nil
	id := verify(t, s, pin())
	if id.Account != account || id.PrincipalType != "assumed-role" || id.Region != "eu-west-1" || id.RegionSource != "profile" || id.CLIVersion != "2.17.0" {
		t.Fatalf("identity %+v", id)
	}
	b, _ := json.Marshal(id)
	// The user ID and the session name (an e-mail here) are never kept.
	for _, leak := range []string{"AROASYNTHETIC", "demo.user@example.com", "demo-session"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("identity leaks %q: %s", leak, b)
		}
	}
	if !strings.Contains(id.ARN, "assumed-role/HclaroDemoReadOnly/") {
		t.Errorf("role not shown: %s", id.ARN)
	}
	if got, ok := s.Identity(pin()); !ok || got.ID != id.ID {
		t.Fatal("verification not found for its own context")
	}
	other := pin()
	other.Workspace = "prod"
	if _, ok := s.Identity(other); ok {
		t.Fatal("verification reused for another workspace")
	}
}

func TestVerifyExpiredSSO(t *testing.T) {
	r := &fakeRunner{handle: func(args []string) ([]byte, error) {
		if args[0] == "sts" {
			return nil, &CallError{Classify("Error loading SSO Token: Token for my-sso does not exist / Token has expired")}
		}
		return nil, nil
	}}
	if _, err := newService(r).Verify(context.Background(), pin(), nil); KindOf(err) != KindExpired {
		t.Fatalf("got %v", err)
	}
}

func TestScanPaginationMatchingAndRedaction(t *testing.T) {
	r := &fakeRunner{}
	s := newService(r)
	snap := snapshot(
		res("aws_s3_bucket", "aws_s3_bucket.artifacts", map[string]any{"id": "hclaro-demo-artifacts", "bucket": "hclaro-demo-artifacts", "arn": "arn:aws:s3:::hclaro-demo-artifacts"}),
		res("aws_s3_bucket", "module.storage.aws_s3_bucket.main[0]", map[string]any{"id": "demo-storage-000"}),
		res("aws_db_instance", "module.database.aws_db_instance.main[0]", map[string]any{"id": "db-ABCDEFGHIJKL", "identifier": "demo-database-000", "arn": "arn:aws:rds:eu-west-1:111122223333:db:demo-database-000"}),
		res("aws_security_group", "aws_security_group.web", map[string]any{"id": "sg-0123456789abcdef0"}),
		// A data source never counts as a binding.
		state.Resource{Mode: "data", Type: "aws_vpc", Address: "data.aws_vpc.main", Attributes: map[string]any{"id": "vpc-0a1b2c3d4e5f60001"}},
	)
	v := scanAll(t, s, DiscoveryScope{Regions: []string{"eu-west-1", "us-east-1"}, Types: allTypes()}, snap)
	if v.Status != ScanCompleted || !v.Current || v.Withheld {
		t.Fatalf("status %s current %v: %+v", v.Status, v.Current, v.Tasks)
	}
	// VPCs: two pages in eu-west-1; the second request carries the first page's token.
	var vpcTask Task
	for _, task := range v.Tasks {
		if task.Type == "aws_vpc" && task.Region == "eu-west-1" {
			vpcTask = task
		}
	}
	if vpcTask.Pages != 2 || vpcTask.Count != 3 || vpcTask.Status != TaskDone {
		t.Fatalf("vpc task %+v", vpcTask)
	}
	paged := false
	for _, c := range r.recorded() {
		if len(c) > 1 && c[1] == "describe-vpcs" && option(c, "--starting-token") == "eyJOZXh0VG9rZW4iOiAicGFnZS0yIn0=" {
			paged = true
		}
	}
	if !paged {
		t.Fatal("second page was not requested with the returned token")
	}
	// S3 is global: one task, and buckets outside the scanned regions are listed.
	s3 := 0
	for _, task := range v.Tasks {
		if task.Type == "aws_s3_bucket" {
			s3++
		}
	}
	if s3 != 1 {
		t.Fatalf("S3 listed %d times", s3)
	}
	want := map[[2]string]string{
		{"aws_s3_bucket", "hclaro-demo-artifacts"}:     MatchTracked,
		{"aws_s3_bucket", "demo-storage-000"}:          MatchTracked,
		{"aws_s3_bucket", "hclaro-legacy-logs"}:        MatchPossiblyUntracked,
		{"aws_s3_bucket", "hclaro-us-archive"}:         MatchPossiblyUntracked,
		{"aws_db_instance", "demo-database-000"}:       MatchTracked,
		{"aws_db_instance", "legacy-reporting"}:        MatchPossiblyUntracked,
		{"aws_security_group", "sg-0123456789abcdef0"}: MatchTracked,
		{"aws_vpc", "vpc-0a1b2c3d4e5f60001"}:           MatchPossiblyUntracked,
		{"aws_subnet", "subnet-0f1e2d3c4b5a60001"}:     MatchPossiblyUntracked,
		{"aws_subnet", "subnet-0f1e2d3c4b5a60002"}:     MatchUnresolved,
	}
	for key, status := range want {
		c := find(v, key[0], key[1])
		if c == nil {
			t.Errorf("missing %v", key)
			continue
		}
		if c.Match.Status != status {
			t.Errorf("%v: %s (%s), want %s", key, c.Match.Status, c.Match.Reason, status)
		}
	}
	if c := find(v, "aws_s3_bucket", "hclaro-demo-artifacts"); c != nil && (len(c.Match.Addresses) != 1 || c.Match.Addresses[0] != "aws_s3_bucket.artifacts") {
		t.Errorf("tracked address %v", c.Match.Addresses)
	}
	if c := find(v, "aws_subnet", "subnet-0f1e2d3c4b5a60002"); c == nil || c.ImportID != "" || !strings.Contains(c.ImportNote, "444455556666") {
		t.Errorf("shared subnet offered an import ID: %+v", c)
	}
	if v.Summary[MatchTracked] != 4 {
		t.Errorf("summary %v", v.Summary)
	}
	// Secrets in tags and responses never reach the view.
	b, _ := json.Marshal(v)
	out := string(b)
	for _, leak := range []string{"DO_NOT_LEAK", "hunter2", "not-a-real-token", "IpPermissions", "0.0.0.0/0", "MasterUsername", "rds.amazonaws.com", "5432", "synthetic-owner", "‮", "AROASYNTHETIC"} {
		if strings.Contains(out, leak) {
			t.Errorf("view leaks %q", leak)
		}
	}
	vpc := find(v, "aws_vpc", "vpc-0a1b2c3d4e5f60001")
	if vpc.Name != "platform-core" || vpc.ARN != "arn:aws:ec2:eu-west-1:111122223333:vpc/vpc-0a1b2c3d4e5f60001" || vpc.Details["cidr"] != "10.0.0.0/16" {
		t.Errorf("vpc %+v", vpc.CloudResourceCandidate)
	}
	tags := map[string]string{}
	for _, tag := range vpc.Tags {
		tags[tag.Key] = tag.Value
	}
	if tags["deploy_token"] != state.Redacted || tags["note"] != state.Redacted || tags["team"] != "platform" {
		t.Errorf("tags %v", tags)
	}
	if legacy := find(v, "aws_vpc", "vpc-0a1b2c3d4e5f60003"); legacy.Name != "legacy-analytics" {
		t.Errorf("bidi control kept: %q", legacy.Name)
	}
}

func TestAccessDeniedGivesPartialResultsPerService(t *testing.T) {
	r := &fakeRunner{handle: func(args []string) ([]byte, error) {
		if args[0] == "rds" {
			return nil, &CallError{Classify("An error occurred (AccessDenied) when calling the DescribeDBInstances operation")}
		}
		return nil, nil
	}}
	v := scanAll(t, newService(r), DiscoveryScope{Regions: []string{"eu-west-1"}, Types: allTypes()}, snapshot())
	if v.Status != ScanPartial {
		t.Fatalf("status %s", v.Status)
	}
	for _, task := range v.Tasks {
		switch {
		case task.Type == "aws_db_instance" && (task.Status != TaskFailed || task.Error != KindAccessDenied || !strings.Contains(task.Message, "missing, not empty")):
			t.Errorf("rds task %+v", task)
		case task.Type != "aws_db_instance" && task.Status != TaskDone:
			t.Errorf("task %+v", task)
		}
	}
	if find(v, "aws_vpc", "vpc-0a1b2c3d4e5f60001") == nil {
		t.Fatal("results of permitted services were dropped")
	}
}

func TestExpiredSSODuringScanStopsIt(t *testing.T) {
	var calls int
	r := &fakeRunner{}
	r.handle = func(args []string) ([]byte, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if args[0] != "sts" && args[0] != "--version" {
			calls++
			return nil, &CallError{KindExpired}
		}
		return nil, nil
	}
	v := scanAll(t, newService(r), DiscoveryScope{Regions: []string{"eu-west-1", "us-east-1", "us-west-2"}, Types: allTypes(), Concurrency: 1}, snapshot())
	if v.Status != ScanFailed || v.Error != KindExpired {
		t.Fatalf("status %s error %s", v.Status, v.Error)
	}
	if calls >= len(v.Tasks) {
		t.Fatalf("every task ran after the session expired (%d calls)", calls)
	}
}

func TestThrottlingRetriesWithBackoff(t *testing.T) {
	var mu sync.Mutex
	throttled := map[string]int{}
	r := &fakeRunner{handle: func(args []string) ([]byte, error) {
		if args[0] != "ec2" {
			return nil, nil
		}
		mu.Lock()
		defer mu.Unlock()
		key := args[1] + option(args, "--region") + option(args, "--starting-token")
		if throttled[key] < 2 {
			throttled[key]++
			return nil, &CallError{KindThrottled}
		}
		return nil, nil
	}}
	s := newService(r)
	var slept []time.Duration
	s.Sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		slept = append(slept, d)
		mu.Unlock()
		return nil
	}
	v := scanAll(t, s, DiscoveryScope{Regions: []string{"eu-west-1"}, Types: []string{"aws_subnet"}}, snapshot())
	if v.Status != ScanCompleted || v.Tasks[0].Retries != 2 {
		t.Fatalf("status %s tasks %+v", v.Status, v.Tasks)
	}
	if len(slept) != 2 || slept[0] != 500*time.Millisecond || slept[1] != time.Second {
		t.Fatalf("backoff %v", slept)
	}
	// Throttled on every attempt: the task fails after the attempt limit; nothing loops.
	r.handle = func(args []string) ([]byte, error) {
		if args[0] == "ec2" {
			return nil, &CallError{KindThrottled}
		}
		return nil, nil
	}
	v = scanAll(t, s, DiscoveryScope{Regions: []string{"eu-west-1"}, Types: []string{"aws_subnet"}}, snapshot())
	if v.Status != ScanFailed || v.Tasks[0].Error != KindThrottled || v.Tasks[0].Retries != maxAttempts-1 {
		t.Fatalf("status %s tasks %+v", v.Status, v.Tasks)
	}
}

func TestMultiRegionDuplicatesAreMerged(t *testing.T) {
	// A misbehaving endpoint (or a global listing) reports the same VPC in two regions.
	r := &fakeRunner{handle: func(args []string) ([]byte, error) {
		if len(args) > 1 && args[1] == "describe-vpcs" {
			return []byte(`{"Vpcs":[{"VpcId":"vpc-0a1b2c3d4e5f60001","OwnerId":"111122223333"}]}`), nil
		}
		return nil, nil
	}}
	v := scanAll(t, newService(r), DiscoveryScope{Regions: []string{"eu-west-1", "us-east-1"}, Types: []string{"aws_vpc", "aws_s3_bucket"}}, snapshot())
	vpcs := 0
	for _, c := range v.Results {
		if c.Type == "aws_vpc" {
			vpcs++
			if len(c.Regions) != 2 {
				t.Errorf("regions %v", c.Regions)
			}
		}
	}
	if vpcs != 1 || v.Merged != 1 {
		t.Fatalf("vpcs %d merged %d", vpcs, v.Merged)
	}
	// Buckets outside the scanned regions are skipped when the CLI reports their region.
	if find(v, "aws_s3_bucket", "hclaro-us-archive") == nil || find(v, "aws_s3_bucket", "hclaro-legacy-logs") == nil {
		t.Fatal("bucket missing")
	}
	v = scanAll(t, newService(&fakeRunner{}), DiscoveryScope{Regions: []string{"us-east-1"}, Types: []string{"aws_s3_bucket"}}, snapshot())
	if find(v, "aws_s3_bucket", "hclaro-legacy-logs") != nil || find(v, "aws_s3_bucket", "hclaro-us-archive") == nil {
		t.Fatal("bucket region filter not applied")
	}
}

func TestLimitTruncatesAndMarksPartial(t *testing.T) {
	v := scanAll(t, newService(&fakeRunner{}), DiscoveryScope{Regions: []string{"eu-west-1"}, Types: []string{"aws_vpc"}, MaxPerType: 2}, snapshot())
	if v.Status != ScanPartial || !v.Tasks[0].Truncated || v.Tasks[0].Count != 2 || len(v.Results) != 2 {
		t.Fatalf("status %s task %+v results %d", v.Status, v.Tasks[0], len(v.Results))
	}
}

func TestMatchIsExactAndConservative(t *testing.T) {
	c := func(typ, id, arn string) CloudResourceCandidate {
		return CloudResourceCandidate{Type: typ, ResourceID: id, ImportID: id, ARN: arn, ImportStatus: ImportIDVerified}
	}
	vpc := c("aws_vpc", "vpc-0a1b2c3d4e5f60001", "arn:aws:ec2:eu-west-1:111122223333:vpc/vpc-0a1b2c3d4e5f60001")
	cases := []struct {
		name string
		snap workspace.Snapshot
		want string
	}{
		{"tracked by ID", snapshot(res("aws_vpc", "aws_vpc.a", map[string]any{"id": "vpc-0a1b2c3d4e5f60001"})), MatchTracked},
		{"tracked by ARN", snapshot(res("aws_vpc", "aws_vpc.a", map[string]any{"arn": vpc.ARN})), MatchTracked},
		{"untracked", snapshot(res("aws_vpc", "aws_vpc.a", map[string]any{"id": "vpc-0a1b2c3d4e5f69999"})), MatchPossiblyUntracked},
		{"empty state", snapshot(), MatchPossiblyUntracked},
		{"ambiguous", snapshot(res("aws_vpc", "aws_vpc.a", map[string]any{"id": "vpc-0a1b2c3d4e5f60001"}), res("aws_vpc", "module.x.aws_vpc.b", map[string]any{"id": "vpc-0a1b2c3d4e5f60001"})), MatchUnknown},
		{"ARN conflict", snapshot(res("aws_vpc", "aws_vpc.a", map[string]any{"id": "vpc-0a1b2c3d4e5f60001", "arn": "arn:aws:ec2:us-east-1:999999999999:vpc/vpc-0a1b2c3d4e5f60001"})), MatchUnknown},
		{"redacted ID", snapshot(res("aws_vpc", "aws_vpc.a", map[string]any{"id": state.Redacted})), MatchUnknown},
		{"ID in another format", snapshot(res("aws_vpc", "aws_vpc.a", map[string]any{"id": "VPC-0A1B2C3D4E5F60001"})), MatchUnknown},
		{"no attributes", snapshot(res("aws_vpc", "aws_vpc.a", nil)), MatchUnknown},
		{"state unreadable", workspace.Snapshot{Status: "error"}, MatchUnknown},
		{"state loading", workspace.Snapshot{Status: "loading"}, MatchUnknown},
	}
	for _, tc := range cases {
		if got := Match([]CloudResourceCandidate{vpc}, tc.snap)[0]; got.Status != tc.want {
			t.Errorf("%s: %s (%s), want %s", tc.name, got.Status, got.Reason, tc.want)
		}
	}
	unresolved := vpc
	unresolved.ImportStatus, unresolved.ImportID = ImportIDUnresolved, ""
	if got := Match([]CloudResourceCandidate{unresolved}, snapshot()); got[0].Status != MatchUnresolved {
		t.Errorf("unresolved: %s", got[0].Status)
	}
	// An aws_db_instance tracked by hashicorp/aws v4 (ID = identifier) and v5 (identifier).
	db := c("aws_db_instance", "orders", "")
	for _, attrs := range []map[string]any{{"id": "orders"}, {"id": "db-ABCDEFGHIJKLMNOP", "identifier": "orders"}} {
		if got := Match([]CloudResourceCandidate{db}, snapshot(res("aws_db_instance", "aws_db_instance.o", attrs))); got[0].Status != MatchTracked {
			t.Errorf("db %v: %s", attrs, got[0].Status)
		}
	}
	// A v5 resource ID is not an import ID: the instance is not comparable.
	if got := Match([]CloudResourceCandidate{db}, snapshot(res("aws_db_instance", "aws_db_instance.o", map[string]any{"id": "db-ABCDEFGHIJKLMNOP"}))); got[0].Status != MatchUnknown {
		t.Errorf("db resource ID: %s", got[0].Status)
	}
}

func TestScanAndPrepareAreBoundToTheirContext(t *testing.T) {
	r := &fakeRunner{}
	s := newService(r)
	snap := snapshot()
	id := verify(t, s, pin())
	other := pin()
	other.ProfileID, other.ContextFingerprint, other.AWSProfile = strings.Repeat("a", 32), "other", "prod"
	// A verification of one context cannot start a scan in another.
	if _, err := s.Start(other, nil, id.ID, DiscoveryScope{Regions: []string{"eu-west-1"}, Types: []string{"aws_vpc"}}); !errors.Is(err, ErrIdentityStale) {
		t.Fatalf("cross-context start: %v", err)
	}
	scan, err := s.Start(pin(), nil, id.ID, DiscoveryScope{Regions: []string{"eu-west-1"}, Types: []string{"aws_vpc", "aws_s3_bucket"}})
	if err != nil {
		t.Fatal(err)
	}
	v := wait(t, s, scan.ID, pin(), snap)
	cand := find(v, "aws_vpc", "vpc-0a1b2c3d4e5f60001")
	// Another context sees no results and cannot prepare an import.
	for _, p := range []Pin{other, {ContextFingerprint: "inherited", Workspace: "prod", Engine: "terraform", AWSRegion: "eu-west-1"}} {
		ov, err := s.View(scan.ID, p, snap)
		if err != nil || ov.Current || !ov.Withheld || len(ov.Results) != 0 || ov.StaleReason == "" {
			t.Fatalf("view in other context: %+v %v", ov, err)
		}
		if _, err := s.Prepare(context.Background(), scan.ID, cand.ID, p, nil, snap, true); !errors.Is(err, ErrScanStale) {
			t.Fatalf("prepare in other context: %v", err)
		}
	}
	// Credentials now belong to another account: prepare refuses.
	r.mu.Lock()
	r.account = "999999999999"
	r.mu.Unlock()
	if _, err := s.Prepare(context.Background(), scan.ID, cand.ID, pin(), nil, snap, false); KindOf(err) != KindIdentityChanged {
		t.Fatalf("prepare after account change: %v", err)
	}
	// And a scan with a verification from the old account fails before listing anything.
	before := len(r.recorded())
	scan2, err := s.Start(pin(), nil, id.ID, DiscoveryScope{Regions: []string{"eu-west-1"}, Types: []string{"aws_vpc"}})
	if err != nil {
		t.Fatal(err)
	}
	v2 := wait(t, s, scan2.ID, pin(), snap)
	if v2.Status != ScanFailed || v2.Error != KindIdentityChanged || len(v2.Results) != 0 {
		t.Fatalf("scan after account change: %s %s", v2.Status, v2.Error)
	}
	for _, c := range r.recorded()[before:] {
		if c[0] != "sts" {
			t.Fatalf("listed %v after the account changed", c)
		}
	}
	r.mu.Lock()
	r.account = ""
	r.mu.Unlock()
	prefill, err := s.Prepare(context.Background(), scan.ID, cand.ID, pin(), nil, snap, false)
	if err != nil {
		t.Fatal(err)
	}
	if prefill.Type != "aws_vpc" || prefill.ImportID != "vpc-0a1b2c3d4e5f60001" || prefill.SuggestedAddress != "aws_vpc.platform_core" || prefill.Account != account {
		t.Fatalf("prefill %+v", prefill)
	}
	ok := imports.Request{Address: "aws_vpc.core", ID: prefill.ImportID, File: "imports.tf"}
	if err := s.CheckImport(prefill.Token, pin(), ok); err != nil {
		t.Fatalf("check: %v", err)
	}
	for name, tc := range map[string]struct {
		p   Pin
		req imports.Request
	}{
		"other context":   {other, ok},
		"other ID":        {pin(), imports.Request{Address: "aws_vpc.core", ID: "vpc-0a1b2c3d4e5f69999"}},
		"other type":      {pin(), imports.Request{Address: "aws_subnet.core", ID: prefill.ImportID}},
		"invalid address": {pin(), imports.Request{Address: "not an address", ID: prefill.ImportID}},
	} {
		if err := s.CheckImport(prefill.Token, tc.p, tc.req); !errors.Is(err, ErrPreparedStale) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := s.CheckImport(strings.Repeat("0", 32), pin(), ok); !errors.Is(err, ErrPreparedStale) {
		t.Errorf("unknown token: %v", err)
	}
	s.Now = func() time.Time { return time.Now().Add(time.Hour) }
	if err := s.CheckImport(prefill.Token, pin(), ok); !errors.Is(err, ErrPreparedStale) {
		t.Errorf("expired token: %v", err)
	}
	if _, err := s.Start(pin(), nil, id.ID, DiscoveryScope{Regions: []string{"eu-west-1"}, Types: []string{"aws_vpc"}}); !errors.Is(err, ErrIdentityStale) {
		t.Errorf("expired verification started a scan: %v", err)
	}
}

func TestPrepareRefusesTrackedUnresolvedAndUnacknowledgedUnknown(t *testing.T) {
	s := newService(&fakeRunner{})
	snap := snapshot(
		res("aws_s3_bucket", "aws_s3_bucket.artifacts", map[string]any{"id": "hclaro-demo-artifacts"}),
		res("aws_subnet", "aws_subnet.legacy", map[string]any{"id": state.Redacted}),
	)
	v := scanAll(t, s, DiscoveryScope{Regions: []string{"eu-west-1"}, Types: []string{"aws_subnet", "aws_s3_bucket"}}, snap)
	tracked := find(v, "aws_s3_bucket", "hclaro-demo-artifacts")
	shared := find(v, "aws_subnet", "subnet-0f1e2d3c4b5a60002")
	unknown := find(v, "aws_subnet", "subnet-0f1e2d3c4b5a60001")
	if tracked.Match.Status != MatchTracked || shared.Match.Status != MatchUnresolved || unknown.Match.Status != MatchUnknown {
		t.Fatalf("labels %s %s %s", tracked.Match.Status, shared.Match.Status, unknown.Match.Status)
	}
	for _, c := range []*CandidateView{tracked, shared} {
		if _, err := s.Prepare(context.Background(), v.ID, c.ID, pin(), nil, snap, true); !errors.Is(err, ErrNotImportable) {
			t.Errorf("%s: %v", c.ResourceID, err)
		}
	}
	if _, err := s.Prepare(context.Background(), v.ID, unknown.ID, pin(), nil, snap, false); !errors.Is(err, ErrAcknowledge) {
		t.Fatalf("unknown without acknowledgement: %v", err)
	}
	p, err := s.Prepare(context.Background(), v.ID, unknown.ID, pin(), nil, snap, true)
	if err != nil || p.Match != MatchUnknown || len(p.Warnings) < 2 {
		t.Fatalf("acknowledged unknown: %+v %v", p, err)
	}
	// An object in another region than the context default gets a provider warning.
	us := scanAll(t, s, DiscoveryScope{Regions: []string{"us-east-1"}, Types: []string{"aws_s3_bucket"}}, snap)
	bucket := find(us, "aws_s3_bucket", "hclaro-us-archive")
	p, err = s.Prepare(context.Background(), us.ID, bucket.ID, pin(), nil, snap, false)
	if err != nil || !strings.Contains(strings.Join(p.Warnings, " "), "aliased configuration for us-east-1") {
		t.Fatalf("region warning: %+v %v", p, err)
	}
	if _, err := s.Prepare(context.Background(), v.ID, strings.Repeat("f", 32), pin(), nil, snap, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown candidate: %v", err)
	}
}

func TestOnlyReadOnlyCallsAreEverMade(t *testing.T) {
	r := &fakeRunner{}
	s := newService(r)
	snap := snapshot()
	v := scanAll(t, s, DiscoveryScope{Regions: []string{"eu-west-1", "us-east-1", "eu-central-1"}, Types: allTypes()}, snap)
	c := find(v, "aws_vpc", "vpc-0a1b2c3d4e5f60001")
	if _, err := s.Prepare(context.Background(), v.ID, c.ID, pin(), nil, snap, false); err != nil {
		t.Fatal(err)
	}
	calls := r.recorded()
	if len(calls) == 0 {
		t.Fatal("no calls recorded")
	}
	for _, args := range calls {
		if !Allowed(args) {
			t.Fatalf("not allowlisted: %v", args)
		}
		if args[0] == "--version" {
			continue
		}
		op := args[1]
		if !(strings.HasPrefix(op, "describe-") || strings.HasPrefix(op, "list-") || strings.HasPrefix(op, "get-")) {
			t.Fatalf("non-read operation %v", args)
		}
	}
	if v.Calls < v.Estimate.MinCalls || v.Calls > v.Estimate.MaxCalls {
		t.Fatalf("calls %d outside estimate %+v", v.Calls, v.Estimate)
	}
}

func TestConcurrencyLimitsAndCancellation(t *testing.T) {
	r := &fakeRunner{delay: 20 * time.Millisecond}
	s := newService(r)
	id := verify(t, s, pin())
	scope := DiscoveryScope{Regions: []string{"eu-west-1", "us-east-1", "us-west-2", "eu-central-1"}, Types: allTypes(), Concurrency: 2}
	a, err := s.Start(pin(), nil, id.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, a.ID, pin(), snapshot())
	if r.peak > 2 {
		t.Fatalf("one scan ran %d reads at once (limit 2)", r.peak)
	}
	// Two scans at full concurrency share the session-wide call limit; a third is refused.
	r.mu.Lock()
	r.peak = 0
	r.mu.Unlock()
	scope.Concurrency = MaxConcurrency
	a, _ = s.Start(pin(), nil, id.ID, scope)
	b, err := s.Start(pin(), nil, id.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(pin(), nil, id.ID, scope); !errors.Is(err, ErrTooManyScans) {
		t.Fatalf("third scan: %v", err)
	}
	wait(t, s, a.ID, pin(), snapshot())
	wait(t, s, b.ID, pin(), snapshot())
	if r.peak > MaxCalls {
		t.Fatalf("%d reads at once across scans (limit %d)", r.peak, MaxCalls)
	}
	// Cancellation stops a running scan; unfinished tasks are marked cancelled.
	r.mu.Lock()
	r.delay = 200 * time.Millisecond
	r.mu.Unlock()
	c, _ := s.Start(pin(), nil, id.ID, scope)
	time.Sleep(30 * time.Millisecond)
	if _, err := s.Cancel(c.ID, ""); err != nil {
		t.Fatal(err)
	}
	v := wait(t, s, c.ID, pin(), snapshot())
	if v.Status != ScanCancelled {
		t.Fatalf("status %s", v.Status)
	}
	for _, task := range v.Tasks {
		if task.Status == TaskPending || task.Status == TaskRunning {
			t.Fatalf("task left %s", task.Status)
		}
	}
	s.Close(time.Second)
	if _, err := s.Start(pin(), nil, id.ID, scope); !errors.Is(err, ErrClosed) {
		t.Fatalf("start after close: %v", err)
	}
}

func TestScopeValidation(t *testing.T) {
	bad := []DiscoveryScope{
		{},
		{Regions: []string{"eu-west-1"}},
		{Types: []string{"aws_vpc"}},
		{Regions: []string{"mars-north-1"}, Types: []string{"aws_vpc"}},
		{Regions: []string{"eu-west-1", "eu-west-1"}, Types: []string{"aws_vpc"}},
		{Regions: []string{"eu-west-1"}, Types: []string{"aws_instance"}},
		{Regions: []string{"eu-west-1"}, Types: []string{"aws_vpc"}, MaxPerType: 5000},
		{Regions: []string{"eu-west-1"}, Types: []string{"aws_vpc"}, Concurrency: 9},
		{Regions: Regions[:7], Types: []string{"aws_vpc"}},
	}
	for _, s := range bad {
		if err := s.Validate(); !errors.Is(err, ErrInvalidScope) {
			t.Errorf("accepted %+v", s)
		}
	}
	s := DiscoveryScope{Regions: []string{"eu-west-1", "us-east-1"}, Types: []string{"aws_s3_bucket", "aws_vpc"}}
	if err := s.Validate(); err != nil || s.MaxPerType != DefaultPerType || s.Concurrency != DefaultConcurrent || s.Types[0] != "aws_vpc" {
		t.Fatalf("defaults %+v %v", s, err)
	}
	if e := EstimateScope(s); e.Tasks != 3 || e.MinCalls != 4 || e.MaxCalls != 7 {
		t.Fatalf("estimate %+v", e)
	}
}

func TestSuggestAddress(t *testing.T) {
	cases := map[string]CloudResourceCandidate{
		"aws_vpc.platform_core":             {Type: "aws_vpc", Name: "Platform Core", ResourceID: "vpc-1"},
		"aws_s3_bucket.imported_9_bucket":   {Type: "aws_s3_bucket", Name: "9-bucket", ResourceID: "9-bucket"},
		"aws_subnet.imported_subnet_0abc12": {Type: "aws_subnet", Name: state.Redacted, ResourceID: "subnet-0abc12"},
	}
	for want, c := range cases {
		if got := suggestAddress(c); got != want {
			t.Errorf("%+v: %s, want %s", c, got, want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	if v, ok := ParseVersion([]byte("aws-cli/2.15.30 Python/3.11.8 Darwin/23.3.0 exe/x86_64 prompt/off\n")); !ok || v != "2.15.30" {
		t.Fatal(v)
	}
	for _, out := range []string{"aws-cli/1.32.0 Python/3.9", "", "not aws"} {
		if _, ok := ParseVersion([]byte(out)); ok {
			t.Errorf("accepted %q", out)
		}
	}
}

func TestMalformedPagesAreRefused(t *testing.T) {
	r := &fakeRunner{handle: func(args []string) ([]byte, error) {
		if len(args) < 2 {
			return nil, nil
		}
		switch args[1] {
		case "describe-vpcs":
			return []byte(`{"Vpcs": [`), nil
		case "describe-subnets":
			return []byte(`{"Subnets": [], "NextToken": "bad token with spaces"}`), nil
		}
		return nil, nil
	}}
	v := scanAll(t, newService(r), DiscoveryScope{Regions: []string{"eu-west-1"}, Types: []string{"aws_vpc", "aws_subnet"}}, snapshot())
	for _, task := range v.Tasks {
		if task.Status != TaskFailed || task.Error != KindMalformed {
			t.Errorf("task %+v", task)
		}
	}
}
