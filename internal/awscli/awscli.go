// Package awscli owns every AWS CLI process Hclaro starts, and every aws-vault process
// outside the engine (engine.CLI wraps its own commands in aws-vault exec). Exec runs the
// read-only Describe/List/Get calls of discovery and the STS identity probe; Local runs the
// sign-in commands of awsauth (tool detection, profile listing, sso login, login). Both take
// argv arrays checked against a fixed allowlist, never a shell, and start the process in one
// place (start) with a composed environment and bounded output. CLI output is read only to
// parse a result or classify a failure; raw output is never returned in an error.
package awscli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"hclaro/internal/awsvault"
	"hclaro/internal/bounded"
)

// Runner runs one read-only AWS CLI call with environ (nil inherits Hclaro's environment)
// and returns its standard output. Errors are *CallError. Tests inject fakes.
type Runner interface {
	Run(ctx context.Context, environ []string, args []string) ([]byte, error)
}

// Error kinds of a failed call. Messages shown to users come from kindMessages; raw CLI
// output is never returned.
const (
	KindMissingCLI       = "cli_missing"
	KindUnsupportedCLI   = "cli_unsupported"
	KindRefused          = "refused"
	KindAccessDenied     = "access_denied"
	KindExpired          = "credentials_expired"
	KindNoCredentials    = "credentials_missing"
	KindInvalid          = "credentials_invalid"
	KindProfileNotFound  = "profile_not_found"
	KindThrottled        = "throttled"
	KindNetwork          = "network"
	KindRegionDisabled   = "region_unavailable"
	KindTimeout          = "timeout"
	KindCancelled        = "cancelled"
	KindMalformed        = "malformed_output"
	KindFailed           = "failed"
	KindIdentityChanged  = "identity_changed"
	KindOutputTooLarge   = "output_too_large"
	KindVaultMissing     = "vault_missing"
	KindVault            = "vault_failed"
	maxOutput            = 8 << 20
	maxStderr            = 16 << 10
	maxLocalOutput       = 256 << 10
	defaultCallTimeout   = 60 * time.Second
	defaultConnectSecond = "10"
	defaultReadSecond    = "30"
)

var kindMessages = map[string]string{
	KindMissingCLI:      "The AWS CLI (aws) was not found on PATH. Install AWS CLI v2 and restart Hclaro.",
	KindUnsupportedCLI:  "Hclaro supports AWS CLI version 2 only. Install AWS CLI v2 and restart Hclaro.",
	KindRefused:         "Hclaro refused to run this AWS CLI command because it is not on the read-only allowlist.",
	KindAccessDenied:    "AWS denied this read (missing IAM permission). Results for this service and region are missing, not empty.",
	KindExpired:         "The AWS credentials or SSO session have expired. Sign in again in your terminal (for example aws sso login), then verify again.",
	KindNoCredentials:   "The AWS CLI found no credentials for this context. Configure the AWS profile in your terminal, then verify again.",
	KindInvalid:         "AWS rejected the credentials (invalid or revoked key, or a clock skew). Check your AWS configuration in your terminal.",
	KindProfileNotFound: "The AWS profile named by this context does not exist in your AWS configuration.",
	KindThrottled:       "AWS throttled the requests and retries were exhausted. Scan fewer regions or types, or try again later.",
	KindNetwork:         "The AWS endpoint was unreachable. Check VPN and network access.",
	KindRegionDisabled:  "This region is not enabled for the account (opt-in region), or the credentials cannot be used in it.",
	KindTimeout:         "The AWS CLI did not answer in time.",
	KindCancelled:       "Cancelled before this read finished.",
	KindMalformed:       "The AWS CLI returned output Hclaro could not read safely. Nothing from it is shown.",
	KindFailed:          "The AWS CLI call failed. Details are withheld because CLI messages can echo configuration.",
	KindIdentityChanged: "The AWS credentials now belong to another account than the one you verified. Verify the AWS context again before scanning or importing.",
	KindOutputTooLarge:  "The AWS CLI returned more output than Hclaro reads for one page.",
	KindVaultMissing:    "This profile uses aws-vault, but aws-vault was not found on PATH. Install aws-vault and restart Hclaro, or choose the AWS CLI method.",
	KindVault:           "aws-vault could not provide credentials. It may need a keychain password, an MFA code or another prompt Hclaro cannot show. Run the aws-vault command shown in a terminal once, then verify again.",
}

