// This helper is a synthetic CLI, not Terraform. Never install it on a normal PATH.
// HCLARO_FAKE_PLAN selects plan behavior: unset (success), "lock", "slow" or "fail". A project
// that declares an import block gets the import plan fixture; -generate-config-out writes
// the generated configuration fixture.
// HCLARO_FAKE_APPLY selects apply behavior: unset (success), "stale" or "fail".
// HCLARO_FAKE_INIT selects init behavior: unset (success: creates the data directory and a
// lock file), "migrate", "prompt", "credentials", "registry", "slow" (waits for an
// interrupt) or "leak" (echoes a canary and AWS_SECRET_ACCESS_KEY, then succeeds). Only
// `init -input=false -no-color` and `init -input=false -json` are accepted.
// HCLARO_FAKE_SPY, when set, names a file that receives each invocation's argv, one
// NUL-separated line per call, so tests can assert exact flags. With HCLARO_FAKE_SPY_ENV
// also set, the line is prefixed with "TF_WORKSPACE=<value or unset>" and a NUL.
//
// Workspaces: a ".fake-workspaces" file in the working directory holds the exact
// `workspace list` output (the "* " line is the CLI default); without one the list is
// "default" and "demo" with "demo" selected. TF_WORKSPACE overrides the selection for every
// command, like the real engines, and `show -json` prints ".fake-state/<workspace>.json"
// when that file exists. `workspace new NAME`, `workspace select NAME` and
// `workspace delete NAME` behave like the real engines: new and select refuse to run while
// TF_WORKSPACE is set, new creates an empty state and selects it, delete refuses the default,
// the selected and a non-empty workspace. New and select also write "environment" in the data
// directory. Any extra flag, including -force, is refused.
// HCLARO_FAKE_WORKSPACE selects failures: "unsupported" (backend without workspaces),
// "locked", "credentials", "slow" (waits for an interrupt after printing a line), "leak"
// (echoes a canary and AWS_SECRET_ACCESS_KEY, then succeeds) and "list-fails-after" (succeeds,
// then every later `workspace list` fails).
// Invoked as "tofu", it reports an OpenTofu version.
package main

import (
	"bytes"
	"fmt"
	"hclaro/fixtures"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	args := os.Args[1:]
	if spy := os.Getenv("HCLARO_FAKE_SPY"); spy != "" {
		if f, err := os.OpenFile(spy, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			line := strings.Join(args, "\x00")
			if os.Getenv("HCLARO_FAKE_SPY_ENV") != "" {
				ws, ok := os.LookupEnv("TF_WORKSPACE")
				if !ok {
					ws = "<unset>"
				}
				line = "TF_WORKSPACE=" + ws + "\x00" + line
			}
			_, _ = f.WriteString(line + "\n")
			_ = f.Close()
		}
	}
	switch {
	case strings.Join(args, " ") == "version -json":
		if strings.HasPrefix(filepath.Base(os.Args[0]), "tofu") {
			fmt.Println(`{"terraform_version":"1.8.3"}`)
		} else {
			fmt.Println(`{"terraform_version":"1.9.8"}`)
		}
	case strings.Join(args, " ") == "workspace list":
		if _, err := os.Stat(".fake-list-broken"); err == nil {
			fmt.Fprintln(os.Stderr, "Error: failed to list workspaces")
			os.Exit(1)
		}
		_, _ = os.Stdout.Write(workspaceList())
	case len(args) == 3 && args[0] == "workspace" && (args[1] == "new" || args[1] == "select" || args[1] == "delete"):
		changeWorkspace(args[1], args[2])
	case strings.Join(args, " ") == "workspace show":
		fmt.Println(selectedWorkspace())
	case strings.Join(args, " ") == "show -json":
		ws := selectedWorkspace()
		if b, err := os.ReadFile(filepath.Join(".fake-state", filepath.Base(ws)+".json")); err == nil {
			_, _ = os.Stdout.Write(b)
		} else {
			_, _ = os.Stdout.Write(fixtures.State)
		}
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
	case strings.Join(args, " ") == "init -input=false -no-color":
		initialize(false)
	case strings.Join(args, " ") == "init -input=false -json":
		initialize(true)
	default:
		refuse()
	}
}

