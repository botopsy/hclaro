package cost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const (
	storeName    = "cost-estimates.json"
	storeVersion = 1
	maxRecords   = 50
	maxStoreFile = 1 << 20
)

// AggregateRecord is the only estimate data kept on disk: totals, currency, coverage counts
// and identifiers. It holds no resource addresses, line items, plan content or paths.
type AggregateRecord struct {
	ID               string `json:"id"`
	ProjectKey       string `json:"projectKey"`
	ProjectName      string `json:"projectName"`
	Workspace        string `json:"workspace"`
	Engine           string `json:"engine"`
	PlanID           string `json:"planId"`
	Fingerprint      string `json:"fingerprint"`
	CreatedAt        string `json:"createdAt"`
	Provider         string `json:"provider"`
	ProviderVersion  string `json:"providerVersion"`
	PricingSource    string `json:"pricingSource"`
	Currency         string `json:"currency"`
	MonthlyDelta     string `json:"monthlyDelta"`
	PastMonthly      string `json:"pastMonthly"`
	ProjectedMonthly string `json:"projectedMonthly"`
	CoveragePercent  *int   `json:"coveragePercent"`
	Changed          int    `json:"changed"`
	Estimated        int    `json:"estimated"`
	NotEstimated     int    `json:"notEstimated"`
}

type storeDoc struct {
	Version int               `json:"version"`
	Records []AggregateRecord `json:"records"`
}

// AggregateStore keeps the last estimate aggregates in the configuration folder.
type AggregateStore struct {
	mu   sync.Mutex
	path string
}

// OpenStore returns the store in dir.
func OpenStore(dir string) *AggregateStore {
	return &AggregateStore{path: filepath.Join(dir, storeName)}
}

// ProjectKey derives a stable key from a project root without keeping the path.
func ProjectKey(root string) string {
	h := sha256.Sum256([]byte("hclaro-cost\x00" + root))
	return hex.EncodeToString(h[:8])
}

func aggregateOf(e CostEstimate, in Input) AggregateRecord {
	b := e.Breakdown
	return AggregateRecord{
		ID: e.ID, ProjectKey: in.ProjectKey, ProjectName: in.ProjectName, Workspace: e.Workspace, Engine: e.Engine,
		PlanID: e.PlanID, Fingerprint: e.Fingerprint, CreatedAt: e.FinishedAt, Provider: e.Provider,
		ProviderVersion: e.ProviderVersion, PricingSource: e.PricingSource, Currency: b.Currency,
		MonthlyDelta: b.MonthlyDelta, PastMonthly: b.PastMonthly, ProjectedMonthly: b.ProjectedMonthly,
		CoveragePercent: b.Coverage.Percent, Changed: b.Coverage.Changed,
		Estimated: b.Coverage.Estimated + b.Coverage.Partial, NotEstimated: b.Coverage.Unsupported + b.Coverage.NotEstimated + b.Coverage.UsageUnknown,
	}
}

// read returns the records on disk. A missing, oversized or unreadable file is empty; it is
// replaced on the next write.
func (s *AggregateStore) read() []AggregateRecord {
	f, err := os.Open(s.path)
	if err != nil {
		return []AggregateRecord{}
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() || info.Size() > maxStoreFile {
		return []AggregateRecord{}
	}
	var doc storeDoc
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if dec.Decode(&doc) != nil || doc.Version != storeVersion || doc.Records == nil {
		return []AggregateRecord{}
	}
	return doc.Records
}

func (s *AggregateStore) write(records []AggregateRecord) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(storeDoc{Version: storeVersion, Records: records}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".cost-estimates-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(b)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(name)
		return err
	}
	_ = os.Chmod(name, 0o600)
	if err := os.Rename(name, s.path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// Add appends a record, keeping the last 50.
func (s *AggregateStore) Add(r AggregateRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := append(s.read(), r)
	if len(records) > maxRecords {
		records = records[len(records)-maxRecords:]
	}
	return s.write(records)
}

// List returns the records of one project, newest first.
func (s *AggregateStore) List(projectKey string) []AggregateRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []AggregateRecord{}
	records := s.read()
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].ProjectKey == projectKey {
			out = append(out, records[i])
		}
	}
	return out
}

// Clear removes the records of one project and returns how many were removed.
func (s *AggregateStore) Clear(projectKey string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.read()
	kept := records[:0]
	for _, r := range records {
		if r.ProjectKey != projectKey {
			kept = append(kept, r)
		}
	}
	removed := len(records) - len(kept)
	if removed == 0 {
		return 0, nil
	}
	return removed, s.write(kept)
}

// Path is the file the store writes.
func (s *AggregateStore) Path() string { return s.path }