// Message returns the user-facing explanation of an error kind.
func Message(kind string) string {
	if m, ok := kindMessages[kind]; ok {
		return m
	}
	return kindMessages[KindFailed]
}

// CallError is a classified, sanitized failure of one CLI call.
type CallError struct{ Kind string }

func (e *CallError) Error() string { return Message(e.Kind) }

// KindOf returns the kind of err, KindFailed for unclassified errors.
func KindOf(err error) string {
	var c *CallError
	if errors.As(err, &c) {
		return c.Kind
	}
	if errors.Is(err, context.Canceled) {
		return KindCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return KindTimeout
	}
	return KindFailed
}

// readOnly lists the only AWS CLI operations Hclaro runs. Every one is a Describe, List or
// Get call; nothing here can create, change or delete a cloud object.
var readOnly = map[string]map[string]bool{
	"sts": {"get-caller-identity": true},
	"ec2": {"describe-vpcs": true, "describe-subnets": true, "describe-security-groups": true, "describe-instances": true,
		"describe-volumes": true, "describe-internet-gateways": true, "describe-route-tables": true, "describe-nat-gateways": true},
	"s3api":          {"list-buckets": true},
	"rds":            {"describe-db-instances": true},
	"elbv2":          {"describe-load-balancers": true},
	"iam":            {"list-roles": true},
	"lambda":         {"list-functions": true},
	"dynamodb":       {"list-tables": true},
	"sqs":            {"list-queues": true},
	"sns":            {"list-topics": true},
	"kms":            {"list-aliases": true},
	"logs":           {"describe-log-groups": true},
	"ecr":            {"describe-repositories": true},
	"eks":            {"list-clusters": true},
	"ecs":            {"list-clusters": true},
	"route53":        {"list-hosted-zones": true},
	"acm":            {"list-certificates": true},
	"secretsmanager": {"list-secrets": true},
	"ssm":            {"describe-parameters": true},
	"stepfunctions":  {"list-state-machines": true},
}

var (
	regionPattern = regexp.MustCompile(`^[a-z]{2}(-gov|-iso[a-z]?)?-[a-z]+-[0-9]{1,2}$`)
	numberPattern = regexp.MustCompile(`^[1-9][0-9]{0,3}$`)
	// tokenPattern bounds an AWS CLI pagination token (base64 of the service token).
	tokenPattern = boundedPattern{regexp.MustCompile(`^[A-Za-z0-9+/=_.-]+$`), 4096}
)

// ValidRegion reports whether s has the shape of an AWS region name.
func ValidRegion(s string) bool { return regionPattern.MatchString(s) }

// ValidToken reports whether s is a pagination token Exec accepts for --starting-token.
func ValidToken(s string) bool { return tokenPattern.MatchString(s) }

// boundedPattern is a pattern with a length limit beyond regexp's repeat limit.
type boundedPattern struct {
	re  *regexp.Regexp
	max int
}

func (p boundedPattern) MatchString(s string) bool { return len(s) <= p.max && p.re.MatchString(s) }

type matcher interface{ MatchString(string) bool }

// valueOptions are the only options Hclaro passes, each with its value pattern; flagOptions
// take no value.
var valueOptions = map[string]matcher{
	"--region":              regionPattern,
	"--output":              regexp.MustCompile(`^json$`),
	"--max-items":           numberPattern,
	"--page-size":           numberPattern,
	"--starting-token":      tokenPattern,
	"--cli-connect-timeout": numberPattern,
	"--cli-read-timeout":    numberPattern,
}
var flagOptions = map[string]bool{"--no-cli-pager": true}

// Allowed reports whether args is `--version` or an allowlisted read-only operation with only
// known options and valid values. Anything else, including create/modify/delete operations,
// --cli-input-json, --endpoint-url, --profile or --query, is refused.
func Allowed(args []string) bool {
	if len(args) == 1 && args[0] == "--version" {
		return true
	}
	if len(args) < 2 || !readOnly[args[0]][args[1]] {
		return false
	}
	seen := map[string]bool{}
	for i := 2; i < len(args); i++ {
		a := args[i]
		if seen[a] {
			return false
		}
		seen[a] = true
		if flagOptions[a] {
			continue
		}
		pattern, ok := valueOptions[a]
		if !ok || i+1 >= len(args) || !pattern.MatchString(args[i+1]) {
			return false
		}
		i++
	}
	return true
}

