package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"hclaro/internal/operations"
	"hclaro/internal/plan"
)

const canary = "DO_NOT_LEAK_HCLARO_TEST_SECRET"

var t0 = time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

func id(n int) string { return fmt.Sprintf("%032x", n) }

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s := Open(dir, t0)
	s.Now = func() time.Time { return t0 }
	s.Sync = false
	return s
}

// finished records a synthetic finished operation.
func finished(s *Store, n int, kind, status string, started time.Time, mutate ...func(*operations.Record)) {
	op := operations.Operation{ID: id(n), Kind: kind, Status: status, StartedAt: started.UTC().Format(time.RFC3339), CompletedAt: started.Add(time.Second).UTC().Format(time.RFC3339), DurationMs: 1000, Command: []string{"terraform", kind}}
	r := operations.Record{Operation: op, Root: "/synthetic/network", Engine: "terraform", EngineVersion: "1.9.8", Workspace: "dev", Final: true}
	for _, m := range mutate {
		if m != nil {
			m(&r)
		}
	}
	s.Record(r)
}

func readFile(t *testing.T, s *Store) string {
	t.Helper()
	b, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStoreCreatesPrivateAtomicFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	s := open(t, dir)
	if status, notice, _ := s.Status(); status != Ready || notice != "" {
		t.Fatal(status, notice)
	}
	if _, err := os.Stat(s.path); !os.IsNotExist(err) {
		t.Fatal("opening must not create the file")
	}
	finished(s, 1, "plan", operations.Succeeded, t0)
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(s.path)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %v %v", fi, err)
		}
		di, _ := os.Stat(dir)
		if di.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v", di.Mode())
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".history-*.tmp"))
	if len(leftovers) != 0 {
		t.Fatal(leftovers)
	}
	var doc document
	if err := json.Unmarshal([]byte(readFile(t, s)), &doc); err != nil || doc.Version != SchemaVersion || len(doc.Entries) != 1 {
		t.Fatalf("%v %+v", err, doc)
	}
	if strings.Contains(readFile(t, s), "/synthetic/network") {
		t.Fatal("the project root path must not be stored")
	}
	e := s.List(Filter{})[0]
	if e.ProjectName != "network" || e.ProjectID != rootID("/synthetic/network") || e.Registered || e.Workspace != "dev" || e.EngineVersion != "1.9.8" {
		t.Fatalf("%+v", e)
	}
}

func TestRestartPersistsAndMarksInterrupted(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	finished(s, 1, "plan", operations.Succeeded, t0)
	// An apply and an init that were still running when Hclaro stopped.
	s.Record(operations.Record{Operation: operations.Operation{ID: id(2), Kind: "apply", Status: operations.Running, StartedAt: t0.Format(time.RFC3339), PlanID: id(9)}, Root: "/synthetic/network", Engine: "terraform"})
	s.Record(operations.Record{Operation: operations.Operation{ID: id(3), Kind: "init", Status: operations.Queued, StartedAt: t0.Format(time.RFC3339)}, Root: "/synthetic/network", Engine: "terraform"})
	if e, _ := s.Get(id(2)); e.Status != StatusRunning || e.Outcome.Result != "running" {
		t.Fatalf("%+v", e)
	}
	again := open(t, dir)
	if len(again.List(Filter{})) != 3 {
		t.Fatal("entries were not kept across a restart")
	}
	for _, n := range []int{2, 3} {
		e, _ := again.Get(id(n))
		if e.Status != StatusUnknown || e.Outcome.Result != "interrupted" || e.Session != "" {
			t.Fatalf("%+v", e)
		}
	}
	apply, _ := again.Get(id(2))
	if !strings.Contains(apply.Outcome.Summary, "partly changed") {
		t.Fatal(apply.Outcome.Summary)
	}
	// The rewrite is persisted, so a third start sees the same.
	if e, _ := open(t, dir).Get(id(2)); e.Status != StatusUnknown {
		t.Fatalf("%+v", e)
	}
}

