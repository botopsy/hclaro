package discovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"hclaro/internal/imports"
	"hclaro/internal/state"
	"hclaro/internal/workspace"
)

// Scan statuses.
const (
	ScanRunning   = "running"
	ScanCompleted = "completed"
	// ScanPartial: some reads failed (for example access denied in one service) or hit the
	// per-type limit. Objects of those tasks may be missing; missing is not "none".
	ScanPartial   = "partial"
	ScanFailed    = "failed"
	ScanCancelled = "cancelled"
)

// Task statuses.
const (
	TaskPending   = "pending"
	TaskRunning   = "running"
	TaskDone      = "done"
	TaskFailed    = "failed"
	TaskCancelled = "cancelled"
)

// Limits. A scan selects at most MaxRegions regions; each type lists at most MaxPerType
// objects per region; at most MaxConcurrency reads of one scan run at once and at most
// MaxCalls reads across all scans.
const (
	MaxRegions        = 6
	MaxPerTypeLimit   = 1000
	DefaultPerType    = 200
	MaxConcurrency    = 4
	DefaultConcurrent = 2
	PageSize          = 100
	MaxCalls          = 4
	MaxRunning        = 2
	maxScans          = 10
	maxIdentities     = 20
	maxCandidates     = 5000
	maxAttempts       = 4
)

var (
	ErrNotFound       = errors.New("not found")
	ErrTooManyScans   = errors.New("too many scans are running")
	ErrClosed         = errors.New("discovery is shutting down")
	ErrInvalidScope   = errors.New("invalid scope")
	ErrIdentityStale  = errors.New("identity verification is missing, expired or for another context")
	ErrScanStale      = errors.New("scan does not match the current context")
	ErrScanNotReady   = errors.New("scan has no results yet")
	ErrNotImportable  = errors.New("candidate cannot be imported")
	ErrAcknowledge    = errors.New("unknown mapping needs acknowledgement")
	ErrPreparedStale  = errors.New("prepared import is missing, expired or does not match")
	ErrScopeTooLarge  = errors.New("scope too large")
	errIdentityFormat = errors.New("unexpected identity output")
)

// Pin is the context a verification, scan and prepared import are bound to: the launch
// project's execution profile and its fingerprint, the AWS profile and region names, and the
// Terraform workspace and engine of the snapshot. Two pins with any difference never share a
// verification, a scan's results or a prepared import.
type Pin struct {
	// Context is the Hub read context ID of a Hub workspace, "" for the launch project.
	// Verifications, scans and prepared imports are never shared between contexts.
	Context            string `json:"contextId,omitempty"`
	ProjectID          string `json:"projectId,omitempty"`
	ProfileID          string `json:"profileId,omitempty"`
	ProfileName        string `json:"profileName,omitempty"`
	ContextFingerprint string `json:"contextFingerprint"`
	AWSProfile         string `json:"awsProfile,omitempty"`
	AWSRegion          string `json:"awsRegion,omitempty"`
	Workspace          string `json:"workspace"`
	Engine             string `json:"engine"`
}

// differs returns why current does not match p, or "".
func (p Pin) differs(current Pin) string {
	switch {
	case p.Context != current.Context || p.ProjectID != current.ProjectID:
		return "This was made for another project or workspace."
	case p.ProfileID != current.ProfileID:
		return "This tab now uses another execution profile than the one this was made with."
	case p.ContextFingerprint != current.ContextFingerprint:
		return "The execution profile changed since this was made."
	case p.Workspace != current.Workspace:
		return "The launch workspace is now " + current.Workspace + ", not " + p.Workspace + "."
	case p.Engine != current.Engine:
		return "The project is now read with another engine."
	case p.AWSProfile != current.AWSProfile || p.AWSRegion != current.AWSRegion:
		return "The AWS profile or region of this context changed."
	}
	return ""
}

