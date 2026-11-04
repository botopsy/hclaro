// This helper is a synthetic CLI, not Terraform. Never install it on a normal PATH.
// HCLARO_FAKE_PLAN selects plan behavior: unset (success), "lock", "slow" or "fail".
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
		plan(strings.TrimPrefix(args[3], "-out="))
	default:
		refuse()
	}
}

func refuse() {
	fmt.Fprintln(os.Stderr, "synthetic CLI refuses this command")
	os.Exit(1)
}

func plan(out string) {
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
	if err := os.WriteFile(out, fixtures.PlanAllActions, 0o600); err != nil {
		os.Exit(1)
	}
}