func TestRunningEntryOfThisSessionIsKeptAndFinished(t *testing.T) {
	s := open(t, t.TempDir())
	start := operations.Record{Operation: operations.Operation{ID: id(1), Kind: "plan", Status: operations.Running, StartedAt: t0.Format(time.RFC3339)}, Root: "/r", Engine: "terraform"}
	s.Record(start)
	if err := s.Delete(id(1)); err != ErrRunning {
		t.Fatalf("deleting a running entry: %v", err)
	}
	if n, _ := s.Clear(Filter{}); n != 0 || len(s.List(Filter{})) != 1 {
		t.Fatal("clear must keep running operations")
	}
	start.Operation.Status, start.Final = operations.Failed, true
	s.Record(start)
	if e, _ := s.Get(id(1)); e.Status != StatusFailed || e.Outcome.Result != "not_planned" {
		t.Fatalf("%+v", e)
	}
}

func TestCorruptHistoryIsMovedAside(t *testing.T) {
	for name, body := range map[string]string{
		"not json":       "{not json",
		"no version":     `{"entries":[]}`,
		"unknown fields": `{"version":1,"retention":{"maxEntries":500,"maxAgeDays":90,"maxBytes":4194304},"entries":[],"extra":1}`,
		"truncated":      `{"version":1,"retention":{"maxEntries":500,"maxAg`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, fileName)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			s := open(t, dir)
			status, notice, _ := s.Status()
			if status != Ready || !strings.Contains(notice, "moved to history.json.corrupt-20261106T100000Z") {
				t.Fatal(status, notice)
			}
			if b, err := os.ReadFile(path + ".corrupt-20261106T100000Z"); err != nil || string(b) != body {
				t.Fatal("backup differs", err)
			}
			finished(s, 1, "plan", operations.Succeeded, t0)
			if len(open(t, dir).List(Filter{})) != 1 {
				t.Fatal("history did not recover")
			}
		})
	}
}

func TestInvalidEntriesAreDroppedIndividually(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	finished(s, 1, "plan", operations.Succeeded, t0)
	var doc map[string]any
	_ = json.Unmarshal([]byte(readFile(t, s)), &doc)
	entries := doc["entries"].([]any)
	bad := map[string]any{}
	for k, v := range entries[0].(map[string]any) {
		bad[k] = v
	}
	bad["id"] = "../../etc/passwd"
	entries = append(entries, bad)
	doc["entries"] = entries
	b, _ := json.Marshal(doc)
	_ = os.WriteFile(s.path, b, 0o600)
	if got := open(t, dir).List(Filter{}); len(got) != 1 || got[0].ID != id(1) {
		t.Fatalf("%+v", got)
	}
}

func TestHandEditedSecretsAreScrubbedOnRead(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	finished(s, 1, "plan", operations.Failed, t0, func(r *operations.Record) {
		r.Operation.Diagnostic = &operations.Diagnostic{Code: "plan_failed", Message: "ok"}
	})
	edited := strings.Replace(readFile(t, s), `"message":"ok"`, `"message":"password=`+canary+`"`, 1)
	_ = os.WriteFile(s.path, []byte(edited), 0o600)
	e, _ := open(t, dir).Get(id(1))
	if b, _ := json.Marshal(e); strings.Contains(string(b), canary) {
		t.Fatalf("canary served from a hand-edited file: %s", b)
	}
}

func TestNewerSchemaIsLeftUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	body := `{"version":99,"entries":[{"anything":true}]}`
	_ = os.WriteFile(path, []byte(body), 0o600)
	s := open(t, dir)
	if status, notice, _ := s.Status(); status != ReadOnly || !strings.Contains(notice, "newer Hclaro (schema 99)") {
		t.Fatal(status, notice)
	}
	finished(s, 1, "plan", operations.Succeeded, t0)
	if err := s.SetRetention(DefaultPolicy); err != ErrUnavailable {
		t.Fatal(err)
	}
	if _, err := s.Clear(Filter{}); err != ErrUnavailable {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != body {
		t.Fatal("a newer history file was changed")
	}
}

func TestMigrationUpgradesOlderSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	// A hypothetical schema 0 named the kind "action" and stored no retention.
	legacy := `{"version":0,"entries":[{"id":"` + id(1) + `","action":"plan","status":"succeeded","startedAt":"2026-11-05T10:00:00Z","durationMs":5,"engine":"terraform","projectId":"` + id(7) + `","projectName":"net","registered":true,"command":["terraform","plan"],"outcome":{"result":"planned","summary":"x","verified":false},"excerpt":[]}]}`
	_ = os.WriteFile(path, []byte(legacy), 0o600)
	migrations := map[int]Migration{0: func(raw json.RawMessage) (json.RawMessage, error) {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		for _, e := range doc["entries"].([]any) {
			m := e.(map[string]any)
			m["kind"] = m["action"]
			delete(m, "action")
		}
		doc["version"] = 1
		doc["retention"] = DefaultPolicy
		return json.Marshal(doc)
	}}
	s := OpenWith(dir, t0, migrations)
	if status, notice, _ := s.Status(); status != Ready || notice != "" {
		t.Fatal(status, notice)
	}
	e, ok := s.Get(id(1))
	if !ok || e.Kind != "plan" || e.ProjectName != "net" {
		t.Fatalf("%+v", e)
	}
	var doc document
	if err := json.Unmarshal([]byte(readFile(t, s)), &doc); err != nil || doc.Version != SchemaVersion {
		t.Fatal("the migrated file was not written", err)
	}
	other := t.TempDir()
	_ = os.WriteFile(filepath.Join(other, fileName), []byte(legacy), 0o600)
	if _, notice, _ := OpenWith(other, t0, nil).Status(); !strings.Contains(notice, "no upgrade from schema 0") {
		t.Fatal(notice)
	}
}

func TestInterruptedWriteKeepsPreviousFile(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	finished(s, 1, "plan", operations.Succeeded, t0)
	before := readFile(t, s)
	// A crash between creating the temporary file and renaming it leaves this behind.
	partial := filepath.Join(dir, ".history-12345.tmp")
	_ = os.WriteFile(partial, []byte(`{"version":1,"entries":[{"id":"`), 0o600)
	again := open(t, dir)
	if got := readFile(t, again); got != before {
		t.Fatal("the history file changed")
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatal("leftover temporary file was not removed")
	}
	if len(again.List(Filter{})) != 1 {
		t.Fatal("entries lost")
	}
	// A write that fails (the directory is replaced by a file) leaves the cached and on-disk
	// history unchanged and reports the failure.
	// Directory permissions do not stop root, and Windows has no POSIX modes.
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		blocked := open(t, t.TempDir())
		finished(blocked, 1, "plan", operations.Succeeded, t0)
		_ = os.Chmod(filepath.Dir(blocked.path), 0o500)
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(blocked.path), 0o700) })
		finished(blocked, 2, "plan", operations.Succeeded, t0)
		if _, notice, _ := blocked.Status(); !strings.Contains(notice, "could not be written") {
			t.Fatal(notice)
		}
		if len(blocked.List(Filter{})) != 1 {
			t.Fatal("a failed write must not change the history")
		}
	}
}

