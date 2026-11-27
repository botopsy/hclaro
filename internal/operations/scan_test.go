package operations

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"hclaro/internal/scanners"
	"hclaro/internal/scanners/scannerstest"
)

func (h *toolHarness) scan(name string, runner *scanners.Runner) ToolRequest {
	req := h.request("", nil)
	req.Tools, req.Files, req.Tool = nil, nil, ""
	req.Scan = &ScanRun{Name: name, Runner: runner}
	return req
}

func (h *toolHarness) runScan(t *testing.T, req ToolRequest) (Operation, string) {
	t.Helper()
	op, err := h.runner.StartScan(req)
	if err != nil {
		t.Fatal(err)
	}
	done, events := waitInit(t, h.runner, op.ID)
	text := joined(events)
	b, _ := json.Marshal(struct {
		Op   Operation
		List []Operation
	}{done, h.runner.List("p1")})
	for _, secret := range []string{"AKIAFAKESECRETVALUE123", "hunter2-hunter2"} {
		if strings.Contains(string(b)+text, secret) {
			t.Fatalf("%q leaked:\n%s\n%s", secret, b, text)
		}
	}
	return done, text
}

const tflintOut = `{"issues":[` +
	`{"rule":{"name":"terraform_unused_declarations","severity":"warning"},"message":"bucket AKIAFAKESECRETVALUE123 is unused","range":{"filename":"main.tf","start":{"line":3}}},` +
	`{"rule":{"name":"r2","severity":"error"},"message":"api_key = hunter2-hunter2","range":{"filename":"../outside.tf","start":{"line":9}}}` +
	`],"errors":[]}`

func TestScanFindingsAreSanitized(t *testing.T) {
	h := newToolHarness(t)
	fake := scannerstest.Install(t, "tflint", "", tflintOut, "", 2, false)
	runner := &scanners.Runner{Paths: map[string]string{"tflint": fake.Path}}
	op, text := h.runScan(t, h.scan("tflint", runner))
	r := op.Tool
	if op.Kind != "tool" || op.Status != Failed || op.Diagnostic.Code != "scan_findings" || r.Outcome != "findings" || *op.ExitCode != 2 {
		t.Fatalf("%+v %+v", op, r)
	}
	if r.Scan.Counts["high"] != 1 || r.Scan.Counts["medium"] != 1 || r.Scan.Version != "0.50.3" || r.Scan.Fingerprint == "" || len(r.Findings) != 2 {
		t.Fatalf("%+v %+v", r.Scan, r.Findings)
	}
	if f := r.Findings[0]; f.File != "main.tf" || f.Line != 3 || f.Rule != "terraform_unused_declarations" || strings.Contains(f.Summary, "AKIA") {
		t.Errorf("finding = %+v", f)
	}
	// A path outside the project is dropped, never shown.
	if f := r.Findings[1]; f.File != "" || f.Line != 0 {
		t.Errorf("outside file kept: %+v", f)
	}
	if !strings.Contains(text, "terraform_unused_declarations") {
		t.Errorf("log: %s", text)
	}
	if got := op.Command; got[0] != "tflint" || got[1] != "--format=json" {
		t.Errorf("command = %v", got)
	}
}

func TestScanCleanAndFailures(t *testing.T) {
	h := newToolHarness(t)
	fake := scannerstest.Install(t, "trivy", "", `{"SchemaVersion":2}`, "", 0, false)
	op, _ := h.runScan(t, h.scan("trivy", &scanners.Runner{Paths: map[string]string{"trivy": fake.Path}}))
	if op.Status != Succeeded || op.Tool.Outcome != "passed" || len(op.Tool.Findings) != 0 {
		t.Fatalf("%+v", op)
	}
	for _, c := range []struct {
		name, stdout, stderr, code string
		exit                       int
	}{
		{"tflint", "", "Plugin `aws` not found. Did you run tflint --init?", "scanner_plugins", 1},
		{"trivy", "", "FATAL failed to download checks: no such host", "scanner_download", 1},
		{"checkov", "garbage", "", "output_unreadable", 0},
		{"checkov", `{"results":{"failed_checks":[]}}`, "", "scanner_failed", 7},
	} {
		f := scannerstest.Install(t, c.name, "", c.stdout, c.stderr, c.exit, false)
		op, _ := h.runScan(t, h.scan(c.name, &scanners.Runner{Paths: map[string]string{c.name: f.Path}}))
		if op.Status != Failed || op.Diagnostic.Code != c.code || op.Tool.Outcome != "not_checked" || len(op.Tool.Findings) != 0 {
			t.Errorf("%s: %+v %+v", c.name, op, op.Diagnostic)
		}
	}
}

func TestScanNotInstalledRunsNothing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	h := newToolHarness(t)
	op, _ := h.runScan(t, h.scan("checkov", &scanners.Runner{}))
	if op.Status != Failed || op.Diagnostic.Code != "scanner_missing" || op.Tool.Outcome != "not_checked" {
		t.Fatalf("%+v", op)
	}
}

func TestScanCancelAndConflict(t *testing.T) {
	h := newToolHarness(t)
	fake := scannerstest.Install(t, "tflint", "", "", "", 0, true)
	req := h.scan("tflint", &scanners.Runner{Paths: map[string]string{"tflint": fake.Path}})
	op, err := h.runner.StartScan(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.runner.StartScan(req); !errors.Is(err, ErrToolRunning) {
		t.Fatalf("duplicate: %v", err)
	}
	for deadline := time.Now().Add(5 * time.Second); !fake.Ran() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := h.runner.Cancel(op.ID); err != nil {
		t.Fatal(err)
	}
	done, _ := waitInit(t, h.runner, op.ID)
	if done.Status != Cancelled || done.Diagnostic.Code != "cancelled" || done.Tool.Outcome != "not_checked" || len(done.Tool.Findings) != 0 {
		t.Fatalf("%+v %+v", done, done.Diagnostic)
	}
}
