// This helper is a synthetic AWS CLI, not the AWS CLI. Never install it on a normal PATH.
// It answers only the read-only calls Hclaro's discovery makes, from the recorded synthetic
// fixtures in fixtures/aws, and never contacts AWS.
//
// HCLARO_FAKE_AWS selects behavior: unset (success), "v1" (reports AWS CLI 1.x), "expired"
// (every call fails like an expired SSO session), "denied" (RDS reads are denied),
// "throttle" (every list call is throttled) and "slow" (list calls wait 3 seconds).
// HCLARO_FAKE_AWS_ACCOUNT replaces the account STS reports.
//
// Authentication (M20): `configure list-profiles` lists dev, prod and sso-dev (plus lines
// that are not profile names); `configure get sso_session|sso_start_url --profile sso-dev`
// succeeds, other settings and profiles exit 1. `sso login` and `login` honour
// HCLARO_FAKE_AWS_LOGIN: unset or "ok" (prints a canary device code and succeeds), "fail",
// "hang" (waits 30 seconds) and "flood" (prints 512 KiB and succeeds; `configure
// list-profiles` then prints 20000 profile names, about 400 KiB). `login help` succeeds only with HCLARO_FAKE_AWS_LOGIN_CMD=1.
// HCLARO_FAKE_AWS_SPY, when set, receives each argv as one NUL-separated line, prefixed
// with AWS_PROFILE=<value>, KEYS=<whether AWS_ACCESS_KEY_ID is set> and SET=<the names in
// credentialVars that are set, comma-separated> so tests can check the composed environment.
package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/discovery/discoverytest"
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

// canary is planted in every output Hclaro must never pass on.
const canary = "DO_NOT_LEAK_HCLARO_TEST_SECRET AKIA_TEST_CANARY SESSION_TOKEN_TEST_CANARY"

// credentialVars are inherited credential sources the spy reports by name (never value).
var credentialVars = []string{"AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_DEFAULT_PROFILE", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE",
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_AUTHORIZATION_TOKEN",
	"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", "AWS_VAULT"}

func spy(args []string) {
	path := os.Getenv("HCLARO_FAKE_AWS_SPY")
	if path == "" {
		return
	}
	if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_, keys := os.LookupEnv("AWS_ACCESS_KEY_ID")
		set := []string{}
		for _, name := range credentialVars {
			if _, ok := os.LookupEnv(name); ok {
				set = append(set, name)
			}
		}
		fmt.Fprintf(f, "AWS_PROFILE=%s\x00KEYS=%t\x00SET=%s\x00%s\n", os.Getenv("AWS_PROFILE"), keys, strings.Join(set, ","), strings.Join(args, "\x00"))
		f.Close()
	}
}

// auth answers the M20 authentication commands; it reports false for other commands.
func auth(args []string) bool {
	op := strings.Join(args[:min(2, len(args))], " ")
	login := os.Getenv("HCLARO_FAKE_AWS_LOGIN")
	switch {
	case op == "configure list-profiles" && login == "flood":
		for i := range 20000 {
			fmt.Printf("generated-profile-%05d\n", i)
		}
	case op == "configure list-profiles":
		fmt.Print("dev\nprod\nsso-dev\n" + canary + "\n--endpoint-url\n")
	case op == "configure get":
		if len(args) == 5 && (args[2] == "sso_session" || args[2] == "sso_start_url") && args[3] == "--profile" && args[4] == "sso-dev" {
			fmt.Println("https://synthetic.awsapps.example/start " + canary)
			return true
		}
		os.Exit(1)
	case op == "login help":
		if os.Getenv("HCLARO_FAKE_AWS_LOGIN_CMD") != "1" {
			fail("aws: error: argument command: Invalid choice, valid choices are: ...", 252)
		}
		fmt.Println("LOGIN()")
	case op == "sso login", len(args) >= 1 && args[0] == "login":
		if args[0] == "login" && os.Getenv("HCLARO_FAKE_AWS_LOGIN_CMD") != "1" {
			fail("aws: error: argument command: Invalid choice", 252)
		}
		fmt.Println("Attempting to automatically open the SSO authorization page in your default browser.")
		fmt.Println("https://device.sso.example/?user_code=" + canary)
		switch login {
		case "fail":
			fail("Failed to open browser "+canary, 255)
		case "hang":
			time.Sleep(30 * time.Second)
		case "flood":
			fmt.Print(strings.Repeat("x", 512<<10))
		}
		fmt.Println("Successfully logged into Start URL")
	default:
		return false
	}
	return true
}

func main() {
	args := os.Args[1:]
	spy(args)
	if auth(args) {
		return
	}
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
	if mode == "v1" && slices.Contains(args, "--no-cli-pager") {
		fail("Unknown options: --no-cli-pager", 252)
	}
	if mode == "expired" {
		fail("Error when retrieving token from sso: Token has expired and refresh failed "+canary, 255)
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
			role := os.Getenv("HCLARO_FAKE_AWS_ROLE")
			if role == "" {
				role = "Other"
			}
			fmt.Printf(`{"UserId":"AROASYNTHETIC:%s","Account":%q,"Arn":"arn:aws:sts::%s:assumed-role/%s/demo"}`+"\n", canary, account, account, role)
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
		out, ok, _ := discoverytest.More(args)
		if !ok {
			fail("synthetic AWS CLI: operation not supported", 252)
		}
		os.Stdout.Write(out)
	}
}
