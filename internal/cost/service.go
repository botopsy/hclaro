// Package cost estimates the recurring monthly cost impact of a reviewed plan through an
// optional cost provider. The only provider is the Infracost CLI, used when the user
// installed it themselves. Estimation is off by default: nothing is run, read or sent until
// the user has seen the data disclosure and opted in for the session, and every estimate is
// an explicit request for one reviewed plan. The provider receives a minimized copy of the
// plan (see Minimize) in a private temporary file and no cloud credentials. In strict
// local-only mode the feature is unavailable, because no offline price source exists.
package cost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DisclosureVersion changes whenever the disclosure text changes; an opt-in names the
// version the user saw.
const DisclosureVersion = "1"

// ProviderInfracost is the only provider.
const ProviderInfracost = "infracost"

const (
	maxEstimates = 20
	// staleAfter bounds the age of leftover input directories removed at startup.
	staleAfter = time.Hour
)

var (
	ErrNotFound = errors.New("estimate not found")
	ErrClosed   = errors.New("cost service closed")
)

// ExternalDataDisclosure states what the provider reads, what may leave the device and how
// it authenticates. It is shown before opt-in.
type ExternalDataDisclosure struct {
	Provider       string   `json:"provider"`
	ProviderName   string   `json:"providerName"`
	Version        string   `json:"version"`
	ExternalCalls  bool     `json:"externalCalls"`
	Offline        bool     `json:"offline"`
	Endpoint       string   `json:"endpoint"`
	Reads          []string `json:"reads"`
	SentOffDevice  []string `json:"sentOffDevice"`
	NeverSent      []string `json:"neverSent"`
	Authentication string   `json:"authentication"`
	Storage        string   `json:"storage"`
	Documentation  string   `json:"documentation"`
}

// CostProviderCapabilities reports whether estimates can be requested. Computing it never
// starts a process: the executable is only looked up on PATH.
type CostProviderCapabilities struct {
	Provider    string                 `json:"provider"`
	State       string                 `json:"state"`
	Installed   bool                   `json:"installed"`
	Enabled     bool                   `json:"enabled"`
	CanEstimate bool                   `json:"canEstimate"`
	Reason      string                 `json:"reason,omitempty"`
	CLIVersion  string                 `json:"cliVersion,omitempty"`
	OptedInAt   string                 `json:"optedInAt,omitempty"`
	StrictLocal bool                   `json:"strictLocal"`
	Offline     bool                   `json:"offlineAvailable"`
	Disclosure  ExternalDataDisclosure `json:"disclosure"`
}

// Capability states.
const (
	StateStrictLocal = "strict_local"
	StateMissing     = "cli_missing"
	StateOff         = "off"
	StateReady       = "ready"
)

// Binding ties an estimate to one reviewed plan and its context.
type Binding struct {
	PlanID         string `json:"planId"`
	ArtifactSHA256 string `json:"artifactSha256"`
	Fingerprint    string `json:"fingerprint"`
	Engine         string `json:"engine"`
	Workspace      string `json:"workspace"`
	ProfileID      string `json:"profileId,omitempty"`
}

// Input is one estimate request, assembled by the server from a stored plan. The browser
// only names the plan.
type Input struct {
	Binding
	Document    []byte
	Changes     []PlanChange
	ProjectKey  string
	ProjectName string
}

// Problem is a sanitized estimate failure.
type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CostEstimate is one estimate. Breakdown is set once it completed.
type CostEstimate struct {
	ID              string     `json:"id"`
	PlanID          string     `json:"planId"`
	Status          string     `json:"status"`
	Error           *Problem   `json:"error,omitempty"`
	StartedAt       string     `json:"startedAt"`
	FinishedAt      string     `json:"finishedAt,omitempty"`
	Provider        string     `json:"provider"`
	ProviderVersion string     `json:"providerVersion,omitempty"`
	PricingSource   string     `json:"pricingSource"`
	Basis           string     `json:"basis"`
	Workspace       string     `json:"workspace"`
	Engine          string     `json:"engine"`
	Fingerprint     string     `json:"fingerprint"`
	ArtifactDigest  string     `json:"artifactDigest"`
	CancelRequested bool       `json:"cancelRequested"`
	Stale           bool       `json:"stale"`
	StaleReason     string     `json:"staleReason,omitempty"`
	Breakdown       *Breakdown `json:"breakdown,omitempty"`
}