func TestRetentionBoundaries(t *testing.T) {
	if err := (HistoryRetentionPolicy{MaxEntries: 9, MaxAgeDays: 1, MaxBytes: MinBytes}).Validate(); err == nil {
		t.Fatal("below minimum entries")
	}
	if err := (HistoryRetentionPolicy{MaxEntries: 10, MaxAgeDays: 0, MaxBytes: MinBytes}).Validate(); err == nil {
		t.Fatal("zero days")
	}
	if err := (HistoryRetentionPolicy{MaxEntries: 10, MaxAgeDays: 1, MaxBytes: MaxBytesLimit + 1}).Validate(); err == nil {
		t.Fatal("above size limit")
	}
	if err := (HistoryRetentionPolicy{MaxEntries: MinEntries, MaxAgeDays: MinAgeDays, MaxBytes: MinBytes}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (HistoryRetentionPolicy{MaxEntries: MaxEntriesLimit, MaxAgeDays: MaxAgeDaysLimit, MaxBytes: MaxBytesLimit}).Validate(); err != nil {
		t.Fatal(err)
	}

	s := open(t, t.TempDir())
	if err := s.SetRetention(HistoryRetentionPolicy{MaxEntries: 10, MaxAgeDays: 30, MaxBytes: MaxBytesLimit}); err != nil {
		t.Fatal(err)
	}
	// Count: exactly 10 kept, the oldest go first.
	for i := 1; i <= 12; i++ {
		finished(s, i, "plan", operations.Succeeded, t0.Add(time.Duration(i)*time.Minute))
	}
	got := s.List(Filter{})
	if len(got) != 10 || got[0].ID != id(12) || got[9].ID != id(3) {
		t.Fatalf("count retention: %d %s %s", len(got), got[0].ID, got[len(got)-1].ID)
	}
	// Age: an entry exactly 30 days old is kept, one second older is removed.
	s2 := open(t, t.TempDir())
	_ = s2.SetRetention(HistoryRetentionPolicy{MaxEntries: 100, MaxAgeDays: 30, MaxBytes: MaxBytesLimit})
	finished(s2, 1, "plan", operations.Succeeded, t0.Add(-30*24*time.Hour))
	finished(s2, 2, "plan", operations.Succeeded, t0.Add(-30*24*time.Hour-time.Second))
	if got := s2.List(Filter{}); len(got) != 1 || got[0].ID != id(1) {
		t.Fatalf("age retention: %+v", got)
	}
	// Size: the file never exceeds the limit; the newest entries are kept.
	s3 := open(t, t.TempDir())
	_ = s3.SetRetention(HistoryRetentionPolicy{MaxEntries: 5000, MaxAgeDays: 30, MaxBytes: MinBytes})
	big := make([]operations.Event, 40)
	for i := range big {
		big[i] = operations.Event{Seq: int64(i + 1), Time: t0.Format(time.RFC3339), Level: "info", Text: strings.Repeat("resource changed ", 25)}
	}
	for i := 1; i <= 60; i++ {
		finished(s3, i, "plan", operations.Succeeded, t0.Add(time.Duration(i)*time.Second), func(r *operations.Record) { r.Excerpt = big })
	}
	fi, _ := os.Stat(s3.path)
	got = s3.List(Filter{})
	if fi.Size() > MinBytes || len(got) == 0 || len(got) == 60 || got[0].ID != id(60) {
		t.Fatalf("size retention: %d bytes, %d entries", fi.Size(), len(got))
	}
	// Lowering the policy applies at once.
	_ = s.SetRetention(HistoryRetentionPolicy{MaxEntries: 10, MaxAgeDays: 1, MaxBytes: MaxBytesLimit})
	if len(s.List(Filter{})) != 10 {
		t.Fatal("entries within a day must stay")
	}
}

func TestRetentionNeverDropsRunningEntries(t *testing.T) {
	s := open(t, t.TempDir())
	_ = s.SetRetention(HistoryRetentionPolicy{MaxEntries: 10, MaxAgeDays: 1, MaxBytes: MaxBytesLimit})
	s.Record(operations.Record{Operation: operations.Operation{ID: id(100), Kind: "apply", Status: operations.Running, StartedAt: t0.Add(-48 * time.Hour).Format(time.RFC3339)}, Root: "/r", Engine: "terraform"})
	for i := 1; i <= 15; i++ {
		finished(s, i, "plan", operations.Succeeded, t0.Add(time.Duration(i)*time.Minute))
	}
	if _, ok := s.Get(id(100)); !ok {
		t.Fatal("a running apply was dropped by retention")
	}
}

func TestFiltersAndClearAreProjectScoped(t *testing.T) {
	s := open(t, t.TempDir())
	s.Project = func(root string) (string, string, bool) {
		if root == "/a" {
			return id(500), "alpha", true
		}
		return "", "", false
	}
	finished(s, 1, "plan", operations.Succeeded, t0, func(r *operations.Record) { r.Root = "/a" })
	finished(s, 2, "plan", operations.Failed, t0.Add(time.Minute), func(r *operations.Record) { r.Root = "/a"; r.Workspace = "prod" })
	finished(s, 3, "apply", operations.Succeeded, t0.Add(2*time.Minute), func(r *operations.Record) { r.Root = "/b" })
	if got := s.List(Filter{ProjectID: id(500)}); len(got) != 2 || got[0].ProjectName != "alpha" || !got[0].Registered {
		t.Fatalf("%+v", got)
	}
	if got := s.List(Filter{ProjectID: id(500), Workspace: "prod"}); len(got) != 1 || got[0].ID != id(2) {
		t.Fatalf("%+v", got)
	}
	if got := s.List(Filter{Status: StatusFailed}); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if got := s.List(Filter{Query: "APPLY"}); len(got) != 1 || got[0].ID != id(3) {
		t.Fatalf("%+v", got)
	}
	n, err := s.Clear(Filter{ProjectID: id(500)})
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if got := s.List(Filter{}); len(got) != 1 || got[0].ID != id(3) {
		t.Fatalf("clearing one project removed another's history: %+v", got)
	}
	if err := s.Delete(id(1)); err != ErrNotFound {
		t.Fatal(err)
	}
}

func reviewed(planID string, changes ...plan.Change) *operations.Plan {
	p := &operations.Plan{ID: planID, Kind: "plan", Fingerprint: strings.Repeat("ab", 32), VarFiles: []string{"dev.tfvars"}, Replace: []string{}}
	p.Summary = map[string]int{}
	for _, c := range changes {
		p.Summary[c.Action]++
	}
	p.Changes = changes
	return p
}

func TestPlanSummaryKeepsAddressesNotValues(t *testing.T) {
	s := open(t, t.TempDir())
	p := reviewed(id(50),
		plan.Change{Address: "aws_instance.web[0]", Action: plan.Replace},
		plan.Change{Address: "aws_s3_bucket.logs", Action: plan.Create},
		plan.Change{Address: `aws_ssm_parameter.x["password=` + canary + `"]`, Action: plan.Update},
		plan.Change{Address: "aws_iam_role.ok", Action: plan.NoOp},
		plan.Change{Address: "aws_vpc.main", Action: plan.NoOp, Importing: true},
	)
	p.Outputs = []plan.Output{{Name: "db_password", Action: plan.Update, After: canary}}
	p.Details = map[string]plan.Detail{"x": {After: canary}}
	finished(s, 1, "plan", operations.Succeeded, t0, func(r *operations.Record) { r.Plan = p })
	e, _ := s.Get(id(1))
	if e.Plan == nil || e.Plan.Counts[plan.Replace] != 1 || e.Plan.Counts[plan.NoOp] != 0 || e.Plan.Outputs != 1 || len(e.Plan.Changes) != 4 || e.Plan.Fingerprint != "abababababab" {
		t.Fatalf("%+v", e.Plan)
	}
	if e.Plan.Changes[2].Address != withheldAddr || e.Plan.Changes[3].Action != "import" {
		t.Fatalf("%+v", e.Plan.Changes)
	}
	if strings.Contains(readFile(t, s), canary) || strings.Contains(readFile(t, s), "db_password") {
		t.Fatal("plan values or output names reached the history file")
	}
	// More than maxChanges changes: the rest are counted, not listed.
	many := make([]plan.Change, maxChanges+7)
	for i := range many {
		many[i] = plan.Change{Address: fmt.Sprintf("null_resource.n[%d]", i), Action: plan.Create}
	}
	finished(s, 2, "plan", operations.Succeeded, t0, func(r *operations.Record) { r.Plan = reviewed(id(51), many...) })
	e, _ = s.Get(id(2))
	if len(e.Plan.Changes) != maxChanges || e.Plan.Omitted != 7 || e.Plan.Counts[plan.Create] != maxChanges+7 {
		t.Fatalf("%d %d", len(e.Plan.Changes), e.Plan.Omitted)
	}
}

func TestApplyLinksPlanAndNeverClaimsVerified(t *testing.T) {
	s := open(t, t.TempDir())
	p := reviewed(id(50), plan.Change{Address: "aws_instance.web", Action: plan.Replace})
	finished(s, 1, "plan", operations.Succeeded, t0, func(r *operations.Record) { r.Plan = p })
	finished(s, 2, "apply", operations.Succeeded, t0.Add(time.Minute), func(r *operations.Record) {
		r.Plan, r.Operation.PlanID, r.Operation.Outcome, r.Operation.StateReloaded = p, p.ID, "applied", true
	})
	planned, _ := s.Get(id(1))
	applied, _ := s.Get(id(2))
	if planned.Plan.Execution == nil || planned.Plan.Execution.OperationID != id(2) || planned.Plan.Execution.Result != "applied" {
		t.Fatalf("%+v", planned.Plan)
	}
	if applied.Outcome.Result != "applied" || applied.Outcome.Verified || !applied.Outcome.StateReloaded || applied.Outcome.PlanID != id(50) || !strings.Contains(applied.Outcome.Summary, "not proof") {
		t.Fatalf("%+v", applied.Outcome)
	}
	if planned.Outcome.Result != "planned" || !strings.Contains(planned.Outcome.Summary, "cannot be applied") {
		t.Fatalf("%+v", planned.Outcome)
	}
}

func TestOutcomesAreNeverOptimistic(t *testing.T) {
	s := open(t, t.TempDir())
	ran := false
	cases := []struct {
		n              int
		kind, status   string
		mutate         func(*operations.Record)
		result, entryS string
	}{
		{1, "plan", operations.Cancelled, func(r *operations.Record) { r.Operation.Replace = []string{"aws_instance.web"} }, "not_planned", StatusCancelled},
		{2, "plan", operations.Failed, nil, "not_planned", StatusFailed},
		{3, "apply", operations.Cancelled, func(r *operations.Record) { r.Operation.Outcome = "uncertain" }, "partial", StatusCancelled},
		{4, "apply", operations.Failed, func(r *operations.Record) { r.Operation.Outcome = "not_applied" }, "not_applied", StatusFailed},
		{5, "apply", operations.Failed, func(r *operations.Record) { r.Operation.Outcome = "uncertain" }, "partial", StatusFailed},
		{6, "init", operations.Cancelled, func(r *operations.Record) { r.Operation.LockfileChanged = &ran }, "partial", StatusCancelled},
		{7, "init", operations.Unknown, nil, "unverified", StatusUnknown},
		{8, "workspace", operations.Failed, func(r *operations.Record) {
			r.Operation.Workspace = &operations.WorkspaceResult{Action: "new", Name: "x", Ran: true}
		}, "unverified", StatusFailed},
		{9, "drift", operations.Failed, func(r *operations.Record) { r.DriftStatus = operations.ScanPartial }, "partial", StatusFailed},
		{10, "plan", operations.Running, nil, "not_planned", StatusUnknown},
	}
	for _, c := range cases {
		finished(s, c.n, c.kind, c.status, t0, func(r *operations.Record) {
			if c.mutate != nil {
				c.mutate(r)
			}
		})
		e, _ := s.Get(id(c.n))
		if e.Status != c.entryS || e.Outcome.Result != c.result || e.Outcome.Verified {
			t.Errorf("case %d: %s %+v", c.n, e.Status, e.Outcome)
		}
	}
	if e, _ := s.Get(id(1)); e.Kind != "replace" {
		t.Fatalf("cancelled replace recorded as %s", e.Kind)
	}
}

func TestCanaryNeverWritten(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	finished(s, 1, "plan", operations.Failed, t0, func(r *operations.Record) {
		r.Operation.Command = []string{"terraform", "plan", "-var-file=password=" + canary}
		r.Operation.Diagnostic = &operations.Diagnostic{Code: "plan_failed", Message: "token=" + canary}
		r.Operation.Context = &operations.ExecutionContext{ProfileName: "Bearer " + canary, Fingerprint: "zz"}
		r.Excerpt = []operations.Event{{Seq: 3, Level: "error", Text: "Error: " + canary}, {Seq: 4, Level: "\x1b[31m", Text: "ok"}}
		r.EngineVersion = "1.9.8 " + canary
		r.Workspace = canary + "/../x"
	})
	raw := readFile(t, s)
	if strings.Contains(raw, canary) {
		t.Fatalf("canary on disk: %s", raw)
	}
	e, _ := s.Get(id(1))
	if e.ExcerptOmitted != 2 || e.Excerpt[1].Level != "" || e.Workspace != "" || e.EngineVersion != "" {
		t.Fatalf("%+v", e)
	}
	report := NewReport(s.List(Filter{}), Filter{}, "test", t0)
	b, _ := json.Marshal(report)
	if strings.Contains(string(b), canary) || strings.Contains(report.Markdown(), canary) {
		t.Fatal("canary in export")
	}
	if !strings.Contains(strings.Join(report.Omitted, " "), "withheld") {
		t.Fatalf("export does not warn about withheld values: %v", report.Omitted)
	}
}

func TestReportMarkdownEscapesValues(t *testing.T) {
	s := open(t, t.TempDir())
	s.Project = func(string) (string, string, bool) { return id(500), "a|b `x` <script>\n# H", true }
	finished(s, 1, "plan", operations.Succeeded, t0, func(r *operations.Record) {
		r.Plan = reviewed(id(50), plan.Change{Address: "aws_instance.a`b", Action: plan.Create})
		r.Excerpt = []operations.Event{{Seq: 1, Level: "info", Text: "excerpt line"}}
	})
	report := NewReport(s.List(Filter{}), Filter{}, "0.1.0", t0)
	md := report.Markdown()
	if strings.Contains(md, "<script>") || strings.Contains(md, "a|b") || strings.Contains(md, "\n# H") || strings.Contains(md, "excerpt line") {
		t.Fatal(md)
	}
	if len(report.Entries[0].Excerpt) != 0 || report.Omitted[0] != "Log excerpts are not exported." {
		t.Fatal("excerpts must not be exported")
	}
	many := make([]HistoryEntry, MaxExport+3)
	if r := NewReport(many, Filter{}, "", t0); len(r.Entries) != MaxExport || r.Total != MaxExport+3 || !strings.Contains(strings.Join(r.Omitted, " "), "newest 1000") {
		t.Fatalf("%d %v", len(r.Entries), r.Omitted)
	}
}

func TestCompareOnlyCompatiblePlans(t *testing.T) {
	s := open(t, t.TempDir())
	a := reviewed(id(50), plan.Change{Address: "aws_instance.web", Action: plan.Update}, plan.Change{Address: "aws_s3_bucket.logs", Action: plan.Create}, plan.Change{Address: "aws_iam_role.r", Action: plan.Delete})
	b := reviewed(id(51), plan.Change{Address: "aws_instance.web", Action: plan.Replace}, plan.Change{Address: "aws_s3_bucket.logs", Action: plan.Create}, plan.Change{Address: "aws_sqs_queue.q", Action: plan.Create})
	b.Fingerprint = strings.Repeat("cd", 32)
	finished(s, 1, "plan", operations.Succeeded, t0, func(r *operations.Record) { r.Plan = a })
	finished(s, 2, "plan", operations.Succeeded, t0.Add(time.Minute), func(r *operations.Record) { r.Plan = b; r.EngineVersion = "1.9.9" })
	finished(s, 3, "plan", operations.Succeeded, t0, func(r *operations.Record) { r.Plan = a; r.Workspace = "prod" })
	finished(s, 4, "plan", operations.Succeeded, t0, func(r *operations.Record) { r.Plan = a; r.Root = "/other" })
	finished(s, 5, "graph", operations.Succeeded, t0, nil)
	finished(s, 6, "plan", operations.Succeeded, t0, func(r *operations.Record) { r.Plan = a; r.Engine = "tofu" })
	get := func(n int) HistoryEntry { e, _ := s.Get(id(n)); return e }
	// Order of arguments does not matter; the older plan is the left side.
	c, err := Compare(get(2), get(1))
	if err != nil {
		t.Fatal(err)
	}
	if c.Left != id(1) || len(c.Added) != 1 || c.Added[0].Address != "aws_sqs_queue.q" || len(c.Removed) != 1 || c.Removed[0].Address != "aws_iam_role.r" || len(c.Changed) != 1 || c.Changed[0].Right != plan.Replace || c.Same != 1 || !c.Complete {
		t.Fatalf("%+v", c)
	}
	if len(c.Notices) != 2 {
		t.Fatalf("notices %v", c.Notices)
	}
	for _, n := range []int{3, 4, 5, 6, 1} {
		if _, err := Compare(get(1), get(n)); !IsIncompatible(err) {
			t.Errorf("plan 1 vs %d: %v", n, err)
		}
	}
}

func TestReadGit(t *testing.T) {
	root := t.TempDir()
	if ReadGit(root) != nil {
		t.Fatal("not a repository")
	}
	commit := strings.Repeat("a1", 20)
	git := filepath.Join(root, ".git")
	_ = os.MkdirAll(filepath.Join(git, "refs", "heads", "feature"), 0o700)
	_ = os.WriteFile(filepath.Join(git, "HEAD"), []byte("ref: refs/heads/feature/x\n"), 0o600)
	// A new branch with no commit has no identity.
	if ReadGit(root) != nil {
		t.Fatal("unborn branch")
	}
	_ = os.WriteFile(filepath.Join(git, "refs", "heads", "feature", "x"), []byte(commit+"\n"), 0o600)
	sub := filepath.Join(root, "envs", "dev")
	_ = os.MkdirAll(sub, 0o700)
	if g := ReadGit(sub); g == nil || g.Commit != commit || g.Branch != "feature/x" {
		t.Fatalf("%+v", g)
	}
	// Packed refs and a detached HEAD.
	_ = os.Remove(filepath.Join(git, "refs", "heads", "feature", "x"))
	packed := strings.Repeat("b2", 20)
	_ = os.WriteFile(filepath.Join(git, "packed-refs"), []byte("# pack-refs\n"+packed+" refs/heads/feature/x\n"), 0o600)
	if g := ReadGit(root); g == nil || g.Commit != packed {
		t.Fatalf("%+v", g)
	}
	_ = os.WriteFile(filepath.Join(git, "HEAD"), []byte(commit+"\n"), 0o600)
	if g := ReadGit(root); g == nil || g.Commit != commit || g.Branch != "" {
		t.Fatalf("%+v", g)
	}
	// Refs that escape the refs directory or look odd are refused.
	for _, head := range []string{"ref: refs/../../HEAD", "ref: heads/x", "ref: refs/heads/" + canary + " x", "garbage"} {
		_ = os.WriteFile(filepath.Join(git, "HEAD"), []byte(head), 0o600)
		if g := ReadGit(root); g != nil {
			t.Fatalf("%q: %+v", head, g)
		}
	}
	// A worktree: .git is a file pointing at the real directory with a common directory.
	wt := t.TempDir()
	real := filepath.Join(t.TempDir(), "wt")
	_ = os.MkdirAll(real, 0o700)
	_ = os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+real+"\n"), 0o600)
	_ = os.WriteFile(filepath.Join(real, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600)
	_ = os.WriteFile(filepath.Join(real, "commondir"), []byte(git+"\n"), 0o600)
	_ = os.WriteFile(filepath.Join(git, "packed-refs"), []byte(packed+" refs/heads/main\n"), 0o600)
	if g := ReadGit(wt); g == nil || g.Commit != packed || g.Branch != "main" {
		t.Fatalf("%+v", g)
	}
}