// CloudIdentity is a verified AWS caller identity for one pinned context. Only the account,
// a display form of the ARN and the principal type are kept; the user ID is dropped.
type CloudIdentity struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Account       string `json:"account"`
	ARN           string `json:"arn"`
	PrincipalType string `json:"principalType"`
	Partition     string `json:"partition"`
	Region        string `json:"region"`
	RegionSource  string `json:"regionSource"`
	CLIVersion    string `json:"cliVersion"`
	VerifiedAt    string `json:"verifiedAt"`
	ExpiresAt     string `json:"expiresAt"`
	Pin           Pin    `json:"context"`
	expires       time.Time
}

// DiscoveryScope is what one scan reads.
type DiscoveryScope struct {
	Regions     []string `json:"regions"`
	Types       []string `json:"types"`
	MaxPerType  int      `json:"maxPerType"`
	Concurrency int      `json:"concurrency"`
}

// Estimate bounds the AWS API requests of a scope: one STS call plus at least one list call
// per task, at most one per page.
type Estimate struct {
	Tasks    int `json:"tasks"`
	MinCalls int `json:"minCalls"`
	MaxCalls int `json:"maxCalls"`
}

// EstimateScope computes the request bounds of a validated scope.
func EstimateScope(s DiscoveryScope) Estimate {
	e := Estimate{MinCalls: 1, MaxCalls: 1}
	pages := int(math.Ceil(float64(s.MaxPerType) / float64(min(PageSize, s.MaxPerType))))
	for _, name := range s.Types {
		t, _ := TypeByName(name)
		n := len(s.Regions)
		if t.Global {
			n = 1
		}
		e.Tasks += n
		e.MinCalls += n
		e.MaxCalls += n * pages
	}
	return e
}

// Validate normalizes s and checks it against the limits.
func (s *DiscoveryScope) Validate() error {
	if s.MaxPerType == 0 {
		s.MaxPerType = DefaultPerType
	}
	if s.Concurrency == 0 {
		s.Concurrency = DefaultConcurrent
	}
	if len(s.Regions) == 0 || len(s.Regions) > MaxRegions || len(s.Types) == 0 || len(s.Types) > len(Types) || s.MaxPerType < 1 || s.MaxPerType > MaxPerTypeLimit || s.Concurrency < 1 || s.Concurrency > MaxConcurrency {
		return ErrInvalidScope
	}
	seen := map[string]bool{}
	for _, r := range s.Regions {
		if !KnownRegion(r) || seen[r] {
			return ErrInvalidScope
		}
		seen[r] = true
	}
	for _, t := range s.Types {
		if _, ok := TypeByName(t); !ok || seen[t] {
			return ErrInvalidScope
		}
		seen[t] = true
	}
	// Types run in catalog order.
	sort.SliceStable(s.Types, func(i, j int) bool { return typeOrder(s.Types[i]) < typeOrder(s.Types[j]) })
	return nil
}

func typeOrder(name string) int {
	for i, t := range Types {
		if t.Type == name {
			return i
		}
	}
	return len(Types)
}

// Task is one list operation of a scan: one type in one region (or once, for global types).
type Task struct {
	Type      string `json:"type"`
	Region    string `json:"region"`
	Status    string `json:"status"`
	Count     int    `json:"count"`
	Pages     int    `json:"pages"`
	Retries   int    `json:"retries"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"`
	Message   string `json:"message,omitempty"`
}

// Scan is the record of one discovery scan, kept in memory only.
type Scan struct {
	ID          string                   `json:"id"`
	Status      string                   `json:"status"`
	StartedAt   string                   `json:"startedAt"`
	CompletedAt string                   `json:"completedAt,omitempty"`
	Scope       DiscoveryScope           `json:"scope"`
	Estimate    Estimate                 `json:"estimate"`
	Calls       int                      `json:"calls"`
	Identity    CloudIdentity            `json:"identity"`
	Pin         Pin                      `json:"context"`
	Tasks       []Task                   `json:"tasks"`
	Candidates  []CloudResourceCandidate `json:"-"`
	Merged      int                      `json:"duplicatesMerged"`
	Error       string                   `json:"error,omitempty"`
	Message     string                   `json:"message,omitempty"`
	cancel      context.CancelFunc
	byID        map[string]int
}

// CandidateView is a candidate with its binding match against the current state.
type CandidateView struct {
	CloudResourceCandidate
	Match TerraformBindingMatch `json:"match"`
}

