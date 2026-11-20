package awscli

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"hclaro/internal/state"
)

const identityTimeout = 20 * time.Second

// Identity is the result of an optional, explicitly requested cloud identity probe.
type Identity struct {
	// State is not_requested, verified, unavailable (no CLI) or failed.
	State   string `json:"state"`
	Account string `json:"account,omitempty"`
	ARN     string `json:"arn,omitempty"`
	// Kind classifies a failed probe (error kinds such as credentials_expired).
	Kind string `json:"kind,omitempty"`
	// Message explains unavailable or failed probes; it never contains CLI output.
	Message string `json:"message,omitempty"`
	At      string `json:"verifiedAt,omitempty"`
}

// IdentityProbe asks a cloud CLI for the caller identity using environ. Tests inject fakes.
type IdentityProbe func(ctx context.Context, environ []string) Identity

var (
	arnRE     = regexp.MustCompile(`^arn:aws[a-z-]{0,20}:(sts|iam)::[0-9]{12}:[A-Za-z0-9+=,.@/_-]{1,512}$`)
	accountID = regexp.MustCompile(`^[0-9]{12}$`)
)

// ValidARN reports whether arn is a readable STS or IAM caller ARN (not "ARN withheld").
func ValidARN(arn string) bool { return arnRE.MatchString(arn) }

// AWSIdentity runs `aws sts get-caller-identity --output json` with environ, through
// aws-vault when the composed environment selects it, exactly as the engine would run. It is
// called only after the user asks for it: STS is a read-only call, but it contacts AWS. The
// pager and prompts are disabled, stdin is empty and stderr is only classified. Only the
// account ID and a validated ARN are returned; the user ID and any other output are dropped.
func AWSIdentity(ctx context.Context, environ []string) Identity {
	out, err := Exec{Timeout: identityTimeout}.Run(ctx, environ, []string{"sts", "get-caller-identity", "--output", "json"})
	if err != nil {
		kind := KindOf(err)
		state := "failed"
		if kind == KindMissingCLI || kind == KindVaultMissing {
			state = "unavailable"
		}
		return Identity{State: state, Kind: kind, Message: Message(kind)}
	}
	return ParseIdentity(out)
}

// ParseIdentity extracts the account and ARN from get-caller-identity JSON.
func ParseIdentity(b []byte) Identity {
	var v struct {
		Account string `json:"Account"`
		Arn     string `json:"Arn"`
	}
	if json.Unmarshal(b, &v) != nil || !accountID.MatchString(v.Account) {
		return Identity{State: "failed", Message: "The AWS CLI returned an unexpected response."}
	}
	id := Identity{State: "verified", Account: v.Account, At: time.Now().UTC().Format(time.RFC3339)}
	if arnRE.MatchString(v.Arn) && strings.Contains(v.Arn, "::"+v.Account+":") && !state.Suspicious(v.Arn) {
		id.ARN = v.Arn
	} else {
		id.ARN = "ARN withheld"
	}
	return id
}

// Principal is the stable part of an STS caller ARN: an assumed-role ARN without its session
// name (aws-vault and SSO name every session differently), any other ARN unchanged.
func Principal(arn string) string {
	if head, rest, ok := strings.Cut(arn, ":assumed-role/"); ok {
		if role, _, ok := strings.Cut(rest, "/"); ok {
			return head + ":assumed-role/" + role
		}
	}
	return arn
}

// SameIdentity reports whether two verified identities are the same account and principal.
// An identity that is not verified, or whose ARN was withheld (so its principal is unknown),
// is never the same as anything.
func SameIdentity(a, b Identity) bool {
	return a.State == "verified" && b.State == "verified" && arnRE.MatchString(a.ARN) && arnRE.MatchString(b.ARN) &&
		a.Account == b.Account && Principal(a.ARN) == Principal(b.ARN)
}
