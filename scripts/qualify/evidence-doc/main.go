// Command evidence-doc validates the final M27 run JSONs and generates the evidence file and the
// support-matrix rows from them (M27.08):
//
//	go run ./scripts/qualify/evidence-doc -root ~/hclaro-qualify/evidence \
//	  -runs docs/qualification/real-engine-runs.json -out docs/qualification/real-engine-20261116.md
//
// It refuses to write anything while any run is invalid. Rows go to stdout, IDs left as "?".
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"hclaro/internal/qualify"
)

type runSpec struct {
	Label string   `json:"label"` // section label; no underscores (GitHub keeps them in anchors, the matrix test drops them)
	Path  string   `json:"path"`  // run.json, relative to -root
	Rows  bool     `json:"rows"`  // source of matrix rows
	Notes []string `json:"notes,omitempty"`
}

type runList struct {
	Title string    `json:"title"`
	Intro []string  `json:"intro"`
	Runs  []runSpec `json:"runs"`
}

func main() {
	root := flag.String("root", "", "evidence root")
	list := flag.String("runs", "", "run list JSON")
	out := flag.String("out", "", "evidence Markdown to write")
	flag.Parse()
	if err := run(*root, *list, *out); err != nil {
		fmt.Fprintln(os.Stderr, "evidence:", err)
		os.Exit(1)
	}
}

func run(root, listPath, out string) error {
	b, err := os.ReadFile(listPath)
	if err != nil {
		return err
	}
	var list runList
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	u, _ := user.Current()
	host, _ := os.Hostname()
	var doc strings.Builder
	doc.WriteString("# " + list.Title + "\n\n" + strings.Join(list.Intro, "\n\n") + "\n\n")
	var rows []string
	failed := false
	for _, spec := range list.Runs {
		if strings.Contains(spec.Label, "_") {
			return fmt.Errorf("%s: label has an underscore", spec.Label)
		}
		p := filepath.Join(root, spec.Path)
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		r, errs := qualify.ValidateRun(raw, spec.Rows)
		// The whole run output directory (run.json and artifacts) must be sanitised.
		hits, err := qualify.ScanOut(filepath.Dir(p), u.Username, home, host)
		if err != nil {
			return err
		}
		for _, h := range hits {
			errs = append(errs, fmt.Errorf("unsanitised content in %s", strings.TrimPrefix(h, root)))
		}
		if len(errs) > 0 {
			failed = true
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "REFUSED %s: %v\n", spec.Path, e)
			}
			continue
		}
		sum := sha256.Sum256(raw)
		doc.WriteString(qualify.Section(spec.Label, r, hex.EncodeToString(sum[:]), spec.Notes))
		if !spec.Rows {
			continue
		}
		heading := qualify.EvidenceHeading(spec.Label, r)
		link := fmt.Sprintf("[%s](qualification/%s#%s)", heading, filepath.Base(out), qualify.EvidenceAnchor(heading))
		rs, err := qualify.Rows(r, link)
		if err != nil {
			return fmt.Errorf("%s: %w", spec.Path, err)
		}
		for _, row := range rs {
			rows = append(rows, row.Line("?"))
		}
	}
	if failed {
		return fmt.Errorf("invalid runs; nothing written")
	}
	text := doc.String()
	// The generated file itself must hold no machine path, username or hostname.
	for _, f := range []string{home, host} {
		if len(f) >= 3 && strings.Contains(text, f) {
			return fmt.Errorf("generated text contains %q", f)
		}
	}
	if err := os.WriteFile(out, []byte(text), 0o644); err != nil {
		return err
	}
	fmt.Println(strings.Join(rows, "\n"))
	return nil
}