// ScanView is a scan as returned to a client. Results of a scan made for another context are
// withheld, so they cannot be reused there.
type ScanView struct {
	Scan
	Results     []CandidateView `json:"results"`
	Summary     map[string]int  `json:"summary"`
	Current     bool            `json:"current"`
	StaleReason string          `json:"staleReason,omitempty"`
	Withheld    bool            `json:"withheld"`
}

// Prefill is what the import wizard receives for one candidate after Prepare.
type Prefill struct {
	Token            string   `json:"token"`
	ScanID           string   `json:"scanId"`
	CandidateID      string   `json:"candidateId"`
	Type             string   `json:"type"`
	ImportID         string   `json:"importId"`
	SuggestedAddress string   `json:"suggestedAddress"`
	Region           string   `json:"region"`
	Account          string   `json:"account"`
	Match            string   `json:"match"`
	Warnings         []string `json:"warnings"`
	ExpiresAt        string   `json:"expiresAt"`
	pin              Pin
	expires          time.Time
}

// Service holds verifications, scans and prepared imports of this session in memory.
type Service struct {
	Runner Runner
	Now    func() time.Time
	// Sleep waits between retries of throttled calls; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
	NewID func() string
	// IdentityTTL bounds how long a verification can start scans; PrepareTTL how long a
	// prepared import can start an import; ScanTimeout bounds one scan.
	IdentityTTL time.Duration
	PrepareTTL  time.Duration
	ScanTimeout time.Duration
	MaxRunning  int

	mu         sync.Mutex
	identities []*CloudIdentity
	scans      []*Scan
	prepared   map[string]*Prefill
	running    int
	calls      chan struct{}
	base       context.Context
	stop       context.CancelFunc
	wg         sync.WaitGroup
	closed     bool
}

