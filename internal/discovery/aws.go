// Package discovery lists existing AWS objects of a small, documented set of resource types
// in the account of the user's own credential chain, labels each one against the launch
// workspace's recorded state and prepares an import for the M04 wizard. It never creates,
// changes or deletes anything: the AWS CLI runs only through Exec, which accepts a fixed set
// of read-only Describe/List/Get operations as argv arrays, never through a shell. Nothing
// runs unless the user asks for it.
package discovery

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

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
	maxOutput            = 8 << 20
	maxStderr            = 16 << 10
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
	"sts":   {"get-caller-identity": true},
	"ec2":   {"describe-vpcs": true, "describe-subnets": true, "describe-security-groups": true},
	"s3api": {"list-buckets": true},
	"rds":   {"describe-db-instances": true},
}

var (
	regionPattern = regexp.MustCompile(`^[a-z]{2}(-gov|-iso[a-z]?)?-[a-z]+-[0-9]{1,2}$`)
	numberPattern = regexp.MustCompile(`^[1-9][0-9]{0,3}$`)
	// tokenPattern bounds an AWS CLI pagination token (base64 of the service token).
	tokenPattern = boundedPattern{regexp.MustCompile(`^[A-Za-z0-9+/=_.-]+$`), 4096}
)

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

// Environ returns environ for an AWS CLI call: the pager, auto-prompt and the CLI's own
// retries are disabled (Hclaro retries throttled calls itself), and settings that change the
// output format are dropped. Credential settings are kept: they are the user's credential
// chain, already composed by the execution profile.
func Environ(environ []string) []string {
	if environ == nil {
		environ = os.Environ()
	}
	out := make([]string, 0, len(environ)+5)
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		switch strings.ToUpper(key) {
		case "AWS_PAGER", "AWS_CLI_AUTO_PROMPT", "AWS_DEFAULT_OUTPUT", "AWS_MAX_ATTEMPTS", "AWS_RETRY_MODE", "AWS_CLI_FILE_ENCODING":
			continue
		}
		out = append(out, item)
	}
	return append(out, "AWS_PAGER=", "AWS_CLI_AUTO_PROMPT=off", "AWS_MAX_ATTEMPTS=1", "AWS_RETRY_MODE=standard")
}

// Exec is the real runner: `aws` from PATH, argv only, empty stdin, bounded output.
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
		argv = append(argv, "--no-cli-pager", "--cli-connect-timeout", defaultConnectSecond, "--cli-read-timeout", defaultReadSecond)
	}
	cmd := exec.CommandContext(ctx, exe, argv...)
	cmd.WaitDelay = time.Second
	cmd.Env = Environ(environ)
	out := &bounded.Buffer{Limit: maxOutput}
	// stderr is read only to classify the failure; it is never returned or logged.
	errOut := &bounded.Buffer{Limit: maxStderr}
	cmd.Stdout, cmd.Stderr = out, errOut
	if err := cmd.Run(); err != nil {
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