// Estimate statuses.
const (
	Running   = "running"
	Completed = "completed"
	Failed    = "failed"
	Cancelled = "cancelled"
)

type record struct {
	est     CostEstimate
	binding Binding
	cancel  context.CancelFunc
}

type optIn struct {
	at      time.Time
	version string
}

// Service runs estimates for one Hclaro session.
type Service struct {
	runner Runner
	strict bool
	// Store keeps sanitized aggregates on disk; nil keeps nothing.
	Store   *AggregateStore
	TempDir string
	Timeout time.Duration
	Now     func() time.Time
	NewID   func() string
	// Environ returns the environment the adapter is derived from (tests); nil uses Hclaro's.
	Environ func() []string

	mu        sync.Mutex
	opted     *optIn
	estimates []*record
	running   *record
	base      context.Context
	stop      context.CancelFunc
	wg        sync.WaitGroup
	closed    bool
}

// New returns a service with estimation off. strictLocal makes it permanently unavailable.
func New(runner Runner, strictLocal bool) *Service {
	base, stop := context.WithCancel(context.Background())
	return &Service{runner: runner, strict: strictLocal, Timeout: callTimeout, Now: time.Now, NewID: newID, base: base, stop: stop}
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (s *Service) environ() []string {
	if s.Environ != nil {
		return s.Environ()
	}
	return os.Environ()
}

// Disclosure describes what the Infracost adapter reads, sends and stores.
func (s *Service) Disclosure() ExternalDataDisclosure {
	storage := "Nothing is saved: estimates are kept in memory for this session only."
	if s.Store != nil {
		storage = "Small aggregate totals (amounts, currency, coverage, time, workspace and plan ID; no addresses or line items) are saved locally in cost-estimates.json in Hclaro's configuration folder. You can clear them."
	}
	return ExternalDataDisclosure{
		Provider: ProviderInfracost, ProviderName: "Infracost CLI v0.10", Version: DisclosureVersion,
		ExternalCalls: true, Offline: false, Endpoint: Endpoint(s.environ()),
		Reads: []string{
			"A minimized copy of the reviewed plan, written to a private temporary file (owner-only) for the duration of the estimate and deleted afterwards.",
			"Infracost's own configuration: its API key and, if you set one, a custom pricing endpoint and currency.",
		},
		SentOffDevice: []string{
			"Price lookups to the pricing API: cloud provider, region, resource type and the attributes that select a price (for example instance type, storage size or database engine).",
			"Infracost can add its own run metadata (CLI version, operating system, resource counts) to these requests. Hclaro switches off update checks, Infracost Cloud uploads and the dashboard, but cannot inspect what the CLI sends.",
		},
		NeverSent: []string{
			"State, state outputs, plan outputs and variable values.",
			"Values marked sensitive, attributes with secret-like names, and credential-shaped or random-looking strings: they are removed before the file is written.",
			"Tags, descriptions, policies, user data, scripts and other free text; module sources and provider arguments other than the region.",
			"Cloud credentials: the CLI gets no AWS, Azure, Google or Terraform Cloud variables, and no execution profile environment.",
			"Project paths and file names.",
			"AWS account IDs in attribute values (ARNs, URLs, names): every 12-digit number there is replaced with 000000000000. Resource addresses are kept as they are, so a for_each key that is an account ID stays in the local input file.",
		},
		Authentication: "Infracost's own API key, which the Infracost CLI reads from INFRACOST_API_KEY or from its credentials file (`infracost auth login`). Hclaro never asks for, shows or stores it.",
		Storage:        storage,
		Documentation:  "https://www.infracost.io/docs/faq/",
	}
}

// Capabilities reports the provider state. It never starts a process.
func (s *Service) Capabilities() CostProviderCapabilities {
	c := CostProviderCapabilities{Provider: ProviderInfracost, StrictLocal: s.strict, Disclosure: s.Disclosure()}
	if s.strict {
		c.State, c.Reason = StateStrictLocal, Message(KindStrictLocal)
		return c
	}
	c.Installed = s.runner.Find()
	s.mu.Lock()
	opted := s.opted
	s.mu.Unlock()
	switch {
	case !c.Installed:
		c.State, c.Reason = StateMissing, Message(KindMissingCLI)
	case opted == nil:
		c.State, c.Reason = StateOff, Message(KindOptIn)
	default:
		c.State, c.Enabled, c.CanEstimate = StateReady, true, true
		c.CLIVersion, c.OptedInAt = opted.version, opted.at.UTC().Format(time.RFC3339)
	}
	return c
}

// CanEstimate checks that the provider is usable now: not in strict mode, opted in, the
// CLI installed and a supported version. It runs `infracost --version` only after opt-in.
func (s *Service) CanEstimate(ctx context.Context) (string, error) {
	if s.strict {
		return "", fail(KindStrictLocal)
	}
	s.mu.Lock()
	opted := s.opted != nil
	s.mu.Unlock()
	if !opted {
		return "", fail(KindOptIn)
	}
	return s.version(ctx)
}

func (s *Service) version(ctx context.Context) (string, error) {
	if !s.runner.Find() {
		return "", fail(KindMissingCLI)
	}
	out, err := s.runner.Run(ctx, s.environ(), []string{"--version"})
	if err != nil {
		return "", err
	}
	return ParseVersion(out)
}

// OptIn turns estimation on for this session after the user acknowledged the disclosure
// version they saw. It checks the installed CLI version (the first process the feature
// starts) and refuses unsupported versions.
func (s *Service) OptIn(ctx context.Context, disclosureVersion string) (CostProviderCapabilities, error) {
	if s.strict {
		return CostProviderCapabilities{}, fail(KindStrictLocal)
	}
	if disclosureVersion != DisclosureVersion {
		return CostProviderCapabilities{}, fail(KindOptIn)
	}
	v, err := s.version(ctx)
	if err != nil {
		return CostProviderCapabilities{}, err
	}
	s.mu.Lock()
	s.opted = &optIn{at: s.Now(), version: v}
	s.mu.Unlock()
	return s.Capabilities(), nil
}

// OptOut turns estimation off and cancels a running estimate.
func (s *Service) OptOut() CostProviderCapabilities {
	s.mu.Lock()
	s.opted = nil
	if s.running != nil {
		s.running.est.CancelRequested = true
		s.running.cancel()
	}
	s.mu.Unlock()
	return s.Capabilities()
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// Start begins an estimate of in. It refuses when estimation is off, another estimate runs
// or the plan has no cost input. It does not start a process itself; the run does.
func (s *Service) Start(in Input) (CostEstimate, error) {
	if s.strict {
		return CostEstimate{}, fail(KindStrictLocal)
	}
	if len(in.Document) == 0 {
		return CostEstimate{}, fail(KindNoInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return CostEstimate{}, fail(KindShutdown)
	case s.opted == nil:
		return CostEstimate{}, fail(KindOptIn)
	case s.running != nil:
		return CostEstimate{}, fail(KindBusy)
	}
	ctx, cancel := context.WithTimeout(s.base, s.Timeout)
	r := &record{binding: in.Binding, cancel: cancel, est: CostEstimate{
		ID: s.NewID(), PlanID: in.PlanID, Status: Running, StartedAt: s.Now().UTC().Format(time.RFC3339),
		Provider: ProviderInfracost, PricingSource: Endpoint(s.environ()), Basis: "monthly, 730 hours",
		Workspace: in.Workspace, Engine: in.Engine, Fingerprint: short(in.Fingerprint), ArtifactDigest: short(in.ArtifactSHA256),
	}}
	s.running = r
	s.estimates = append(s.estimates, r)
	if len(s.estimates) > maxEstimates {
		s.estimates = s.estimates[len(s.estimates)-maxEstimates:]
	}
	s.wg.Add(1)
	go s.run(ctx, r, in)
	return r.est, nil
}

func (s *Service) run(ctx context.Context, r *record, in Input) {
	defer s.wg.Done()
	defer r.cancel()
	breakdown, version, err := s.estimate(ctx, in)
	s.mu.Lock()
	if s.running == r {
		s.running = nil
	}
	r.est.FinishedAt = s.Now().UTC().Format(time.RFC3339)
	r.est.ProviderVersion = version
	kind := ""
	switch {
	case err == nil:
		r.est.Status, r.est.Breakdown = Completed, &breakdown
	case r.est.CancelRequested || s.closed:
		r.est.Status, kind = Cancelled, KindCancelled
	default:
		r.est.Status, kind = Failed, KindOf(err)
	}
	if kind != "" {
		r.est.Error = &Problem{Code: kind, Message: Message(kind)}
	}
	est := r.est
	s.mu.Unlock()
	if est.Status == Completed && s.Store != nil {
		_ = s.Store.Add(aggregateOf(est, in))
	}
}

// estimate checks the CLI version again, writes the private input, runs the breakdown and
// aggregates it. The input directory is removed on every path.
func (s *Service) estimate(ctx context.Context, in Input) (Breakdown, string, error) {
	version, err := s.version(ctx)
	if err != nil {
		return Breakdown{}, "", err
	}
	dir, err := os.MkdirTemp(s.TempDir, "hclaro-cost-")
	if err != nil {
		return Breakdown{}, version, fail(KindFailed)
	}
	defer os.RemoveAll(dir)
	_ = os.Chmod(dir, 0o700)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	path := filepath.Join(dir, inputName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Breakdown{}, version, fail(KindFailed)
	}
	_, werr := f.Write(in.Document)
	if cerr := f.Close(); werr != nil || cerr != nil {
		return Breakdown{}, version, fail(KindFailed)
	}
	out, err := s.runner.Run(ctx, s.environ(), estimateArgs(path))
	if err != nil {
		return Breakdown{}, version, err
	}
	b, err := Aggregate(out, in.Changes)
	return b, version, err
}

// Cancel requests cancellation of a running estimate.
func (s *Service) Cancel(id string) (CostEstimate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.estimates {
		if r.est.ID == id {
			if r.est.Status == Running {
				r.est.CancelRequested = true
				r.cancel()
			}
			return r.est, nil
		}
	}
	return CostEstimate{}, ErrNotFound
}

// Get returns an estimate and the binding it was made for.
func (s *Service) Get(id string) (CostEstimate, Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.estimates {
		if r.est.ID == id {
			return r.est, r.binding, nil
		}
	}
	return CostEstimate{}, Binding{}, ErrNotFound
}

// Latest returns the most recent estimate of a plan.
func (s *Service) Latest(planID string) (CostEstimate, Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.estimates) - 1; i >= 0; i-- {
		if r := s.estimates[i]; r.est.PlanID == planID {
			return r.est, r.binding, nil
		}
	}
	return CostEstimate{}, Binding{}, ErrNotFound
}

// Close cancels a running estimate and waits (bounded) for its cleanup.
func (s *Service) Close(timeout time.Duration) {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.stop()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// SweepStale removes hclaro-cost-* input directories older than an hour from dir (the OS
// temp directory when empty). Only a killed process leaves them; symlinks are not followed.
func SweepStale(dir string, now time.Time) int {
	if dir == "" {
		dir = os.TempDir()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "hclaro-cost-") || !inputDir.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 || now.Sub(info.ModTime()) < staleAfter {
			continue
		}
		if os.RemoveAll(filepath.Join(dir, e.Name())) == nil {
			n++
		}
	}
	return n
}