// New returns a service with runner.
func New(runner Runner) *Service {
	base, stop := context.WithCancel(context.Background())
	return &Service{Runner: runner, Now: time.Now, Sleep: sleep, NewID: newID, IdentityTTL: 30 * time.Minute, PrepareTTL: 15 * time.Minute, ScanTimeout: 15 * time.Minute, MaxRunning: MaxRunning, prepared: map[string]*Prefill{}, calls: make(chan struct{}, MaxCalls), base: base, stop: stop}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Close cancels running scans and waits for them.
func (s *Service) Close(timeout time.Duration) {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.stop()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// RegionOf returns the default region of a context: the profile's region, else AWS_REGION or
// AWS_DEFAULT_REGION of the environment, else us-east-1. source says which.
func RegionOf(pin Pin, environ []string) (region, source string) {
	if pin.AWSRegion != "" {
		return pin.AWSRegion, "profile"
	}
	if environ == nil {
		environ = os.Environ()
	}
	values := map[string]string{}
	for _, item := range environ {
		key, value, _ := strings.Cut(item, "=")
		values[strings.ToUpper(key)] = value
	}
	for _, key := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if regionPattern.MatchString(values[key]) {
			return values[key], "environment"
		}
	}
	return "us-east-1", "default"
}

var (
	identityARN = regexp.MustCompile(`^arn:(aws|aws-cn|aws-us-gov|aws-iso[a-z]?):(sts|iam)::([0-9]{12}):([A-Za-z0-9+=,.@/_-]{1,512})$`)
)

// parseIdentity extracts the account, partition, principal type and a display ARN. An
// assumed-role session name (often a user name or e-mail) is not shown.
func parseIdentity(out []byte) (account, arn, principal, partition string, err error) {
	var v struct {
		Account string `json:"Account"`
		Arn     string `json:"Arn"`
	}
	if json.Unmarshal(out, &v) != nil || !accountRE.MatchString(v.Account) {
		return "", "", "", "", errIdentityFormat
	}
	m := identityARN.FindStringSubmatch(v.Arn)
	if m == nil || m[3] != v.Account || state.Suspicious(v.Arn) {
		return v.Account, "ARN withheld", "unknown", "aws", nil
	}
	partition, resource := m[1], m[4]
	kind, rest, _ := strings.Cut(resource, "/")
	switch kind {
	case "assumed-role":
		role, _, _ := strings.Cut(rest, "/")
		return v.Account, "arn:" + partition + ":sts::" + v.Account + ":assumed-role/" + role + "/…", "assumed-role", partition, nil
	case "user", "federated-user", "role":
		return v.Account, v.Arn, kind, partition, nil
	case "root":
		return v.Account, v.Arn, "root", partition, nil
	}
	return v.Account, "ARN withheld", "unknown", partition, nil
}

// callerIdentity runs STS GetCallerIdentity in region.
func (s *Service) callerIdentity(ctx context.Context, environ []string, region string) (account, arn, principal, partition string, err error) {
	out, err := s.Runner.Run(ctx, environ, []string{"sts", "get-caller-identity", "--output", "json", "--region", region})
	if err != nil {
		return "", "", "", "", err
	}
	account, arn, principal, partition, err = parseIdentity(out)
	if err != nil {
		return "", "", "", "", &CallError{KindMalformed}
	}
	return account, arn, principal, partition, nil
}

// Verify checks the AWS CLI version and asks STS for the caller identity of pin's context.
// It is an explicit user action: STS is read-only but contacts AWS.
func (s *Service) Verify(ctx context.Context, pin Pin, environ []string) (CloudIdentity, error) {
	out, err := s.Runner.Run(ctx, environ, []string{"--version"})
	if err != nil {
		return CloudIdentity{}, err
	}
	version, supported := ParseVersion(out)
	if !supported {
		return CloudIdentity{}, &CallError{KindUnsupportedCLI}
	}
	region, source := RegionOf(pin, environ)
	account, arn, principal, partition, err := s.callerIdentity(ctx, environ, region)
	if err != nil {
		return CloudIdentity{}, err
	}
	now := s.Now()
	id := &CloudIdentity{ID: s.NewID(), Provider: "aws", Account: account, ARN: arn, PrincipalType: principal, Partition: partition, Region: region, RegionSource: source, CLIVersion: version, VerifiedAt: now.UTC().Format(time.RFC3339), Pin: pin, expires: now.Add(s.IdentityTTL)}
	id.ExpiresAt = id.expires.UTC().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.identities = append(s.identities, id)
	if len(s.identities) > maxIdentities {
		s.identities = s.identities[len(s.identities)-maxIdentities:]
	}
	return *id, nil
}

// Identity returns the latest unexpired verification for pin, if any.
func (s *Service) Identity(pin Pin) (CloudIdentity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	for i := len(s.identities) - 1; i >= 0; i-- {
		id := s.identities[i]
		if id.Pin.differs(pin) == "" && now.Before(id.expires) {
			return *id, true
		}
	}
	return CloudIdentity{}, false
}

// Start begins a scan for a verified identity of pin. It returns at once; the scan runs in
// the background.
func (s *Service) Start(pin Pin, environ []string, identityID string, scope DiscoveryScope) (Scan, error) {
	if err := scope.Validate(); err != nil {
		return Scan{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Scan{}, ErrClosed
	}
	var identity *CloudIdentity
	for _, id := range s.identities {
		if id.ID == identityID {
			identity = id
		}
	}
	if identity == nil || identity.Pin.differs(pin) != "" || !s.Now().Before(identity.expires) {
		return Scan{}, ErrIdentityStale
	}
	limit := s.MaxRunning
	if limit <= 0 {
		limit = MaxRunning
	}
	if s.running >= limit {
		return Scan{}, ErrTooManyScans
	}
	scan := &Scan{ID: s.NewID(), Status: ScanRunning, StartedAt: s.Now().UTC().Format(time.RFC3339), Scope: scope, Estimate: EstimateScope(scope), Identity: *identity, Pin: pin, Tasks: []Task{}, Candidates: []CloudResourceCandidate{}, byID: map[string]int{}}
	for _, name := range scope.Types {
		t, _ := TypeByName(name)
		if t.Global {
			scan.Tasks = append(scan.Tasks, Task{Type: name, Region: "global", Status: TaskPending})
			continue
		}
		for _, r := range scope.Regions {
			scan.Tasks = append(scan.Tasks, Task{Type: name, Region: r, Status: TaskPending})
		}
	}
	ctx, cancel := context.WithTimeout(s.base, s.scanTimeout())
	scan.cancel = cancel
	s.scans = append(s.scans, scan)
	if len(s.scans) > maxScans {
		// Drop the oldest finished scan.
		for i, old := range s.scans {
			if old.Status != ScanRunning {
				s.scans = append(s.scans[:i], s.scans[i+1:]...)
				break
			}
		}
	}
	s.running++
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		s.run(ctx, scan, environ)
	}()
	return s.copyScan(scan), nil
}

func (s *Service) scanTimeout() time.Duration {
	if s.ScanTimeout <= 0 {
		return 15 * time.Minute
	}
	return s.ScanTimeout
}

// copyScan returns a deep copy. Caller holds mu.
func (s *Service) copyScan(scan *Scan) Scan {
	out := *scan
	out.Tasks = slices.Clone(scan.Tasks)
	out.Candidates = slices.Clone(scan.Candidates)
	out.Scope.Regions = slices.Clone(scan.Scope.Regions)
	out.Scope.Types = slices.Clone(scan.Scope.Types)
	out.cancel, out.byID = nil, nil
	return out
}

// fatal error kinds stop the whole scan: every other read would fail the same way.
var fatal = map[string]bool{KindExpired: true, KindNoCredentials: true, KindInvalid: true, KindProfileNotFound: true, KindMissingCLI: true, KindIdentityChanged: true}

func (s *Service) run(ctx context.Context, scan *Scan, environ []string) {
	finish := func(status, kind string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.running--
		scan.Status, scan.CompletedAt = status, s.Now().UTC().Format(time.RFC3339)
		if kind != "" {
			scan.Error, scan.Message = kind, Message(kind)
		}
		for i := range scan.Tasks {
			if t := &scan.Tasks[i]; t.Status == TaskPending || t.Status == TaskRunning {
				t.Status, t.Error, t.Message = TaskCancelled, KindCancelled, Message(KindCancelled)
			}
		}
		sortCandidates(scan.Candidates)
		for i, c := range scan.Candidates {
			scan.byID[c.ID] = i
		}
	}
	// The account is checked again: credentials can change between verification and scan.
	// It counts against the session-wide call limit like every other read of a scan.
	select {
	case s.calls <- struct{}{}:
	case <-ctx.Done():
		finish(ScanCancelled, KindCancelled)
		return
	}
	s.countCall(scan)
	account, _, _, _, err := s.callerIdentity(ctx, environ, scan.Identity.Region)
	<-s.calls
	switch {
	case ctx.Err() != nil:
		finish(ScanCancelled, KindCancelled)
		return
	case err != nil:
		finish(ScanFailed, KindOf(err))
		return
	case account != scan.Identity.Account:
		finish(ScanFailed, KindIdentityChanged)
		return
	}
	scanCtx, abort := context.WithCancel(ctx)
	defer abort()
	var fatalKind string
	var fatalOnce sync.Once
	sem := make(chan struct{}, scan.Scope.Concurrency)
	var wg sync.WaitGroup
	selected := map[string]bool{}
	for _, r := range scan.Scope.Regions {
		selected[r] = true
	}
	for i := range scan.Tasks {
		select {
		case sem <- struct{}{}:
		case <-scanCtx.Done():
		}
		if scanCtx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			kind := s.runTask(scanCtx, scan, i, environ, selected)
			if fatal[kind] {
				fatalOnce.Do(func() { fatalKind = kind; abort() })
			}
		}(i)
	}
	wg.Wait()
	switch {
	case fatalKind != "":
		finish(ScanFailed, fatalKind)
	case ctx.Err() != nil:
		kind := KindCancelled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			kind = KindTimeout
		}
		finish(ScanCancelled, kind)
	default:
		s.mu.Lock()
		failed, partial := 0, false
		for _, t := range scan.Tasks {
			if t.Status == TaskFailed {
				failed++
			}
			if t.Truncated {
				partial = true
			}
		}
		s.mu.Unlock()
		switch {
		case failed == len(scan.Tasks):
			finish(ScanFailed, "")
		case failed > 0 || partial:
			finish(ScanPartial, "")
		default:
			finish(ScanCompleted, "")
		}
	}
}

