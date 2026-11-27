package operations

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"hclaro/internal/engine"
	"hclaro/internal/scanners"
	"hclaro/internal/workspace"
)

// ScanRun describes a scanner run (M36-06). It reuses the project-utility plumbing: one run
// per scanner per context, the shared cap, cancel, history and a shared claim on the root.
type ScanRun struct {
	Name      string
	Recursive bool
	Runner    *scanners.Runner
}

// ScanResult is a scan's summary. Counts is by severity over every parsed issue, including
// those past the findings cap. Fingerprint hashes the configuration as the scan began, so a
// later plan can tell whether the scan still describes it.
type ScanResult struct {
	Scanner     string         `json:"scanner"`
	Version     string         `json:"version,omitempty"`
	Recursive   bool           `json:"recursive"`
	Config      []string       `json:"config"`
	Counts      map[string]int `json:"counts"`
	Hidden      int            `json:"hidden"`
	Fingerprint string         `json:"fingerprint,omitempty"`
}

const scanSuccess = "No findings. This is a static scan of configuration files by the scanner's own rules; it does not prove the configuration is secure."

// StartScan runs a scanner in the background. req.Scan must be set; Tool is the scanner name.
func (r *Inits) StartScan(req ToolRequest) (Operation, error) {
	s := req.Scan
	req.Tool = engine.Tool(s.Name)
	op := Operation{
		Kind: "tool", Command: append([]string{s.Name}, scanners.Argv(s.Name, s.Recursive)...), ProjectID: req.ProjectID, Notices: slices.Clone(req.Notices),
		Tool: &ToolResult{Tool: req.Tool, ContextID: req.ContextID, Workspace: req.Workspace, Outcome: "not_checked", Findings: []Finding{},
			Scan: &ScanResult{Scanner: s.Name, Recursive: s.Recursive, Config: scanners.FoundConfig(s.Name, req.Root), Counts: map[string]int{}}},
	}
	return r.begin(op, recordMeta{req.Root, req.Engine, req.EngineVersion, req.Workspace}, req.Release, toolConflict(req.ContextID, req.Tool), func(ctx context.Context, e *initEntry) { r.runTool(ctx, e, req) })
}

func (r *Inits) executeScan(ctx context.Context, e *initEntry, req ToolRequest) (string, *int) {
	s := req.Scan
	r.stage(e, "Checking project")
	if code := req.Revalidate(); code != "" {
		return code, nil
	}
	if ctx.Err() != nil {
		return r.stopped(e), nil
	}
	fp := workspace.Fingerprint(req.Root, nil, "scan")
	r.stage(e, "Running "+scanners.Title(s.Name))
	out, version, err := s.Runner.Run(ctx, s.Name, req.Root, s.Recursive)
	r.update(e, func(o *Operation) { o.Tool.Scan.Version, o.Tool.Scan.Fingerprint = version, fp })
	switch {
	case ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return r.stopped(e), nil
	case errors.Is(err, scanners.ErrNotRunnable):
		return "scanner_missing", nil
	case err != nil:
		return "scanner_failed", nil
	}
	exit := out.ExitCode
	if out.Truncated {
		return "output_too_large", &exit
	}
	k := collectKnown(req.Root, nil, req.Environ)
	issues, hidden, perr := scanners.Parse(s.Name, out.Stdout)
	ok := perr == nil && validExit(s.Name, exit)
	if !ok {
		code := scanners.Classify(s.Name, string(out.Stderr))
		if perr != nil && code == "scanner_failed" && exit == 0 {
			code = "output_unreadable"
		}
		// The first lines of stderr, sanitized, help the user; they are never parsed.
		if line := sanitize(string(out.Stderr), k, 3); line != "" {
			e.log.Append("warn", line)
		}
		return code, &exit
	}
	t := toolRun{r: r, e: e, k: k}
	findings := make([]Finding, 0, min(len(issues), maxFindings))
	counts := map[string]int{}
	for _, i := range issues {
		counts[i.Severity]++
		if len(findings) == maxFindings {
			continue
		}
		f := Finding{Severity: i.Severity, Rule: sanitize(i.Rule, k, 1), Summary: truncate(sanitize(i.Message, k, 1), 300), File: localFile(strings.TrimPrefix(strings.ReplaceAll(i.File, "\\", "/"), "./")), Line: max(i.Line, 0)}
		if f.File == "" {
			f.Line = 0
		}
		where := ""
		if f.File != "" {
			where = fmt.Sprintf(" (%s:%d)", f.File, f.Line)
		}
		e.log.Append("warn", f.Severity+" "+f.Rule+": "+f.Summary+where)
		findings = append(findings, f)
	}
	t.update(func(r *ToolResult) {
		r.Findings, r.Scan.Counts, r.Scan.Hidden = findings, counts, hidden
		r.Outcome = "passed"
		if len(issues) > 0 {
			r.Outcome = "findings"
		}
	})
	if len(issues) > 0 {
		return "scan_findings", &exit
	}
	return "succeeded", &exit
}

// validExit lists the exit codes each scanner uses for "ran to completion": tflint 2 means
// issues, Checkov 1 means failed checks. Anything else is a failure whatever was printed.
func validExit(name string, code int) bool {
	switch name {
	case scanners.TFLint:
		return code == 0 || code == 2
	case scanners.Checkov:
		return code == 0 || code == 1
	}
	return code == 0
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
