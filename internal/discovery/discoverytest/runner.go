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
	"hclaro/internal/awscli"
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
	if !awscli.Allowed(args) {
		return nil, &awscli.CallError{Kind: awscli.KindRefused}
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
	if out, ok, err := More(args); ok {
		return out, err
	}
	return nil, &awscli.CallError{Kind: awscli.KindFailed}
}

// More answers the later catalog types (EC2 compute and networking, load balancers, IAM
// roles, Lambda, DynamoDB) from fixtures. Regional types answer in eu-west-1 only; IAM is
// global and answers once. ok is false for any other operation.
func More(args []string) (out []byte, ok bool, err error) {
	f, ok := moreFixtures[args[0]+" "+args[1]]
	if !ok {
		return nil, false, nil
	}
	if option(args, "--region") != "eu-west-1" && !f.global {
		return []byte(f.empty), true, nil
	}
	out, err = page(f.file)
	return out, true, err
}

// moreFixtures maps the later catalog types to their fixture and the empty page other
// regions return.
var moreFixtures = map[string]struct {
	file, empty string
	global      bool
}{
	"ec2 describe-instances":            {"ec2-describe-instances.json", `{"Reservations":[]}`, false},
	"ec2 describe-volumes":              {"ec2-describe-volumes.json", `{"Volumes":[]}`, false},
	"ec2 describe-internet-gateways":    {"ec2-describe-internet-gateways.json", `{"InternetGateways":[]}`, false},
	"ec2 describe-route-tables":         {"ec2-describe-route-tables.json", `{"RouteTables":[]}`, false},
	"ec2 describe-nat-gateways":         {"ec2-describe-nat-gateways.json", `{"NatGateways":[]}`, false},
	"elbv2 describe-load-balancers":     {"elbv2-describe-load-balancers.json", `{"LoadBalancers":[]}`, false},
	"lambda list-functions":             {"lambda-list-functions.json", `{"Functions":[]}`, false},
	"dynamodb list-tables":              {"dynamodb-list-tables.json", `{"TableNames":[]}`, false},
	"sqs list-queues":                   {"sqs-list-queues.json", `{"QueueUrls":[]}`, false},
	"sns list-topics":                   {"sns-list-topics.json", `{"Topics":[]}`, false},
	"kms list-aliases":                  {"kms-list-aliases.json", `{"Aliases":[]}`, false},
	"logs describe-log-groups":          {"logs-describe-log-groups.json", `{"logGroups":[]}`, false},
	"ecr describe-repositories":         {"ecr-describe-repositories.json", `{"repositories":[]}`, false},
	"eks list-clusters":                 {"eks-list-clusters.json", `{"clusters":[]}`, false},
	"ecs list-clusters":                 {"ecs-list-clusters.json", `{"clusterArns":[]}`, false},
	"route53 list-hosted-zones":         {"route53-list-hosted-zones.json", `{"HostedZones":[]}`, true},
	"acm list-certificates":             {"acm-list-certificates.json", `{"CertificateSummaryList":[]}`, false},
	"secretsmanager list-secrets":       {"secretsmanager-list-secrets.json", `{"SecretList":[]}`, false},
	"ssm describe-parameters":           {"ssm-describe-parameters.json", `{"Parameters":[]}`, false},
	"stepfunctions list-state-machines": {"stepfunctions-list-state-machines.json", `{"stateMachines":[]}`, false},
	"iam list-roles":                    {"iam-list-roles.json", `{"Roles":[]}`, true},
}
