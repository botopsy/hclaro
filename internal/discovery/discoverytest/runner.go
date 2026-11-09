// Package discoverytest provides a synthetic AWS CLI runner for tests and the isolated
// browser harness. It answers from the recorded fixtures in fixtures/aws and records every
// argv; it never runs a process or contacts AWS.
package discoverytest

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"hclaro/fixtures"
	"hclaro/internal/discovery"
)

// Runner answers allowlisted calls from fixtures. Account replaces the account STS reports;
// Fail, when set, may return an error for a call (nil falls through to the fixtures).
type Runner struct {
	mu      sync.Mutex
	calls   [][]string
	Account string
	Fail    func(args []string) error
}

// Calls returns every argv the runner received.
func (r *Runner) Calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// SetAccount changes the account STS reports.
func (r *Runner) SetAccount(account string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Account = account
}

func option(args []string, name string) string {
	if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func page(name string) ([]byte, error) { return fixtures.AWS.ReadFile("aws/" + name) }

func (r *Runner) Run(ctx context.Context, environ []string, args []string) ([]byte, error) {
	if !discovery.Allowed(args) {
		return nil, &discovery.CallError{Kind: discovery.KindRefused}
	}
	r.mu.Lock()
	r.calls = append(r.calls, slices.Clone(args))
	account, fail := r.Account, r.Fail
	r.mu.Unlock()
	if fail != nil {
		if err := fail(args); err != nil {
			return nil, err
		}
	}
	if args[0] == "--version" {
		return []byte("aws-cli/2.17.0 Python/3.12.6 Linux/6 exe/x86_64 (synthetic)\n"), nil
	}
	eu := option(args, "--region") == "eu-west-1"
	switch args[0] + " " + args[1] {
	case "sts get-caller-identity":
		if account != "" {
			return []byte(fmt.Sprintf(`{"Account":%q,"Arn":"arn:aws:sts::%s:assumed-role/Other/x"}`, account, account)), nil
		}
		return page("sts-identity.json")
	case "ec2 describe-vpcs":
		switch {
		case !eu:
			return []byte(`{"Vpcs":[]}`), nil
		case option(args, "--starting-token") != "":
			return page("ec2-describe-vpcs-2.json")
		}
		return page("ec2-describe-vpcs-1.json")
	case "ec2 describe-subnets":
		if !eu {
			return []byte(`{"Subnets":[]}`), nil
		}
		return page("ec2-describe-subnets.json")
	case "ec2 describe-security-groups":
		if !eu {
			return []byte(`{"SecurityGroups":[]}`), nil
		}
		return page("ec2-describe-security-groups.json")
	case "s3api list-buckets":
		return page("s3api-list-buckets.json")
	case "rds describe-db-instances":
		if !eu {
			return []byte(`{"DBInstances":[]}`), nil
		}
		return page("rds-describe-db-instances.json")
	}
	return nil, &discovery.CallError{Kind: discovery.KindFailed}
}
