// Package azclitest answers allowlisted az calls from the synthetic fixtures in
// fixtures/azure. Runner is the in-process fake; scripts/fake-az serves the same answers as an
// executable. Neither runs a process that reaches Azure.
package azclitest

import (
	"context"
	"slices"
	"strings"
	"sync"

	"hclaro/fixtures"
	"hclaro/internal/awscli"
	"hclaro/internal/azcli"
)

// Version is what the fake reports for --version.
const Version = "azure-cli                         2.60.0\n\ncore                              2.60.0\ntelemetry                          1.1.0\n"

// Subscription is the subscription the fixtures belong to.
const Subscription = "11111111-1111-1111-1111-111111111111"

var files = map[string]string{
	"account show": "account.json", "account list": "accounts.json", "group list": "groups.json", "network vnet list": "vnets.json",
	"network nsg list": "nsgs.json", "vm list": "vms.json", "disk list": "disks.json", "storage account list": "storage.json",
}

// Answer returns the fixture for an allowlisted argv, or a refused error. subscription replaces
// the subscription the fixtures belong to (a `--subscription` flag in args wins over it).
// override replaces the fixture of a command ("vm list").
func Answer(args []string, subscription string, override map[string][]byte) ([]byte, error) {
	if !azcli.Allowed(args) {
		return nil, &awscli.CallError{Kind: awscli.KindRefused}
	}
	if args[0] == "--version" {
		return []byte(Version), nil
	}
	n := 0
	for n < len(args) && !strings.HasPrefix(args[n], "-") {
		n++
	}
	command := strings.Join(args[:n], " ")
	for i, a := range args {
		if a == "--subscription" {
			subscription = args[i+1]
		}
	}
	if b, ok := override[command]; ok {
		return b, nil
	}
	b, err := fixtures.Azure.ReadFile("azure/" + files[command])
	if err != nil {
		return nil, &awscli.CallError{Kind: awscli.KindFailed}
	}
	if subscription != "" {
		b = []byte(strings.ReplaceAll(string(b), Subscription, subscription))
	}
	return b, nil
}

// Runner answers calls from fixtures and records every argv. Subscription replaces the
// subscription az reports; Fail, when set, may return an error for a call; Override replaces
// fixtures.
type Runner struct {
	mu           sync.Mutex
	calls        [][]string
	Subscription string
	Fail         func(args []string) error
	Override     map[string][]byte
}

// Calls returns every argv the runner received.
func (r *Runner) Calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// SetSubscription changes the subscription az reports.
func (r *Runner) SetSubscription(subscription string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Subscription = subscription
}

func (r *Runner) Run(_ context.Context, _ []string, args []string) ([]byte, error) {
	if !azcli.Allowed(args) {
		return nil, &awscli.CallError{Kind: awscli.KindRefused}
	}
	r.mu.Lock()
	r.calls = append(r.calls, slices.Clone(args))
	subscription, fail, override := r.Subscription, r.Fail, r.Override
	r.mu.Unlock()
	if fail != nil {
		if err := fail(args); err != nil {
			return nil, err
		}
	}
	return Answer(args, subscription, override)
}