// Environ returns environ for a read-only AWS CLI call: the pager, auto-prompt and the CLI's
// own retries are disabled (Hclaro retries throttled calls itself), and settings that change
// the output format are dropped. Credential settings are kept: they are the user's credential
// chain, already composed by the execution profile.
func Environ(environ []string) []string {
	return append(localEnviron(environ, "AWS_MAX_ATTEMPTS", "AWS_RETRY_MODE", "AWS_DEFAULT_OUTPUT", "AWS_CLI_FILE_ENCODING"), "AWS_MAX_ATTEMPTS=1", "AWS_RETRY_MODE=standard")
}

// localEnviron is environ (Hclaro's own when nil) without the aws-vault marker, the pager,
// auto-prompt and the extra keys drop, with the pager and auto-prompt disabled. A sign-in
// keeps the user's retry, output and file-encoding settings, as in their terminal.
func localEnviron(environ []string, drop ...string) []string {
	if environ == nil {
		environ = os.Environ()
	}
	drop = append(drop, "AWS_PAGER", "AWS_CLI_AUTO_PROMPT")
	out := make([]string, 0, len(environ)+4)
	for _, item := range awsvault.Strip(environ) {
		key, _, _ := strings.Cut(item, "=")
		if !slices.Contains(drop, strings.ToUpper(key)) {
			out = append(out, item)
		}
	}
	return append(out, "AWS_PAGER=", "AWS_CLI_AUTO_PROMPT=off")
}

// Exec is the real read-only runner: `aws` from PATH, argv only, empty stdin, bounded output.
type Exec struct {
	Timeout time.Duration
	// Observe, when set, is called with each permitted argv before it runs (tests).
	Observe func(args []string)
}

func (e Exec) Run(ctx context.Context, environ []string, args []string) ([]byte, error) {
	if !Allowed(args) {
		return nil, &CallError{KindRefused}
	}
	exe, err := exec.LookPath("aws")
	if err != nil {
		return nil, &CallError{KindMissingCLI}
	}
	// Only an environment composed for an execution profile selects aws-vault.
	vault := ""
	if environ != nil {
		vault, environ = awsvault.Split(environ)
	}
	if e.Observe != nil {
		e.Observe(append([]string(nil), args...))
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = defaultCallTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv := append([]string(nil), args...)
	if args[0] != "--version" {
		// The pager is disabled through AWS_PAGER= (Environ); --no-cli-pager is not passed
		// because AWS CLI v1, which the STS identity probe still supports, rejects it.
		argv = append(argv, "--cli-connect-timeout", defaultConnectSecond, "--cli-read-timeout", defaultReadSecond)
	}
	name := exe
	if args[0] != "--version" {
		if name, argv, err = awsvault.Command(vault, exe, argv); err != nil {
			return nil, &CallError{KindVaultMissing}
		}
	}
	out, errOut, err := start(ctx, name, argv, Environ(environ), maxOutput, false)
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			return nil, &CallError{KindCancelled}
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, &CallError{KindTimeout}
		}
		return nil, &CallError{Classify(errOut.String())}
	}
	if out.Overflow {
		return nil, &CallError{KindOutputTooLarge}
	}
	// AWS CLI v1 prints its version to stderr; v2 to stdout.
	if args[0] == "--version" && out.Len() == 0 {
		return errOut.Bytes(), nil
	}
	return out.Bytes(), nil
}

// ErrRefusedArgs is returned by Local for a command not on the sign-in allowlist; no process
// starts.
var ErrRefusedArgs = errors.New("hclaro refused to run this command because it is not on the allowlist")

// localAllowed is the sign-in allowlist: the exact commands awsauth builds, with a valid
// profile name where one is substituted.
func localAllowed(name string, args []string) bool {
	is := func(want ...string) bool { return slices.Equal(args, want) }
	profile := func(prefix ...string) bool {
		return len(args) == len(prefix)+1 && slices.Equal(args[:len(prefix)], prefix) && awsvault.ValidProfile(args[len(prefix)])
	}
	switch name {
	case "aws-vault":
		return is("--version")
	case "aws":
		return is("--version") || is("login", "help") || is("configure", "list-profiles") ||
			profile("configure", "get", "sso_session", "--profile") || profile("configure", "get", "sso_start_url", "--profile") ||
			profile("sso", "login", "--profile") || profile("login", "--profile")
	}
	return false
}

