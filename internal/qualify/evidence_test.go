package qualify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func evidenceRun() RunResult {
	f := false
	sc := func(id, engine, status string, hub bool) ScenarioResult {
		return ScenarioResult{ID: id, Engine: engine, Mode: "launch", HubExecutionFlag: hub, Status: status, Canary: "absent", Steps: []Step{}, DirectEngineCall: []string{}, Artifacts: []string{}}
	}
	return RunResult{SchemaVersion: 1, RunID: "r", StartedAt: "2026-11-15T10:00:00Z", FinishedAt: "2026-11-15T11:00:00Z",
		HclaroCommit: "abc", HclaroVersion: "v", HclaroBinarySha256: "s", OS: "linux", Arch: "amd64", OSVersion: "x", RunnerImage: "img",
		Engines:   []EngineRecord{{Name: "terraform", Version: "1.17.2", Provenance: "verified"}, {Name: "tofu", Version: "1.13.9", Provenance: "verified"}},
		Providers: []ProviderRecord{}, Isolation: Isolation{Mechanism: "unshare", Level: "enforced"}, CredentialFilesReachable: &f,
		EnvironmentAllowlist: []string{"PATH"},
		Scenarios: []ScenarioResult{
			sc("a1-apply-terraform", "terraform", Pass, false),
			sc("a7b-shutdown-grace-terraform", "terraform", NotTested, false),
			sc("l1-state-locked-tofu", "tofu", Fail, false),
			sc("h2-a1-terraform", "terraform", Pass, true),
			sc("h6-binary-terraform", "terraform", Pass, true),
			sc("h6-binary-tofu", "tofu", Skipped, true),
		}}
}

func encode(t *testing.T, r RunResult, edit func(map[string]any)) []byte {
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(m)
	}
	b, _ = json.Marshal(m)
	return b
}

func TestValidateRun(t *testing.T) {
	ok := evidenceRun()
	ok.GoTestExit = 1 // with the FAIL scenario
	if _, errs := ValidateRun(encode(t, ok, nil), true); len(errs) != 0 {
		t.Fatalf("valid run refused: %v", errs)
	}
	for name, c := range map[string]struct {
		edit func(*RunResult)
		raw  func(map[string]any)
		rows bool
		want string
	}{
		"missing dirty key":    {raw: func(m map[string]any) { delete(m, "hclaroDirty") }, want: "missing run field hclaroDirty"},
		"null credential flag": {raw: func(m map[string]any) { m["credentialFilesReachable"] = nil }, want: "missing run field credentialFilesReachable"},
		"dirty":                {edit: func(r *RunResult) { r.HclaroDirty = true }, want: "hclaroDirty"},
		"partial":              {edit: func(r *RunResult) { r.Partial = true }, want: "partial"},
		"exit without FAIL": {edit: func(r *RunResult) {
			r.Scenarios[2].Status = Pass
		}, want: "goTestExit 1 without a FAIL"},
		"unverified engine for rows": {edit: func(r *RunResult) { r.Engines[0].Provenance = "unverified" }, rows: true, want: "provenance"},
		"canary found":               {edit: func(r *RunResult) { r.Scenarios[0].Canary = "found" }, want: "canary"},
		"unknown status":             {edit: func(r *RunResult) { r.Scenarios[0].Status = "OK" }, want: "status"},
		"Windows 8.3 path": {edit: func(r *RunResult) {
			r.Scenarios[0].Observations = []string{`C:\Users\RUNNER~1\AppData\Local\Temp\x`}
		}, want: "unsanitised path"},
		"unix home path": {edit: func(r *RunResult) { r.Scenarios[0].Reason = "/home/runner/work" }, want: "unsanitised path"},
	} {
		r := evidenceRun()
		r.GoTestExit = 1
		r.Engines = append([]EngineRecord(nil), r.Engines...)
		r.Scenarios = append([]ScenarioResult(nil), r.Scenarios...)
		if c.edit != nil {
			c.edit(&r)
		}
		_, errs := ValidateRun(encode(t, r, c.raw), c.rows)
		found := false
		for _, e := range errs {
			found = found || strings.Contains(e.Error(), c.want)
		}
		if !found {
			t.Errorf("%s: errors %v, want one containing %q", name, errs, c.want)
		}
	}
	// Unverified engines are fine for a section-only run.
	r := evidenceRun()
	r.GoTestExit = 1
	r.Engines = []EngineRecord{{Name: "terraform", Version: "1.9.8", Provenance: "unverified"}}
	if _, errs := ValidateRun(encode(t, r, nil), false); len(errs) != 0 {
		t.Errorf("section-only run refused: %v", errs)
	}
}

