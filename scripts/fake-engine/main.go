// This helper is a synthetic CLI, not Terraform. Never install it on a normal PATH.
// HCLARO_FAKE_PLAN selects plan behavior: unset (success), "lock", "slow" or "fail". A project
// that declares an import block gets the import plan fixture; -generate-config-out writes
// the generated configuration fixture.
// HCLARO_FAKE_APPLY selects apply behavior: unset (success), "stale" or "fail".
// HCLARO_FAKE_SPY, when set, names a file that receives each invocation's argv, one
// NUL-separated line per call, so tests can assert exact flags.
package main

import (
	"bytes"
	"fmt"
	"hclaro/fixtures"
	"os"
	"os/signal"
	"strings"
	"time"
)

func main() {
	args := os.Args[1:]
	if spy := os.Getenv("HCLARO_FAKE_SPY"); spy != "" {
		if f, err := os.OpenFile(spy, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.WriteString(strings.Join(args, "\x00") + "\n")
			_ = f.Close()
		}
	}
	switch {
	case strings.Join(args, " ") == "version -json":
		fmt.Println(`{"terraform_version":"1.9.8"}`)
	case strings.Join(args, " ") == "workspace show":
		fmt.Println("demo")
	case strings.Join(args, " ") == "show -json":
		_, _ = os.Stdout.Write(fixtures.State)
	case len(args) == 3 && args[0] == "show" && args[1] == "-json":
		b, err := os.ReadFile(args[2])
		if err != nil || !bytes.HasPrefix(b, []byte("{")) {
			refuse()
		}
		_, _ = os.Stdout.Write(b)
	case len(args) >= 4 && strings.Join(args[:3], " ") == "plan -input=false -json" && strings.HasPrefix(args[3], "-out="):
		generate := ""
		for _, a := range args[4:] {
			switch {
			case strings.HasPrefix(a, "-generate-config-out="):
				generate = strings.TrimPrefix(a, "-generate-config-out=")
			case !strings.HasPrefix(a, "-var-file=") && !strings.HasPrefix(a, "-replace="):
				refuse()
			}
		}
		plan(strings.TrimPrefix(args[3], "-out="), generate)
	case len(args) == 4 && strings.Join(args[:3], " ") == "apply -input=false -json":
		apply(args[3])
	default:
		refuse()
	}
}

func refuse() {
	fmt.Fprintln(os.Stderr, "synthetic CLI refuses this command")
	os.Exit(1)
}

// plan writes a synthetic saved plan. When the working directory declares an import block it
// writes the import fixture; with -generate-config-out it writes the generated configuration
// fixture to that new file, refusing to overwrite one, as the real engines do.
func plan(out, generate string) {
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)
	log := fixtures.PlanLog
	if os.Getenv("HCLARO_FAKE_PLAN") == "lock" {
		log = fixtures.PlanLogLocked
	}
	delay := 150 * time.Millisecond
	if os.Getenv("HCLARO_FAKE_PLAN") == "slow" {
		delay = 3 * time.Second
	}
	for _, line := range bytes.Split(bytes.TrimSpace(log), []byte("\n")) {
		_, _ = os.Stdout.Write(append(line, '\n'))
		select {
		case <-interrupted:
			fmt.Println(`{"type":"log","@level":"warn","@message":"Interrupt received. Stopping gracefully."}`)
			os.Exit(1)
		case <-time.After(delay):
		}
	}
	switch os.Getenv("HCLARO_FAKE_PLAN") {
	case "lock", "fail":
		os.Exit(1)
	}
	document := fixtures.PlanAllActions
	if declaresImport() {
		document = fixtures.PlanImport
	}
	if generate != "" {
		if strings.ContainsAny(generate, `/\`) {
			refuse()
		}
		f, err := os.OpenFile(generate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			fmt.Println(`{"type":"diagnostic","@level":"error","diagnostic":{"severity":"error","summary":"Target generated file already exists"}}`)
			os.Exit(1)
		}
		_, _ = f.Write(fixtures.ImportGenerated)
		_ = f.Close()
	}
	if err := os.WriteFile(out, document, 0o600); err != nil {
		os.Exit(1)
	}
}

// apply accepts only a saved plan file this helper wrote. It never touches anything else.
func apply(path string) {
	b, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(b, []byte("{")) {
		refuse()
	}
	// Like the real engines, a stale saved plan is refused before anything is applied.
	if os.Getenv("HCLARO_FAKE_APPLY") == "stale" {
		fmt.Println(`{"type":"diagnostic","@level":"error","diagnostic":{"severity":"error","summary":"Saved plan is stale","detail":"The given plan file can no longer be applied because the state was changed by another operation after the plan was created."}}`)
		os.Exit(1)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(fixtures.ApplyLog), []byte("\n")) {
		_, _ = os.Stdout.Write(append(line, '\n'))
		time.Sleep(150 * time.Millisecond)
	}
	if os.Getenv("HCLARO_FAKE_APPLY") == "fail" {
		os.Exit(1)
	}
}

func declaresImport() bool {
	entries, err := os.ReadDir(".")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".tf") {
			continue
		}
		if b, err := os.ReadFile(e.Name()); err == nil && bytes.Contains(b, []byte("import {")) {
			return true
		}
	}
	return false
}