func workspaceList() []byte {
	if b, err := os.ReadFile(".fake-workspaces"); err == nil {
		return b
	}
	return []byte("  default\n* demo\n")
}

// selectedWorkspace is TF_WORKSPACE when set, otherwise the CLI default from the list.
func selectedWorkspace() string {
	if ws := os.Getenv("TF_WORKSPACE"); ws != "" {
		return ws
	}
	for _, line := range strings.Split(string(workspaceList()), "\n") {
		if strings.HasPrefix(line, "* ") {
			return strings.TrimSpace(line[2:])
		}
	}
	return "default"
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

// initialize emulates a standard init. Human output goes to stdout and errors to stderr, as
// with the real engines; with -json every line is a machine-readable UI message.
func initialize(json bool) {
	say := func(text string) {
		if json {
			fmt.Printf("{\"type\":\"init_output\",\"@level\":\"info\",\"@message\":%q}\n", text)
		} else {
			fmt.Println(text)
		}
	}
	fail := func(summary, detail string) {
		if json {
			fmt.Printf("{\"type\":\"diagnostic\",\"@level\":\"error\",\"diagnostic\":{\"severity\":\"error\",\"summary\":%q,\"detail\":%q}}\n", summary, detail)
		} else {
			fmt.Fprintf(os.Stderr, "\u2577\n\u2502 Error: %s\n\u2502 \n\u2502 %s\n\u2575\n", summary, detail)
		}
		os.Exit(1)
	}
	if json {
		fmt.Println(`{"type":"version","@level":"info","terraform":"1.9.8","ui":"1.2"}`)
	}
	say("Initializing the backend...")
	switch os.Getenv("HCLARO_FAKE_INIT") {
	case "migrate":
		fail("Backend configuration changed", "A change in the backend configuration has been detected, which may require migrating existing state. If you wish to attempt automatic migration of the state, use \"init -migrate-state\". If you wish to store the current configuration with no changes to the state, use \"init -reconfigure\".")
	case "prompt":
		fail("No value for required variable", "The root module input variable \"region\" is not set, and input is disabled.")
	case "credentials":
		fail("No valid credential sources found", "Please see the provider documentation for authentication.")
	case "registry":
		fail("Failed to query available provider packages", "Could not retrieve the list of available versions for provider hashicorp/null.")
	case "leak":
		say("Backend password=DO_NOT_LEAK_HCLARO_TEST_SECRET")
		say("Using key " + os.Getenv("AWS_SECRET_ACCESS_KEY") + " for the backend")
	case "slow":
		interrupted := make(chan os.Signal, 1)
		signal.Notify(interrupted, os.Interrupt)
		say("Initializing provider plugins...")
		select {
		case <-interrupted:
			fail("Interrupted", "Init was interrupted.")
		case <-time.After(time.Minute):
		}
	}
	say("Initializing provider plugins...")
	say("- Installing hashicorp/null v3.2.2...")
	data := os.Getenv("TF_DATA_DIR")
	if data == "" {
		data = ".terraform"
	}
	if err := os.MkdirAll(data, 0o755); err != nil {
		fail("Failed to create the data directory", "")
	}
	if _, err := os.Stat(".terraform.lock.hcl"); err != nil {
		_ = os.WriteFile(".terraform.lock.hcl", []byte("# Synthetic lock file written by the fake engine.\n"), 0o644)
		say("Terraform has created a lock file .terraform.lock.hcl.")
	}
	say("Terraform has been successfully initialized!")
}

// changeWorkspace emulates `workspace new|select|delete NAME`.
func changeWorkspace(action, name string) {
	fail := func(text string) {
		fmt.Fprintf(os.Stderr, "\u2577\n\u2502 Error: %s\n\u2575\n", text)
		os.Exit(1)
	}
	if strings.HasPrefix(name, "-") || strings.ContainsAny(name, "/\\. ") {
		refuse()
	}
	if os.Getenv("TF_WORKSPACE") != "" && action != "delete" {
		fail("Workspace overridden: the selected workspace is overridden using the TF_WORKSPACE environment variable.")
	}
	switch os.Getenv("HCLARO_FAKE_WORKSPACE") {
	case "unsupported":
		fail("workspaces not supported")
	case "locked":
		fail("Error acquiring the state lock")
	case "credentials":
		fail("No valid credential sources found")
	case "leak":
		fmt.Println("Backend password=DO_NOT_LEAK_HCLARO_TEST_SECRET")
		fmt.Println("Using key " + os.Getenv("AWS_SECRET_ACCESS_KEY"))
	case "slow":
		interrupted := make(chan os.Signal, 1)
		signal.Notify(interrupted, os.Interrupt)
		fmt.Println("Acquiring state lock. This may take a few moments...")
		select {
		case <-interrupted:
			fail("Interrupted")
		case <-time.After(time.Minute):
		}
	}
	names, current := []string{}, ""
	for _, line := range strings.Split(string(workspaceList()), "\n") {
		n := strings.TrimSpace(strings.TrimPrefix(line, "* "))
		if n == "" {
			continue
		}
		if strings.HasPrefix(line, "* ") {
			current = n
		}
		names = append(names, n)
	}
	exists := false
	for _, n := range names {
		exists = exists || n == name
	}
	switch action {
	case "new":
		if exists {
			fail(fmt.Sprintf("Workspace %q already exists", name))
		}
		names, current = append(names, name), name
		_ = os.MkdirAll(".fake-state", 0o755)
		_ = os.WriteFile(filepath.Join(".fake-state", name+".json"), []byte(`{"format_version":"1.0"}`+"\n"), 0o644)
		fmt.Printf("Created and switched to workspace %q!\n\nYou're now on a new, empty workspace.\n", name)
	case "select":
		if !exists {
			fail(fmt.Sprintf("Workspace %q doesn't exist.", name))
		}
		current = name
		fmt.Printf("Switched to workspace %q.\n", name)
	case "delete":
		switch {
		case !exists:
			fail(fmt.Sprintf("Workspace %q doesn't exist.", name))
		case name == "default":
			fail("Cannot delete the default workspace")
		case name == current:
			fail(fmt.Sprintf("Workspace %q is your active workspace.", name))
		}
		if b, err := os.ReadFile(filepath.Join(".fake-state", name+".json")); err == nil && bytes.Contains(b, []byte(`"resources"`)) {
			fail(fmt.Sprintf("Workspace %q is currently tracking the following resource instances", name))
		}
		kept := []string{}
		for _, n := range names {
			if n != name {
				kept = append(kept, n)
			}
		}
		names = kept
		_ = os.Remove(filepath.Join(".fake-state", name+".json"))
		fmt.Printf("Deleted workspace %q!\n", name)
	}
	list := ""
	for _, n := range names {
		mark := "  "
		if n == current {
			mark = "* "
		}
		list += mark + n + "\n"
	}
	if err := os.WriteFile(".fake-workspaces", []byte(list), 0o644); err != nil {
		fail("could not write workspaces")
	}
	if action != "delete" {
		data := os.Getenv("TF_DATA_DIR")
		if data == "" {
			data = ".terraform"
		}
		_ = os.MkdirAll(data, 0o755)
		_ = os.WriteFile(filepath.Join(data, "environment"), []byte(current), 0o644)
	}
	if os.Getenv("HCLARO_FAKE_WORKSPACE") == "list-fails-after" {
		_ = os.WriteFile(".fake-list-broken", nil, 0o644)
	}
}
