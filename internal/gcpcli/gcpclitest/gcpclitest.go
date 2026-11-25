// Package gcpclitest answers allowlisted gcloud calls from the synthetic fixtures in
// fixtures/gcp. Runner is the in-process fake; scripts/fake-gcloud serves the same answers
// as an executable. Neither runs a process that reaches Google Cloud.
package gcpclitest

import (
	"context"
	"slices"
	"strings"
	"sync"

	"hclaro/fixtures"
	"hclaro/internal/awscli"
	"hclaro/internal/gcpcli"
)

// Version is what the fake reports for --version.
const Version = "Google Cloud SDK 480.0.0\nbq 2.1.4\ncore 2024.05.17\ngsutil 5.29\n"

var files = map[string]string{
	"config list": "config.json", "auth list": "auth-list.json", "projects describe": "project.json",
	"compute networks list": "networks.json", "compute networks subnets list": "subnets.json", "compute firewall-rules list": "firewalls.json",
	"compute instances list": "instances.json", "compute disks list": "disks.json", "storage buckets list": "buckets.json", "sql instances list": "sql.json",
}

// Answer returns the fixture for an allowlisted argv, or a refused error. project replaces the
// project `config list` reports. override replaces the fixture of a command ("compute
// instances list").
func Answer(args []string, project string, override map[string][]byte) ([]byte, error) {
	if !gcpcli.Allowed(args) {
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
	if strings.HasPrefix(command, "projects describe ") {
		command = "projects describe"
	}
	if b, ok := override[command]; ok {
		return b, nil
	}
	b, err := fixtures.GCP.ReadFile("gcp/" + files[command])
	if err != nil {
		return nil, &awscli.CallError{Kind: awscli.KindFailed}
	}
	if project != "" && (command == "config list" || command == "projects describe") {
		b = []byte(strings.ReplaceAll(string(b), "hclaro-demo-1", project))
	}
	return b, nil
}

// Runner answers calls from fixtures and records every argv. Project replaces the project
// gcloud reports; Fail, when set, may return an error for a call; Override replaces fixtures.
type Runner struct {
	mu       sync.Mutex
	calls    [][]string
	Project  string
	Fail     func(args []string) error
	Override map[string][]byte
}

// Calls returns every argv the runner received.
func (r *Runner) Calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// SetProject changes the project gcloud reports.
func (r *Runner) SetProject(project string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Project = project
}

func (r *Runner) Run(_ context.Context, _ []string, args []string) ([]byte, error) {
	if !gcpcli.Allowed(args) {
		return nil, &awscli.CallError{Kind: awscli.KindRefused}
	}
	r.mu.Lock()
	r.calls = append(r.calls, slices.Clone(args))
	project, fail, override := r.Project, r.Fail, r.Override
	r.mu.Unlock()
	if fail != nil {
		if err := fail(args); err != nil {
			return nil, err
		}
	}
	return Answer(args, project, override)
}