func TestExportPathValidation(t *testing.T) {
	dir := t.TempDir()
	s := open(t, t.TempDir())
	finished(s, 1, "plan", operations.Succeeded, t0)
	report := NewReport(s.List(Filter{}), Filter{}, "test", t0)
	existing := filepath.Join(dir, "old.json")
	_ = os.WriteFile(existing, []byte("keep"), 0o600)
	_ = os.MkdirAll(filepath.Join(dir, ".terraform"), 0o700)
	for _, bad := range []string{"", filepath.Join(dir, "report.txt"), filepath.Join(dir, "missing", "r.json"), existing, filepath.Join(dir, ".terraform", "r.md"), "r\x00.json"} {
		if err := WriteExport(bad, report); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if b, _ := os.ReadFile(existing); string(b) != "keep" {
		t.Fatal("an existing file was overwritten")
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(dir, "link.json")
		_ = os.Symlink(existing, link)
		if err := WriteExport(link, report); err == nil {
			t.Fatal("a symbolic link was followed")
		}
	}
	for _, name := range []string{"history.json", "history.md"} {
		path := filepath.Join(dir, name)
		if err := WriteExport(path, report); err != nil {
			t.Fatal(err)
		}
		fi, _ := os.Stat(path)
		if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", name, fi.Mode())
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "history.json"))
	var back Report
	if json.Unmarshal(b, &back) != nil || len(back.Entries) != 1 {
		t.Fatal(string(b))
	}
}
