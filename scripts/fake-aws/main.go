// This helper is a synthetic AWS CLI, not the AWS CLI. Never install it on a normal PATH.
// It answers only the read-only calls Hclaro's discovery makes, from the recorded synthetic
// fixtures in fixtures/aws, and never contacts AWS.
//
// HCLARO_FAKE_AWS selects behavior: unset (success), "v1" (reports AWS CLI 1.x), "expired"
// (every call fails like an expired SSO session), "denied" (RDS reads are denied),
// "throttle" (every list call is throttled) and "slow" (list calls wait 3 seconds).
// HCLARO_FAKE_AWS_ACCOUNT replaces the account STS reports.
package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"hclaro/fixtures"
)

func fail(msg string, code int) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(code)
}

func value(args []string, name string) string {
	if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func page(name string) {
	b, err := fixtures.AWS.ReadFile("aws/" + name)
	if err != nil {
		fail("fixture missing", 255)
	}
	os.Stdout.Write(b)
}

func main() {
	args := os.Args[1:]
	mode := os.Getenv("HCLARO_FAKE_AWS")
	if len(args) == 1 && args[0] == "--version" {
		if mode == "v1" {
			fmt.Fprintln(os.Stderr, "aws-cli/1.29.0 Python/3.11.4 Darwin/23.0.0 botocore/1.31.0")
			return
		}
		fmt.Println("aws-cli/2.17.0 Python/3.12.6 Darwin/23.0.0 exe/x86_64 (synthetic)")
		return
	}
	if len(args) < 2 {
		fail("usage: aws <service> <operation>", 252)
	}
	if mode == "expired" {
		fail("Error when retrieving token from sso: Token has expired and refresh failed", 255)
	}
	region := value(args, "--region")
	op := args[0] + " " + args[1]
	if op != "sts get-caller-identity" {
		switch mode {
		case "throttle":
			fail("An error occurred (Throttling) when calling the operation: Rate exceeded", 254)
		case "slow":
			time.Sleep(3 * time.Second)
		}
	}
	switch op {
	case "sts get-caller-identity":
		if account := os.Getenv("HCLARO_FAKE_AWS_ACCOUNT"); account != "" {
			fmt.Printf(`{"UserId":"AROASYNTHETIC:demo","Account":%q,"Arn":"arn:aws:sts::%s:assumed-role/Other/demo"}`+"\n", account, account)
			return
		}
		page("sts-identity.json")
	case "ec2 describe-vpcs":
		if region != "eu-west-1" {
			fmt.Println(`{"Vpcs": []}`)
		} else if strings.HasPrefix(value(args, "--starting-token"), "eyJ") {
			page("ec2-describe-vpcs-2.json")
		} else {
			page("ec2-describe-vpcs-1.json")
		}
	case "ec2 describe-subnets":
		if region != "eu-west-1" {
			fmt.Println(`{"Subnets": []}`)
			return
		}
		page("ec2-describe-subnets.json")
	case "ec2 describe-security-groups":
		if region != "eu-west-1" {
			fmt.Println(`{"SecurityGroups": []}`)
			return
		}
		page("ec2-describe-security-groups.json")
	case "s3api list-buckets":
		page("s3api-list-buckets.json")
	case "rds describe-db-instances":
		if mode == "denied" {
			fail("An error occurred (AccessDenied) when calling the DescribeDBInstances operation: User is not authorized to perform: rds:DescribeDBInstances", 254)
		}
		if region != "eu-west-1" {
			fmt.Println(`{"DBInstances": []}`)
			return
		}
		page("rds-describe-db-instances.json")
	default:
		fail("synthetic AWS CLI: operation not supported", 252)
	}
}
