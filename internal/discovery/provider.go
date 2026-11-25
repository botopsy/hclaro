package discovery

import (
	"context"
	"slices"

	"hclaro/internal/awscli"
)

// Provider is one cloud's read-only listing backend: the allowlisted CLI it runs, how it
// verifies the caller and how it asks for one page of a resource type. Service owns the
// scan lifecycle, limits, retries and matching and knows no cloud-specific names.
type Provider interface {
	// Run runs one read-only CLI call; errors are *awscli.CallError (shared kinds).
	awscli.Runner
	Name() string
	// Verify checks the CLI version and the caller identity of pin's context.
	Verify(ctx context.Context, pin Pin, environ []string) (Verified, error)
	// Account returns the account the credentials resolve to now (scan-time recheck).
	Account(ctx context.Context, environ []string, region string) (string, error)
	// ListArgs is the argv of one page of t in region ("" token starts the listing).
	ListArgs(t ResourceType, region string, pageSize int, token string) []string
}

// Verified is a provider's verified caller identity. ARN is the display form of the
// principal and is not an AWS-only value: other providers put their principal there.
type Verified struct {
	Account, ARN, PrincipalType, Partition, Region, RegionSource, CLIVersion string
	// Tenant is the Azure tenant ("" for other providers).
	Tenant string
}

// awsProvider lists AWS objects with the AWS CLI v2.
type awsProvider struct{ awscli.Runner }

func (awsProvider) Name() string { return "aws" }

func (p awsProvider) Verify(ctx context.Context, pin Pin, environ []string) (Verified, error) {
	out, err := p.Run(ctx, environ, []string{"--version"})
	if err != nil {
		return Verified{}, err
	}
	version, supported := awscli.ParseVersion(out)
	if !supported {
		return Verified{}, &awscli.CallError{Kind: awscli.KindUnsupportedCLI}
	}
	region, source := RegionOf(pin, environ)
	account, arn, principal, partition, err := p.identity(ctx, environ, region)
	if err != nil {
		return Verified{}, err
	}
	return Verified{account, arn, principal, partition, region, source, version, ""}, nil
}

func (p awsProvider) Account(ctx context.Context, environ []string, region string) (string, error) {
	account, _, _, _, err := p.identity(ctx, environ, region)
	return account, err
}

// identity runs STS GetCallerIdentity in region.
func (p awsProvider) identity(ctx context.Context, environ []string, region string) (account, arn, principal, partition string, err error) {
	out, err := p.Run(ctx, environ, []string{"sts", "get-caller-identity", "--output", "json", "--region", region})
	if err != nil {
		return "", "", "", "", err
	}
	account, arn, principal, partition, err = parseIdentity(out)
	if err != nil {
		return "", "", "", "", &awscli.CallError{Kind: awscli.KindMalformed}
	}
	return account, arn, principal, partition, nil
}

func (awsProvider) ListArgs(t ResourceType, region string, pageSize int, token string) []string {
	args := append(slices.Clone(t.args), "--output", "json", "--region", region)
	if !t.noPaging {
		args = append(args, "--max-items", itoa(pageSize))
	}
	if token != "" {
		args = append(args, "--starting-token", token)
	}
	return args
}
