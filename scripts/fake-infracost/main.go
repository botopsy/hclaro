// This helper is a synthetic Infracost CLI, not Infracost. Never install it on a normal PATH.
// It answers only the calls Hclaro's cost estimation makes, from the synthetic report in
// fixtures/cost, and never contacts a pricing API.
//
// Hclaro passes the adapter only Infracost's own settings, so the behavior is selected by the
// host of INFRACOST_PRICING_API_ENDPOINT: unset or any other host (success), "old.invalid"
// (reports v0.9.24), "auth.invalid" (no API key), "offline.invalid" (pricing API unreachable)
// and "slow.invalid" (waits 5 seconds). The input file is refused when it contains the test
// canary, so a live run proves it was minimized.
package main

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"hclaro/fixtures"
)

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

func main() {
	args := os.Args[1:]
	mode := ""
	if u, err := url.Parse(os.Getenv("INFRACOST_PRICING_API_ENDPOINT")); err == nil {
		mode = strings.TrimSuffix(u.Hostname(), ".invalid")
	}
	if len(args) == 1 && args[0] == "--version" {
		if mode == "old" {
			fmt.Println("Infracost v0.9.24")
			return
		}
		fmt.Println("Infracost v0.10.39")
		return
	}
	if len(args) < 3 || args[0] != "breakdown" || args[1] != "--path" {
		fail("synthetic Infracost: command not supported")
	}
	input, err := os.ReadFile(args[2])
	if err != nil {
		fail("synthetic Infracost: input missing")
	}
	if bytes.Contains(input, []byte("DO_NOT_LEAK")) {
		fail("synthetic Infracost: the input holds the test canary")
	}
	switch mode {
	case "auth":
		fail("Error: No INFRACOST_API_KEY environment variable is set. Run infracost auth login")
	case "offline":
		fail(`Error: Post "https://pricing.api.infracost.io/graphql": dial tcp: lookup pricing.api.infracost.io: no such host`)
	case "slow":
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt)
		select {
		case <-stop:
			os.Exit(1)
		case <-time.After(5 * time.Second):
		}
	}
	os.Stdout.Write(fixtures.CostInfracost)
}
