package awscli

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestAllowedRefusesEverythingButReadOnlyCalls(t *testing.T) {
	ok := [][]string{
		{"--version"},
		{"sts", "get-caller-identity", "--output", "json", "--region", "eu-west-1"},
		{"ec2", "describe-vpcs", "--output", "json", "--region", "us-east-1", "--max-items", "100", "--starting-token", "eyJhIjoxfQ=="},
		{"s3api", "list-buckets", "--output", "json"},
		{"rds", "describe-db-instances", "--region", "ap-southeast-2"},
	}
	for _, args := range ok {
		if !Allowed(args) {
			t.Errorf("refused %v", args)
		}
	}
	refused := [][]string{
		{},
		{"ec2", "create-vpc"},
		{"ec2", "delete-vpc", "--vpc-id", "vpc-1"},
		{"ec2", "modify-vpc-attribute"},
		{"ec2", "run-instances"},
		{"s3api", "delete-bucket"},
		{"s3api", "put-bucket-policy"},
		{"s3", "rm", "s3://x"},
		{"rds", "delete-db-instance"},
		{"iam", "list-users"},
		{"configure", "set"},
		{"ec2", "describe-vpcs", "--cli-input-json", "file://x"},
		{"ec2", "describe-vpcs", "--endpoint-url", "http://evil"},
		{"ec2", "describe-vpcs", "--profile", "prod"},
		{"ec2", "describe-vpcs", "--query", "Vpcs"},
		{"ec2", "describe-vpcs", "--region", "eu-west-1; rm -rf /"},
		{"ec2", "describe-vpcs", "--region", "eu-west-1", "--region", "us-east-1"},
		{"ec2", "describe-vpcs", "--max-items", "0"},
		{"ec2", "describe-vpcs", "--output", "text"},
		{"ec2", "describe-vpcs", "--starting-token"},
		{"ec2", "describe-vpcs", "--debug"},
		{"--version", "x"},
	}
	for _, args := range refused {
		if Allowed(args) {
			t.Errorf("allowed %v", args)
		}
	}
	if _, err := (Exec{}).Run(context.Background(), nil, []string{"ec2", "delete-vpc"}); KindOf(err) != KindRefused {
		t.Fatalf("Exec ran a refused command: %v", err)
	}
}

func TestEnvironDisablesPagerPromptsAndRetries(t *testing.T) {
	env := Environ([]string{"AWS_PAGER=less", "aws_cli_auto_prompt=on", "AWS_MAX_ATTEMPTS=10", "AWS_DEFAULT_OUTPUT=table", "AWS_PROFILE=dev", "HOME=/home/x"})
	joined := strings.Join(env, "\n")
	for _, bad := range []string{"less", "=on", "=10", "table"} {
		if strings.Contains(joined, bad) {
			t.Errorf("environment kept %q: %v", bad, env)
		}
	}
	for _, want := range []string{"AWS_PROFILE=dev", "AWS_PAGER=", "AWS_CLI_AUTO_PROMPT=off", "AWS_MAX_ATTEMPTS=1"} {
		if !slices.Contains(env, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"Error when retrieving token from sso: Token has expired and refresh failed":                                KindExpired,
		"An error occurred (ExpiredToken) when calling the GetCallerIdentity operation":                             KindExpired,
		"The config profile (prod) could not be found":                                                              KindProfileNotFound,
		"Unable to locate credentials. You can configure credentials by running \"aws configure\".":                 KindNoCredentials,
		"An error occurred (Throttling) when calling the DescribeVpcs operation: Rate exceeded":                     KindThrottled,
		"An error occurred (RequestLimitExceeded) when calling the DescribeSubnets operation":                       KindThrottled,
		"An error occurred (UnauthorizedOperation) when calling the DescribeVpcs operation: You are not authorized": KindAccessDenied,
		"An error occurred (AccessDenied) when calling the ListBuckets operation: Access Denied":                    KindAccessDenied,
		"An error occurred (InvalidClientTokenId) when calling the GetCallerIdentity operation":                     KindInvalid,
		"An error occurred (AuthFailure) when calling the DescribeVpcs operation: AWS was not able to validate":     KindRegionDisabled,
		"Could not connect to the endpoint URL: \"https://ec2.eu-west-1.amazonaws.com/\"":                           KindNetwork,
		"something else entirely": KindFailed,
	}
	for in, want := range cases {
		if got := Classify(in); got != want {
			t.Errorf("Classify(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	if v, ok := ParseVersion([]byte("aws-cli/2.15.30 Python/3.11.8 Darwin/23.3.0 exe/x86_64 prompt/off\n")); !ok || v != "2.15.30" {
		t.Fatal(v)
	}
	for _, out := range []string{"aws-cli/1.32.0 Python/3.9", "", "not aws"} {
		if _, ok := ParseVersion([]byte(out)); ok {
			t.Errorf("accepted %q", out)
		}
	}
}

func TestParseIdentity(t *testing.T) {
	id := ParseIdentity([]byte(`{"UserId":"AIDAEXAMPLE","Account":"123456789012","Arn":"arn:aws:sts::123456789012:assumed-role/deploy/s1"}`))
	if id.State != "verified" || id.Account != "123456789012" || id.ARN != "arn:aws:sts::123456789012:assumed-role/deploy/s1" {
		t.Fatalf("%+v", id)
	}
	// The user ID is never returned; a mismatched ARN is withheld.
	if id := ParseIdentity([]byte(`{"UserId":"AIDA","Account":"123456789012","Arn":"arn:aws:iam::999999999999:user/x"}`)); id.ARN != "ARN withheld" {
		t.Fatalf("%+v", id)
	}
	for _, bad := range []string{`{}`, `{"Account":"12"}`, `not json`} {
		if id := ParseIdentity([]byte(bad)); id.State != "failed" {
			t.Fatalf("%s: %+v", bad, id)
		}
	}
}

// Sign-in commands disable only the pager and auto-prompt; the user's retry, output and
// file-encoding settings apply as in their terminal.
func TestLocalEnvironKeepsUserSettings(t *testing.T) {
	env := localEnviron([]string{"AWS_PAGER=less", "AWS_CLI_AUTO_PROMPT=on", "AWS_MAX_ATTEMPTS=10", "AWS_DEFAULT_OUTPUT=table", "AWS_CLI_FILE_ENCODING=cp1252"})
	for _, want := range []string{"AWS_MAX_ATTEMPTS=10", "AWS_DEFAULT_OUTPUT=table", "AWS_CLI_FILE_ENCODING=cp1252", "AWS_PAGER=", "AWS_CLI_AUTO_PROMPT=off"} {
		if !slices.Contains(env, want) {
			t.Errorf("missing %s: %v", want, env)
		}
	}
	if slices.Contains(env, "AWS_PAGER=less") || slices.Contains(env, "AWS_CLI_AUTO_PROMPT=on") {
		t.Errorf("kept pager or prompt: %v", env)
	}
}
