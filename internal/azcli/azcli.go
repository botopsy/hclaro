// Package azcli owns every az process Hclaro starts for cloud discovery. It runs the
// read-only list and show calls of discovery and the identity probe, takes argv arrays checked
// against a fixed allowlist, never a shell, and returns bounded output. CLI output is read only
// to parse a result or classify a failure; raw output is never returned in an error. Errors are
// *awscli.CallError so discovery shares one set of error kinds; az's own wording is in Message.
package azcli

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
	// KindUnsupportedCloud is an Azure cloud other than the public one (owner decision pending).
	KindUnsupportedCloud = "cloud_unsupported"
)

// Queries is the exact --query (JMESPath) of every allowed command: a projection of the few
// fields discovery reads, so rules, keys, extensions, profiles and settings never leave az. A
// command with another query is refused. The text has no space, quote or shell/cmd.exe
// metacharacter, because az is az.cmd on Windows.
var Queries = map[string]string{
	"account show":         "{id:id,tenantId:tenantId,user:user.name,userType:user.type,cloud:environmentName,state:state}",
	"account list":         "[].{id:id,tenantId:tenantId,isDefault:isDefault,state:state}",
	"group list":           "[].{id:id,name:name,location:location,tags:tags}",
	"network vnet list":    "[].{id:id,name:name,location:location,subnets:subnets[].{id:id,name:name,prefix:addressPrefix},tags:tags}",
	"network nsg list":     "[].{id:id,name:name,location:location,tags:tags}",
	"vm list":              "[].{id:id,name:name,location:location,osType:storageProfile.osDisk.osType,size:hardwareProfile.vmSize,tags:tags}",
	"disk list":            "[].{id:id,name:name,location:location,sizeGb:diskSizeGb,sku:sku.name,state:diskState,tags:tags}",
	"storage account list": "[].{id:id,name:name,location:location,kind:kind,sku:sku.name,tags:tags}",
}

var guid = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidGUID reports whether s has the shape of a subscription or tenant ID.
func ValidGUID(s string) bool { return guid.MatchString(s) }

// Allowed reports whether args is `--version` or one command of Queries followed by its exact
// `--query`, `-o json`, `--only-show-errors` and an optional `--subscription GUID`, each once.
// Everything else is refused: other subcommands, tokens (account get-access-token), --debug,
// --verbose, --query of another shape, --ids, --resource-group and any other flag.
func Allowed(args []string) bool {
	if len(args) == 1 && args[0] == "--version" {
		return true
	}
	n := 0
	for n < len(args) && !strings.HasPrefix(args[n], "-") {
		n++
	}
	query, ok := Queries[strings.Join(args[:n], " ")]
	if !ok {
		return false
	}
	rest := args[n:]
	var seenQuery, seenOutput, seenQuiet, seenSub bool
	for i := 0; i < len(rest); i++ {
		switch a := rest[i]; {
		case a == "--query" && !seenQuery && i+1 < len(rest) && rest[i+1] == query:
			seenQuery = true
			i++
		case a == "-o" && !seenOutput && i+1 < len(rest) && rest[i+1] == "json":
			seenOutput = true
			i++
		case a == "--only-show-errors" && !seenQuiet:
			seenQuiet = true
		case a == "--subscription" && !seenSub && i+1 < len(rest) && guid.MatchString(rest[i+1]):
			seenSub = true
			i++
		default:
			return false
		}
	}
	return seenQuery && seenOutput
}

// withSubscription adds `--subscription ID` when the context selects one (ARM_SUBSCRIPTION_ID,
// composed by the execution profile or inherited): az has no environment variable for it, and
// Terraform uses the same value, so both talk about one subscription.
func withSubscription(environ, args []string) []string {
	if len(args) == 1 || args[0] == "account" && args[1] == "list" {
		return args
	}
	for i := len(environ) - 1; i >= 0; i-- {
		if key, v, _ := strings.Cut(environ[i], "="); strings.EqualFold(key, "ARM_SUBSCRIPTION_ID") {
			if guid.MatchString(v) && !slices.Contains(args, "--subscription") {
				return append(slices.Clone(args), "--subscription", v)
			}
			break
		}
	}
	return args
}

// Environ returns environ for a read-only az call: prompts, telemetry, colors, extension
// installs and the user's output settings are off. The credential and subscription selection
// (AZURE_*, ARM_*, AZURE_CONFIG_DIR) stays: it is the user's chain, composed by the execution
// profile.
func Environ(environ []string) []string {
	if environ == nil {
		environ = os.Environ()
	}
	set := []string{"AZURE_CORE_COLLECT_TELEMETRY=false", "AZURE_CORE_OUTPUT=json", "AZURE_CORE_NO_COLOR=true", "AZURE_CORE_ONLY_SHOW_ERRORS=true", "AZURE_CORE_DISABLE_CONFIRM_PROMPT=true", "AZURE_CORE_LOGIN_EXPERIENCE_V2=off", "AZURE_EXTENSION_USE_DYNAMIC_INSTALL=no"}
	out := make([]string, 0, len(environ)+len(set))
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		if !slices.ContainsFunc(set, func(s string) bool { return strings.EqualFold(strings.SplitN(s, "=", 2)[0], key) }) {
			out = append(out, item)
		}
	}
	return append(out, set...)
}

