// Package gcpcli owns every gcloud process Hclaro starts for cloud discovery. It runs the
// read-only list and describe calls of discovery and the identity probe, takes argv arrays
// checked against a fixed allowlist, never a shell, and returns bounded output. CLI output is
// read only to parse a result or classify a failure; raw output is never returned in an error.
// Errors are *awscli.CallError so discovery shares one set of error kinds; gcloud's own
// wording is in Message.
package gcpcli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"hclaro/internal/awscli"
)

const (
	maxOutput   = 8 << 20
	callTimeout = 60 * time.Second
)

// Formats is the exact --format of every allowed command: a projection of the few fields
// discovery reads, so descriptions, metadata (startup scripts), rules and keys never leave
// gcloud. A command with another format is refused.
var Formats = map[string]string{
	"auth list":                     "json(account,status)",
	"config list":                   "json(core.account,core.project,compute.region)",
	"projects describe":             "json(projectId,lifecycleState)",
	"compute networks list":         "json(name,routingConfig.routingMode)",
	"compute networks subnets list": "json(name,region,ipCidrRange,network)",
	"compute firewall-rules list":   "json(name,network,direction,disabled)",
	"compute instances list":        "json(name,zone,machineType,status,labels)",
	"compute disks list":            "json(name,zone,sizeGb,type,status,labels)",
	"storage buckets list":          "json(name,location,storage_url,default_storage_class,labels)",
	"sql instances list":            "json(name,region,databaseVersion,state,settings.userLabels)",
}

var (
	projectID     = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	numberPattern = regexp.MustCompile(`^[1-9][0-9]{0,3}$`)
)

// ValidProject reports whether s has the shape of a GCP project ID.
func ValidProject(s string) bool { return projectID.MatchString(s) }

// Allowed reports whether args is `--version`, `auth list`, `config list`,
// `projects describe PROJECT`, or a list command of Formats with its exact --format, an
// optional --limit and --quiet. Everything else is refused: other subcommands, access or
// identity tokens (auth print-access-token), --impersonate-service-account, --project,
// --billing-project, --filter, --flatten and any other flag.
func Allowed(args []string) bool {
	if len(args) == 1 && args[0] == "--version" {
		return true
	}
	// The command is the leading words that are not flags or the project argument.
	n := 0
	for n < len(args) && !strings.HasPrefix(args[n], "-") {
		n++
	}
	words, rest := args[:n], args[n:]
	if len(words) == 3 && words[0] == "projects" && words[1] == "describe" && projectID.MatchString(words[2]) {
		words = words[:2]
	}
	format, ok := Formats[strings.Join(words, " ")]
	if !ok {
		return false
	}
	seenFormat, seenLimit, seenQuiet := false, false, false
	for i := 0; i < len(rest); i++ {
		switch a := rest[i]; {
		case a == "--format="+format && !seenFormat:
			seenFormat = true
		case a == "--quiet" && !seenQuiet:
			seenQuiet = true
		case a == "--limit" && !seenLimit && i+1 < len(rest) && numberPattern.MatchString(rest[i+1]):
			seenLimit = true
			i++
		default:
			return false
		}
	}
	return seenFormat
}

// Environ returns environ for a read-only gcloud call: prompts, the update checker, usage
// reporting and the user's output settings are off. The credential selection (configuration,
// project, account, key file) stays: it is the user's chain, composed by the execution profile.
func Environ(environ []string) []string {
	if environ == nil {
		environ = os.Environ()
	}
	drop := []string{"CLOUDSDK_CORE_DISABLE_PROMPTS", "CLOUDSDK_CORE_DISABLE_USAGE_REPORTING", "CLOUDSDK_COMPONENT_MANAGER_DISABLE_UPDATE_CHECK", "CLOUDSDK_CORE_VERBOSITY", "CLOUDSDK_CORE_LOG_HTTP", "CLOUDSDK_CORE_SHOW_STRUCTURED_LOGS", "CLOUDSDK_CORE_USER_OUTPUT_ENABLED"}
	out := make([]string, 0, len(environ)+4)
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		if !slices.Contains(drop, strings.ToUpper(key)) {
			out = append(out, item)
		}
	}
	return append(out, "CLOUDSDK_CORE_DISABLE_PROMPTS=1", "CLOUDSDK_CORE_DISABLE_USAGE_REPORTING=true", "CLOUDSDK_COMPONENT_MANAGER_DISABLE_UPDATE_CHECK=true", "CLOUDSDK_CORE_VERBOSITY=error")
}

// Exec is the real read-only runner: `gcloud` from PATH, argv only, empty stdin, bounded output.
type Exec struct {
	Timeout time.Duration
	// Observe, when set, is called with each permitted argv before it runs (tests).
	Observe func(args []string)
}

