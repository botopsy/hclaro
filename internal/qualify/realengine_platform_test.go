//go:build realengine

package qualify

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestRealPlatform covers the M27.07 platform scenarios WIN1, WIN3 and WIN4 (WIN2 is H5d in
// TestRealHubPaths). WIN4 runs on every platform: CRLF files reach Unix checkouts too.
func TestRealPlatform(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	for _, name := range engines() {
		run.Scenario(t, "win1-interrupt-"+name, name, "launch", false, func(s *Scenario) {
			if runtime.GOOS != "windows" {
				s.Skip("Windows interrupt variant of A6/A7/A7b; the Unix gate runs as A6, A7 and A7b")
			}
			s.NotTested("needs a PowerShell gate fixture, which OD-8 does not cover (its own approval), and a passing F-5")
		})
		run.Scenario(t, "win3-saved-plan-files-"+name, name, "launch", false, func(s *Scenario) {
			if runtime.GOOS != "windows" {
				s.Skip("ACLs and the read-only attribute are Windows-only; Unix modes are checked by the operations tests")
			}
			win3(s, name)
		})
		run.Scenario(t, "win4-crlf-import-"+name, name, "launch", false, func(s *Scenario) {
			i1ImportScenario(s, name, crlfStart)
		})
		run.Scenario(t, "win4-crlf-generated-"+name, name, "launch", false, func(s *Scenario) {
			i3Generated(s, name, crlfStart)
		})
	}
}

// crlfStart rewrites the project's .tf and .tfvars files (the canary var file included) with
// CRLF line endings before init, then starts it like launchStart (WIN4).
func crlfStart(s *Scenario, engine, project string) (ProcessDirs, []string, *Hclaro) {
	n := 0
	err := filepath.WalkDir(project, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".tf" && filepath.Ext(p) != ".tfvars" {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n++
		return os.WriteFile(p, bytes.ReplaceAll(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n")), 0o600)
	})
	if err != nil || n == 0 {
		s.Fatalf("CRLF conversion: %v (%d files)", err, n)
	}
	s.RefreshBaseline(project)
	s.Observe("WIN4: %d .tf/.tfvars files rewritten with CRLF line endings before init", n)
	return launchStart(s, engine, project)
}

// win3 checks the saved-plan folder and file ACLs, then that the files are removed after the
// apply and after shutdown even with the read-only attribute set. Plan expiry (one hour) is
// not waited for.
func win3(s *Scenario, name string) {
	project, _, _, h := launch(s, name, "launch")
	detail := s.LaunchPlan(h, project, name, []string{"launch.tfvars"})
	files := planFiles(s, h)
	for _, f := range files {
		if broad := broadGrants(s, f); len(broad) > 0 {
			s.Fatalf("WIN3 %s is readable by other principals: %v", s.run.San.String(f), broad)
		}
		if err := os.Chmod(f, 0o400); err != nil { // sets FILE_ATTRIBUTE_READONLY on Windows
			s.Fatalf("set read-only: %v", err)
		}
	}
	s.Observe("WIN3 ACLs: owner, SYSTEM and Administrators only on %d saved-plan entries; read-only attribute set on the files", len(files))
	if _, op := s.ApplyReviewed(h, detail.ID); op.Outcome != "applied" {
		s.Fatalf("WIN3 apply: %s %q", op.Status, op.Outcome)
	}
	if left := savedPlanDirs(h); len(left) > 0 {
		s.Fatalf("WIN3 saved plan left after the apply: %v", left)
	}
	s.LaunchPlan(h, project, name, []string{"launch.tfvars", "destroy.tfvars"})
	for _, f := range planFiles(s, h) {
		if fi, err := os.Stat(f); err == nil && !fi.IsDir() {
			_ = os.Chmod(f, 0o400)
		}
	}
	if _, err := h.Stop(2 * time.Minute); err != nil {
		s.Fatalf("WIN3 stop: %v", err)
	}
	if left := savedPlanDirs(h); len(left) > 0 {
		s.Fatalf("WIN3 saved plan left after shutdown with the read-only attribute set: %v", left)
	}
	s.Observe("WIN3 saved plans removed after the apply and after shutdown, read-only attribute set")
}

// planFiles lists the saved-plan folders and the files in them.
func planFiles(s *Scenario, h *Hclaro) []string {
	var out []string
	for _, d := range savedPlanDirs(h) {
		_ = filepath.WalkDir(filepath.Join(h.Dirs.Tmp, d), func(p string, _ fs.DirEntry, err error) error {
			if err == nil {
				out = append(out, p)
			}
			return nil
		})
	}
	if len(out) == 0 {
		s.Fatalf("no saved plan found")
	}
	return out
}

// broadGrants parses `icacls <path>` and returns the allow entries of principals other than
// the owner, SYSTEM, Administrators and CREATOR OWNER.
func broadGrants(s *Scenario, path string) []string {
	out, err := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "icacls.exe"), path).Output()
	if err != nil {
		s.Fatalf("icacls: %v", err)
	}
	s.Artifact("icacls-"+filepath.Base(path)+".txt", out)
	text := strings.Replace(strings.ReplaceAll(string(out), "\r", ""), path, "", 1)
	var broad []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		i := strings.Index(line, ":(")
		if i < 0 || strings.Contains(line, "(DENY)") {
			continue
		}
		who := line[:i]
		switch {
		case strings.EqualFold(who, `NT AUTHORITY\SYSTEM`), strings.EqualFold(who, `BUILTIN\Administrators`),
			strings.EqualFold(who, `CREATOR OWNER`), strings.HasSuffix(strings.ToLower(who), `\`+strings.ToLower(s.run.user)):
		default:
			broad = append(broad, line)
		}
	}
	return broad
}