func (s *Service) countCall(scan *Scan) {
	s.mu.Lock()
	scan.Calls++
	s.mu.Unlock()
}

// runTask lists one type in one region page by page and returns the error kind, if any.
func (s *Service) runTask(ctx context.Context, scan *Scan, i int, environ []string, selected map[string]bool) string {
	s.mu.Lock()
	task := &scan.Tasks[i]
	task.Status = TaskRunning
	typ, _ := TypeByName(task.Type)
	region := task.Region
	limit := scan.Scope.MaxPerType
	pctx := parseContext{Region: region, Account: scan.Identity.Account, Partition: scan.Identity.Partition, Observed: s.Now().UTC().Format(time.RFC3339), Selected: selected}
	s.mu.Unlock()
	callRegion := region
	if typ.Global {
		callRegion = scan.Identity.Region
		pctx.Region = ""
	}
	fail := func(kind string) string {
		s.mu.Lock()
		defer s.mu.Unlock()
		status := TaskFailed
		if kind == KindCancelled || kind == KindTimeout {
			status = TaskCancelled
		}
		task.Status, task.Error, task.Message = status, kind, Message(kind)
		return kind
	}
	token, count := "", 0
	for {
		pageSize := min(PageSize, limit-count)
		args := append(slices.Clone(typ.args), "--output", "json", "--region", callRegion, "--max-items", itoa(pageSize))
		if token != "" {
			args = append(args, "--starting-token", token)
		}
		out, err := s.call(ctx, scan, task, environ, args)
		if err != nil {
			return fail(KindOf(err))
		}
		found, next, err := typ.parse(out, pctx)
		if err != nil {
			return fail(KindOf(err))
		}
		s.mu.Lock()
		task.Pages++
		for _, c := range found {
			if count >= limit {
				break
			}
			count++
			s.add(scan, c)
		}
		task.Count = count
		// The limit was reached with more pages left: objects may be missing.
		task.Truncated = count >= limit && next != ""
		s.mu.Unlock()
		if next == "" || count >= limit {
			break
		}
		if ctx.Err() != nil {
			return fail(KindCancelled)
		}
		token = next
	}
	s.mu.Lock()
	task.Status = TaskDone
	s.mu.Unlock()
	return ""
}