func (e Exec) Run(ctx context.Context, environ []string, args []string) ([]byte, error) {
	if !Allowed(args) {
		return nil, &awscli.CallError{Kind: awscli.KindRefused}
	}
	exe, err := exec.LookPath("gcloud")
	if err != nil {
		return nil, &awscli.CallError{Kind: awscli.KindMissingCLI}
	}
	if e.Observe != nil {
		e.Observe(slices.Clone(args))
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = callTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, errOut, err := awscli.Start(ctx, exe, args, Environ(environ), maxOutput)
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			return nil, &awscli.CallError{Kind: awscli.KindCancelled}
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, &awscli.CallError{Kind: awscli.KindTimeout}
		}
		return nil, &awscli.CallError{Kind: Classify(errOut.String())}
	}
	if out.Overflow {
		return nil, &awscli.CallError{Kind: awscli.KindOutputTooLarge}
	}
	return out.Bytes(), nil
}

// Classify maps gcloud error text to a kind. The text itself is discarded by callers.
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
	case has("reauthentication", "reauth", "refresh token", "token has been expired", "invalid_grant", "gcloud auth login"):
		return awscli.KindExpired
	case has("you do not currently have an active account", "could not automatically determine credentials", "no credentialed accounts"):
		return awscli.KindNoCredentials
	case has("configuration", "named configuration") && has("does not exist", "not exist"):
		return awscli.KindProfileNotFound
	case has("rateLimitExceeded", "quota exceeded", "resource_exhausted", "too many requests", "429"):
		return awscli.KindThrottled
	case has("permission_denied", "permission denied", "403", "forbidden", "does not have", "not authorized"):
		return awscli.KindAccessDenied
	case has("api has not been used", "service_disabled", "is not enabled"):
		return awscli.KindRegionDisabled
	case has("failed to connect", "unable to connect", "name or service not known", "nodename nor servname", "connection refused", "network is unreachable", "timed out"):
		return awscli.KindNetwork
	}
	return awscli.KindFailed
}

var cliVersion = regexp.MustCompile(`(?m)^Google Cloud SDK ([0-9]+)\.([0-9]+)\.([0-9]+)`)

// MinMajor is the oldest supported Google Cloud SDK major version (owner decision: 400).
const MinMajor = 400

// ParseVersion returns the Google Cloud SDK version from `gcloud --version` output and
// whether it is supported.
func ParseVersion(out []byte) (version string, supported bool) {
	m := cliVersion.FindStringSubmatch(string(out))
	if m == nil {
		return "", false
	}
	major := 0
	for _, c := range m[1] {
		major = major*10 + int(c-'0')
		if major > 1<<20 {
			return "", false
		}
	}
	return m[1] + "." + m[2] + "." + m[3], major >= MinMajor
}

var messages = map[string]string{
	awscli.KindMissingCLI:      "The Google Cloud CLI (gcloud) was not found on PATH. Install it and restart Hclaro.",
	awscli.KindUnsupportedCLI:  "Hclaro could not recognize this Google Cloud CLI version, or it is older than supported. Update the Google Cloud CLI and restart Hclaro.",
	awscli.KindRefused:         "Hclaro refused to run this gcloud command because it is not on the read-only allowlist.",
	awscli.KindAccessDenied:    "Google Cloud denied this read (missing IAM permission). Results for this type are missing, not empty.",
	awscli.KindExpired:         "The gcloud login has expired. Sign in again in your terminal (gcloud auth login), then verify again.",
	awscli.KindNoCredentials:   "gcloud has no active account or project for this context. Run gcloud auth login and gcloud config set project in your terminal, then verify again.",
	awscli.KindInvalid:         "Google Cloud rejected the credentials. Check your gcloud configuration in your terminal.",
	awscli.KindProfileNotFound: "The gcloud configuration named by this context does not exist.",
	awscli.KindThrottled:       "Google Cloud throttled the requests and retries were exhausted. Scan fewer types, or try again later.",
	awscli.KindNetwork:         "The Google Cloud endpoint was unreachable. Check VPN and network access.",
	awscli.KindRegionDisabled:  "The API for this type is not enabled in the project, or is not reachable with these credentials.",
	awscli.KindTimeout:         "gcloud did not answer in time.",
	awscli.KindCancelled:       "Cancelled before this read finished.",
	awscli.KindMalformed:       "gcloud returned output Hclaro could not read safely. Nothing from it is shown.",
	awscli.KindFailed:          "The gcloud call failed. Details are withheld because CLI messages can echo configuration.",
	awscli.KindIdentityChanged: "gcloud now resolves to another project than the one you verified. Verify the Google Cloud context again before scanning or importing.",
	awscli.KindOutputTooLarge:  "gcloud returned more output than Hclaro reads for one list.",
}

// Message returns the user-facing explanation of an error kind for gcloud.
func Message(kind string) string {
	if m, ok := messages[kind]; ok {
		return m
	}
	return messages[awscli.KindFailed]
}
