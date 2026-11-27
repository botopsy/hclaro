package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hclaro/internal/engine/enginetest"
	"hclaro/internal/operations"
	"hclaro/internal/scanners"
	"hclaro/internal/scanners/scannerstest"
)

func TestScannerRoutes(t *testing.T) {
	s, p := toolServer(t, &enginetest.Fake{VersionValue: "1.9.8"})
	cid := s.open(p.ID, "dev")
	flint := scannerstest.Install(t, "tflint", "", `{"issues":[{"rule":{"name":"r1","severity":"warning"},"message":"unused","range":{"filename":"main.tf","start":{"line":2}}}],"errors":[]}`, "", 2, false)
	s.hub.Scanners = &scanners.Runner{Paths: map[string]string{"tflint": flint.Path, "trivy": "/nonexistent/trivy"}}
	base := "/api/v1/projects/" + p.ID

	// Detection reads versions only.
	w := s.do("GET", base+"/scanners", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":"0.50.3"`) || !strings.Contains(w.Body.String(), "Not installed") || flint.Ran() {
		t.Fatalf("%d %s ran=%v", w.Code, w.Body, flint.Ran())
	}
	body := fmt.Sprintf(`{"contextId":%q}`, cid)
	for _, tc := range []struct {
		name string
		opts []opt
		code int
	}{
		{"no session", []opt{without("Authorization")}, 401},
		{"no csrf", []opt{without("X-Hclaro-CSRF")}, 403},
		{"cross-site origin", []opt{with("Origin", "https://evil.example")}, 403},
	} {
		if w := s.do("POST", base+"/operations/tflint", body, tc.opts...); w.Code != tc.code {
			t.Fatalf("%s: %d", tc.name, w.Code)
		}
	}
	if flint.Ran() {
		t.Fatal("a refused request ran the scanner")
	}
	for _, bad := range []string{`{}`, `{"contextId":"x"}`, fmt.Sprintf(`{"contextId":%q,"args":["--init"]}`, cid)} {
		if w := s.do("POST", base+"/operations/tflint", bad); w.Code != 400 {
			t.Fatalf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
	// --recursive belongs to tflint alone.
	if w := s.do("POST", base+"/operations/trivy", fmt.Sprintf(`{"contextId":%q,"recursive":true}`, cid)); w.Code != 400 {
		t.Fatalf("trivy recursive: %d", w.Code)
	}
	w = s.do("POST", base+"/operations/tflint", fmt.Sprintf(`{"contextId":%q,"recursive":true}`, cid))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	op := s.waitOp(decode[operations.Operation](t, w).ID)
	if op.Diagnostic.Code != "scan_findings" || op.Tool.Scan.Counts["medium"] != 1 || strings.Join(op.Command, " ") != "tflint --format=json --no-color --recursive" {
		t.Fatalf("%+v %+v", op, op.Tool)
	}
	if got := flint.Args(); strings.Join(got, " ") != "--format=json --no-color --recursive" {
		t.Fatalf("argv %v", got)
	}
	// Plan Review's hint: the scan counts while the configuration is unchanged, not after an edit.
	if w := s.do("GET", "/api/v1/contexts/"+cid+"/scans", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"scanner":"tflint"`) || !strings.Contains(w.Body.String(), `"medium":1`) {
		t.Fatalf("scans: %d %s", w.Code, w.Body)
	}
	if err := os.WriteFile(filepath.Join(p.RootPath, "extra.tf"), []byte("# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := s.do("GET", "/api/v1/contexts/"+cid+"/scans", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"scans":[]`) {
		t.Fatalf("stale scans: %d %s", w.Code, w.Body)
	}
	// A scanner that is not installed is refused to run, not downloaded.
	w = s.do("POST", base+"/operations/trivy", body)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if op := s.waitOp(decode[operations.Operation](t, w).ID); op.Diagnostic.Code != "scanner_missing" {
		t.Fatalf("%+v", op.Diagnostic)
	}
}