// call runs one list call with the shared call limit and retries throttled calls with
// exponential backoff.
func (s *Service) call(ctx context.Context, scan *Scan, task *Task, environ []string, args []string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		select {
		case s.calls <- struct{}{}:
		case <-ctx.Done():
			return nil, &CallError{KindCancelled}
		}
		s.countCall(scan)
		out, err := s.Runner.Run(ctx, environ, args)
		<-s.calls
		if err == nil {
			return out, nil
		}
		if ctx.Err() != nil {
			return nil, &CallError{KindCancelled}
		}
		if KindOf(err) != KindThrottled || attempt+1 >= maxAttempts {
			return nil, err
		}
		s.mu.Lock()
		task.Retries++
		s.mu.Unlock()
		backoff := time.Duration(500*(1<<attempt)) * time.Millisecond
		if s.Sleep(ctx, backoff) != nil {
			return nil, &CallError{KindCancelled}
		}
	}
}

// add merges a candidate into the scan. Caller holds mu. The same object reported by more
// than one region (or listed twice) becomes one candidate.
func (s *Service) add(scan *Scan, c CloudResourceCandidate) {
	if i, ok := scan.byID[c.ID]; ok {
		existing := &scan.Candidates[i]
		for _, r := range c.Regions {
			if !slices.Contains(existing.Regions, r) {
				existing.Regions = append(existing.Regions, r)
			}
		}
		scan.Merged++
		return
	}
	if len(scan.Candidates) >= maxCandidates {
		return
	}
	scan.byID[c.ID] = len(scan.Candidates)
	scan.Candidates = append(scan.Candidates, c)
}