// Local runs one sign-in-path command (name is aws or aws-vault) and returns bounded stdout
// and stderr: the executable from PATH, argv only, empty stdin, the pager and auto-prompt
// disabled. A command off the allowlist returns ErrRefusedArgs, a missing executable
// exec.ErrNotFound, a failed one its *exec.ExitError, and stdout beyond maxLocalOutput a
// *CallError with KindOutputTooLarge. Cancelling ctx interrupts the process first so the AWS
// CLI can finish writing its SSO cache.
func Local(ctx context.Context, environ []string, name string, args ...string) (stdout, stderr []byte, err error) {
	if !localAllowed(name, args) {
		return nil, nil, ErrRefusedArgs
	}
	exe, err := exec.LookPath(name)
	if err != nil {
		return nil, nil, exec.ErrNotFound
	}
	out, errOut, err := start(ctx, exe, args, localEnviron(environ), maxLocalOutput, true)
	if err == nil && out.Overflow {
		err = &CallError{KindOutputTooLarge}
	}
	return out.Bytes(), errOut.Bytes(), err
}

// start is the only place an AWS CLI or aws-vault process starts. stderr is bounded and
// read only to classify a failure. An interactive process (a sign-in) is interrupted on
// cancel and killed if it has not exited 3 seconds later; Windows has no interrupt signal
// for another process, so there, like every other call, it is killed at once.
func start(ctx context.Context, name string, argv, environ []string, limit int, interactive bool) (out, errOut *bounded.Buffer, err error) {
	cmd := exec.CommandContext(ctx, name, argv...)
	cmd.WaitDelay = time.Second
	if interactive {
		cmd.Cancel = func() error {
			if runtime.GOOS != "windows" && cmd.Process.Signal(os.Interrupt) == nil {
				return nil
			}
			return cmd.Process.Kill()
		}
		cmd.WaitDelay = 3 * time.Second
	}
	cmd.Env = environ
	out, errOut = &bounded.Buffer{Limit: limit}, &bounded.Buffer{Limit: maxStderr}
	cmd.Stdout, cmd.Stderr = out, errOut
	return out, errOut, cmd.Run()
}

// Classify maps AWS CLI error text to a kind. The text itself is discarded by callers.
func Classify(stderr string) string {
	s := strings.ToLower(stderr)
	has := func(parts ...string) bool {
		for _, p := range parts {
			if strings.Contains(s, strings.ToLower(p)) {
				return true
			}
		}
		return false
	}
	switch {
	case has("aws-vault: error"):
		return KindVault
	case has("ExpiredToken", "token has expired", "UnauthorizedSSOTokenError", "Error loading SSO Token", "SSO session", "sso token", "token is expired", "RequestExpired"):
		return KindExpired
	case has("could not be found") && has("profile"):
		return KindProfileNotFound
	case has("Unable to locate credentials", "NoCredentialProviders", "no credentials"):
		return KindNoCredentials
	case has("Throttling", "RequestLimitExceeded", "TooManyRequests", "SlowDown", "Rate exceeded", "RequestThrottled"):
		return KindThrottled
	case has("OptInRequired", "not subscribed", "AuthFailure", "UnrecognizedClientException") && !has("InvalidClientTokenId"):
		return KindRegionDisabled
	case has("InvalidClientTokenId", "SignatureDoesNotMatch", "InvalidAccessKeyId", "IncompleteSignature"):
		return KindInvalid
	case has("AccessDenied", "UnauthorizedOperation", "not authorized to perform", "AuthorizationError", "Forbidden"):
		return KindAccessDenied
	case has("Could not connect to the endpoint", "Connect timeout", "Read timeout", "EndpointConnectionError", "Name or service not known", "nodename nor servname", "connection refused"):
		return KindNetwork
	}
	return KindFailed
}

var cliVersion = regexp.MustCompile(`^aws-cli/([0-9]+)\.([0-9]+)\.([0-9]+)`)

// ParseVersion returns the AWS CLI version from `aws --version` output and whether it is a
// supported major version (2).
func ParseVersion(out []byte) (version string, supported bool) {
	m := cliVersion.FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		return "", false
	}
	return m[1] + "." + m[2] + "." + m[3], m[1] == "2"
}