// Exec is the real read-only runner: `az` from PATH, argv only, empty stdin, bounded output.
type Exec struct {
	Timeout time.Duration
	// Observe, when set, is called with each permitted argv before it runs (tests).
	Observe func(args []string)
}

func (e Exec) Run(ctx context.Context, environ []string, args []string) ([]byte, error) {
	if !Allowed(args) {
		return nil, &awscli.CallError{Kind: awscli.KindRefused}
	}
	exe, err := exec.LookPath("az")
	if err != nil {
		return nil, &awscli.CallError{Kind: awscli.KindMissingCLI}
	}
	args = withSubscription(environ, args)
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

// Classify maps az error text to a kind. The text itself is discarded by callers.
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
	case has("aadsts70043", "aadsts700082", "aadsts50173", "aadsts50076", "aadsts50079", "token has expired", "refresh token has expired", "session has expired", "interactive authentication is needed"):
		return awscli.KindExpired
	case has("az login", "no subscription found", "please run 'az login'", "az account set"):
		return awscli.KindNoCredentials
	case has("subscriptionnotfound", "subscription") && has("not found", "doesn't exist", "does not exist", "is not a valid"):
		return awscli.KindProfileNotFound
	case has("aadsts7000215", "aadsts7000222", "invalid client secret", "invalid_client"):
		return awscli.KindInvalid
	case has("toomanyrequests", "429", "throttl", "retry-after"):
		return awscli.KindThrottled
	case has("authorizationfailed", "does not have authorization", "authorizationpermissionmismatch", "forbidden", "403"):
		return awscli.KindAccessDenied
	case has("missingsubscriptionregistration", "noregisteredproviderfound", "is not registered"):
		return awscli.KindRegionDisabled
	case has("failed to establish a new connection", "connection error", "name or service not known", "nodename nor servname", "connection refused", "network is unreachable", "timed out", "max retries exceeded"):
		return awscli.KindNetwork
	}
	return awscli.KindFailed
}

var cliVersion = regexp.MustCompile(`(?m)^azure-cli\s+([0-9]+)\.([0-9]+)\.([0-9]+)`)

// MinMajor and MinMinor are the oldest supported Azure CLI (owner decision: 2.50).
const (
	MinMajor = 2
	MinMinor = 50
)

// ParseVersion returns the Azure CLI version from `az --version` output and whether it is
// supported.
func ParseVersion(out []byte) (version string, supported bool) {
	m := cliVersion.FindStringSubmatch(string(out))
	if m == nil || len(m[1]) > 4 || len(m[2]) > 4 {
		return "", false
	}
	major, minor := atoi(m[1]), atoi(m[2])
	return m[1] + "." + m[2] + "." + m[3], major > MinMajor || major == MinMajor && minor >= MinMinor
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

var messages = map[string]string{
	awscli.KindMissingCLI:      "The Azure CLI (az) was not found on PATH. Install it and restart Hclaro.",
	awscli.KindUnsupportedCLI:  "Hclaro could not recognize this Azure CLI version, or it is older than supported. Update the Azure CLI and restart Hclaro.",
	awscli.KindRefused:         "Hclaro refused to run this az command because it is not on the read-only allowlist.",
	awscli.KindAccessDenied:    "Azure denied this read (missing role assignment), or the subscription is not enabled. Results for this type are missing, not empty.",
	awscli.KindExpired:         "The az login has expired. Sign in again in your terminal (az login), then verify again.",
	awscli.KindNoCredentials:   "az has no signed-in account or subscription for this context. Run az login in your terminal, then verify again.",
	awscli.KindInvalid:         "Azure rejected the credentials, or reported another subscription or tenant than this context names. Check your az login and the profile in your terminal.",
	awscli.KindProfileNotFound: "The Azure subscription named by this context was not found for the signed-in account.",
	awscli.KindThrottled:       "Azure throttled the requests and retries were exhausted. Scan fewer types, or try again later.",
	awscli.KindNetwork:         "The Azure endpoint was unreachable. Check VPN and network access.",
	awscli.KindRegionDisabled:  "The resource provider for this type is not registered in the subscription, or is not reachable with these credentials.",
	awscli.KindTimeout:         "az did not answer in time.",
	awscli.KindCancelled:       "Cancelled before this read finished.",
	awscli.KindMalformed:       "az returned output Hclaro could not read safely. Nothing from it is shown.",
	awscli.KindFailed:          "The az call failed. Details are withheld because CLI messages can echo configuration.",
	awscli.KindIdentityChanged: "az now resolves to another subscription than the one you verified. Verify the Azure context again before scanning or importing.",
	awscli.KindOutputTooLarge:  "az returned more output than Hclaro reads for one list.",
	KindUnsupportedCloud:       "This subscription belongs to an Azure cloud other than the public Azure cloud (AzureCloud). Sovereign clouds are not supported yet.",
}

// Message returns the user-facing explanation of an error kind for az.
func Message(kind string) string {
	if m, ok := messages[kind]; ok {
		return m
	}
	return messages[awscli.KindFailed]
}