func sortCandidates(cs []CloudResourceCandidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if typeOrder(a.Type) != typeOrder(b.Type) {
			return typeOrder(a.Type) < typeOrder(b.Type)
		}
		if a.Region != b.Region {
			return a.Region < b.Region
		}
		return a.ResourceID < b.ResourceID
	})
}

func itoa(n int) string {
	if n <= 0 {
		return "1"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Scans lists the scans of a context ("" for the launch project), newest first, without
// results.
func (s *Service) Scans(context string) []Scan {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Scan, 0, len(s.scans))
	for i := len(s.scans) - 1; i >= 0; i-- {
		if s.scans[i].Pin.Context != context {
			continue
		}
		c := s.copyScan(s.scans[i])
		c.Candidates = nil
		out = append(out, c)
	}
	return out
}

// find returns scan id of context; a scan of another context is not found.
func (s *Service) find(id, context string) *Scan {
	for _, scan := range s.scans {
		if scan.ID == id && scan.Pin.Context == context {
			return scan
		}
	}
	return nil
}

// View returns scan id with candidates labelled against snap, for a client in context pin.
func (s *Service) View(id string, pin Pin, snap workspace.Snapshot) (ScanView, error) {
	s.mu.Lock()
	scan := s.find(id, pin.Context)
	if scan == nil {
		s.mu.Unlock()
		return ScanView{}, ErrNotFound
	}
	snapshot := s.copyScan(scan)
	s.mu.Unlock()
	view := ScanView{Scan: snapshot, Results: []CandidateView{}, Summary: map[string]int{MatchTracked: 0, MatchPossiblyUntracked: 0, MatchUnresolved: 0, MatchUnknown: 0}}
	if reason := snapshot.Pin.differs(pin); reason != "" {
		view.StaleReason, view.Withheld = reason+" Results are withheld; scan again in this context.", true
		return view, nil
	}
	view.Current = true
	cands := snapshot.Candidates
	if snapshot.Status == ScanRunning {
		cands = slices.Clone(cands)
		sortCandidates(cands)
	}
	matches := Match(cands, snap)
	for i, c := range cands {
		view.Results = append(view.Results, CandidateView{c, matches[i]})
		view.Summary[matches[i].Status]++
	}
	return view, nil
}

// Cancel stops a running scan of context.
func (s *Service) Cancel(id, context string) (Scan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scan := s.find(id, context)
	if scan == nil {
		return Scan{}, ErrNotFound
	}
	if scan.Status == ScanRunning && scan.cancel != nil {
		scan.cancel()
	}
	return s.copyScan(scan), nil
}

var addressWord = regexp.MustCompile(`[^a-z0-9_]+`)

// suggestAddress proposes a root-module address from the object's display name.
func suggestAddress(c CloudResourceCandidate) string {
	name := ""
	if c.Name != state.Redacted {
		name = strings.Trim(addressWord.ReplaceAllString(strings.ToLower(c.Name), "_"), "_")
	}
	if name == "" || !(name[0] >= 'a' && name[0] <= 'z') {
		name = "imported_" + strings.Trim(addressWord.ReplaceAllString(strings.ToLower(c.ResourceID), "_"), "_")
	}
	if len(name) > 48 {
		name = strings.TrimRight(name[:48], "_")
	}
	address := c.Type + "." + name
	if _, err := imports.ParseAddress(address); err != nil || state.Suspicious(address) {
		return c.Type + ".imported"
	}
	return address
}

// Prepare checks that a candidate of a current scan can be imported in pin's context, asks
// STS again that the credentials still belong to the scanned account, and returns a prefill
// for the import wizard. Nothing is written; the wizard still requires review, a new file
// and a reviewed plan.
func (s *Service) Prepare(ctx context.Context, scanID, candidateID string, pin Pin, environ []string, snap workspace.Snapshot, acknowledgeUnknown bool) (Prefill, error) {
	s.mu.Lock()
	scan := s.find(scanID, pin.Context)
	if scan == nil {
		s.mu.Unlock()
		return Prefill{}, ErrNotFound
	}
	if reason := scan.Pin.differs(pin); reason != "" {
		s.mu.Unlock()
		return Prefill{}, ErrScanStale
	}
	if scan.Status == ScanRunning || scan.Status == ScanFailed {
		s.mu.Unlock()
		return Prefill{}, ErrScanNotReady
	}
	i, ok := scan.byID[candidateID]
	if !ok {
		s.mu.Unlock()
		return Prefill{}, ErrNotFound
	}
	c := scan.Candidates[i]
	identity := scan.Identity
	s.mu.Unlock()
	match := Match([]CloudResourceCandidate{c}, snap)[0]
	switch {
	case match.Status == MatchTracked, match.Status == MatchUnresolved:
		return Prefill{}, ErrNotImportable
	case match.Status == MatchUnknown && !acknowledgeUnknown:
		return Prefill{}, ErrAcknowledge
	}
	account, _, _, _, err := s.callerIdentity(ctx, environ, identity.Region)
	if err != nil {
		return Prefill{}, err
	}
	if account != identity.Account || c.Account != identity.Account {
		return Prefill{}, &CallError{KindIdentityChanged}
	}
	now := s.Now()
	p := &Prefill{Token: s.NewID(), ScanID: scanID, CandidateID: candidateID, Type: c.Type, ImportID: c.ImportID, SuggestedAddress: suggestAddress(c), Region: c.Region, Account: c.Account, Match: match.Status, Warnings: []string{}, pin: pin, expires: now.Add(s.PrepareTTL)}
	p.ExpiresAt = p.expires.UTC().Format(time.RFC3339)
	region, _ := RegionOf(pin, environ)
	switch {
	case c.Region == "":
		p.Warnings = append(p.Warnings, "The bucket's region was not reported. Make sure the provider configuration you import with targets the bucket's region.")
	case c.Region != region:
		p.Warnings = append(p.Warnings, "This object is in "+c.Region+", but this context's default region is "+region+". If the default provider configuration targets "+region+", set Provider to an aliased configuration for "+c.Region+", or the plan will not find the object.")
	}
	if match.Status == MatchUnknown {
		p.Warnings = append(p.Warnings, "The state mapping of this object is unknown: "+match.Reason+" Review the plan carefully; it fails if the object is already managed at another address.")
	}
	p.Warnings = append(p.Warnings, "Choose the address and write or generate the resource block yourself. The plan, not discovery, shows what the import would change.")
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, old := range s.prepared {
		if !now.Before(old.expires) {
			delete(s.prepared, token)
		}
	}
	s.prepared[p.Token] = p
	return *p, nil
}

// CheckImport verifies that an import request started from a prepared candidate still
// matches it: same context, same import ID, same resource type, current scan, unexpired.
func (s *Service) CheckImport(token string, pin Pin, req imports.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.prepared[token]
	if !ok || !s.Now().Before(p.expires) || p.pin.differs(pin) != "" || req.ID != p.ImportID {
		return ErrPreparedStale
	}
	scan := s.find(p.ScanID, pin.Context)
	if scan == nil || scan.Pin.differs(pin) != "" {
		return ErrPreparedStale
	}
	address, err := imports.ParseAddress(strings.TrimSpace(req.Address))
	if err != nil || address.Type != p.Type {
		return ErrPreparedStale
	}
	return nil
}

// Consume removes a prepared import after it started an import plan.
func (s *Service) Consume(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.prepared, token)
}

// Drop cancels the running scans of a Hub context and forgets its verifications, scans and
// prepared imports. It is called when the context's execution scope closes.
func (s *Service) Drop(context string) {
	if context == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scans = slices.DeleteFunc(s.scans, func(scan *Scan) bool {
		if scan.Pin.Context != context {
			return false
		}
		if scan.cancel != nil {
			scan.cancel()
		}
		return true
	})
	s.identities = slices.DeleteFunc(s.identities, func(id *CloudIdentity) bool { return id.Pin.Context == context })
	for token, p := range s.prepared {
		if p.pin.Context == context {
			delete(s.prepared, token)
		}
	}
}