func TestEvidenceRows(t *testing.T) {
	r := evidenceRun()
	r.Isolation.Level = "weak-proxy"
	rows, err := Rows(r, "[x](qualification/f.md#a)")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, row := range rows {
		got = append(got, row.Engine+" "+row.Version+" "+row.Status+": "+row.Operation)
		if !strings.Contains(row.Environment, "offline not verified") || row.Date != "2026-11-15" || row.Platform != "linux/amd64" {
			t.Errorf("row %+v", row)
		}
	}
	want := []string{
		"Terraform 1.17.2 PASS: Saved-plan apply (a1-apply)",
		"Terraform 1.17.2 NOT_TESTED: Saved-plan apply (a7b-shutdown-grace)",
		"OpenTofu 1.13.9 FAIL: State lock (l1-state-locked)",
		// h6-binary is PASS for one engine and SKIPPED for the other: listed by full ID.
		"Terraform + OpenTofu 1.17.2 + 1.13.9 PASS: `--experimental-hub-execution`: Hub execution (h2-a1, h6-binary-terraform)",
		"Terraform + OpenTofu 1.17.2 + 1.13.9 NOT_TESTED: `--experimental-hub-execution`: Hub execution (h6-binary-tofu)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if h := EvidenceHeading("m27-07 dispatch5 windows-amd64", r); EvidenceAnchor(h) != "2026-11-15-m27-07-dispatch5-windows-amd64-offline-not-verified" {
		t.Errorf("anchor of %q: %s", h, EvidenceAnchor(h))
	}

	r = evidenceRun()
	r.Scenarios = append(r.Scenarios, ScenarioResult{ID: "zz-new-terraform", Engine: "terraform", Status: Pass})
	if _, err := Rows(r, ""); err == nil || !strings.Contains(err.Error(), "no matrix operation") {
		t.Errorf("unmapped family: %v", err)
	}
	r = evidenceRun()
	r.Engines = r.Engines[:1]
	r.Scenarios = r.Scenarios[3:4]
	if _, err := Rows(r, ""); err == nil {
		t.Error("Hub scenario in a single-engine run accepted")
	}
	// Fake-engine F-5 scenarios never become rows.
	r = evidenceRun()
	r.Scenarios = []ScenarioResult{{ID: "f5-hub-stop-terraform", Engine: "terraform", Status: Pass}}
	if rows, err := Rows(r, ""); err != nil || len(rows) != 0 {
		t.Errorf("F-5 rows %v %v", rows, err)
	}
}

func TestRunFilter(t *testing.T) {
	dir := t.TempDir()
	if f, err := RunFilter(dir); err != nil || f != FullFilter {
		t.Fatalf("no file: %q %v", f, err)
	}
	for content, want := range map[string]string{"^TestReal\n": FullFilter, "^TestRealLocks$\r\n": "^TestRealLocks$"} {
		if err := os.WriteFile(filepath.Join(dir, "run-filter.txt"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if f, err := RunFilter(dir); err != nil || f != want {
			t.Errorf("%q: %q %v", content, f, err)
		}
	}
}

func TestEvidenceSection(t *testing.T) {
	r := evidenceRun()
	r.Scenarios[0].DirectEngineCall = []string{"show -json", "init -input=false -no-color"}
	r.Scenarios[0].Reason = "a | b <TMP>"
	s := Section("m27-07 x", r, "abc", []string{"note"})
	for _, want := range []string{"## 2026-11-15 m27-07 x\n", "| Engine versions | 1.17.2 + 1.13.9 |", "none of them applies", "`a \\| b <TMP>`", "- note"} {
		if !strings.Contains(s, want) {
			t.Errorf("section lacks %q", want)
		}
	}
	r.Scenarios[0].DirectEngineCall = []string{"apply -auto-approve"}
	if s := Section("x", r, "", nil); !strings.Contains(s, "a direct call names apply") {
		t.Error("a mutating direct call was not flagged")
	}
}
