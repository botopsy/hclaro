// Package preflight evaluates the checks that gate a saved-plan apply. Evaluation is pure
// and deterministic: callers gather the facts (context, artifact digest, Git status, an
// optional cloud identity) and Evaluate turns them into typed checks. Built-in context,
// artifact, backend and operation checks always block when they fail; a small, versioned
// local policy can only tune the advisory rules (dirty Git, production-like labels, a
// destructive-change limit and an AWS account allow-list).
package preflight

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Severity of a check that does not pass. Off disables an optional policy rule.
type Severity string

const (
	Block Severity = "block"
	Warn  Severity = "warn"
	Info  Severity = "info"
	Off   Severity = "off"
)

// PolicyVersion is the only policy schema version this build understands.
const PolicyVersion = 1

// PolicyFile is the policy location relative to the project root.
const PolicyFile = ".hclaro/policy.json"

// maxPolicy bounds the policy file size.
const maxPolicy = 64 << 10

// Policy is the effective local policy. It holds no code: every rule is data with a fixed
// meaning, so a policy can neither run anything nor relax the built-in blocking checks.
type Policy struct {
	Version int   `json:"version"`
	Rules   Rules `json:"rules"`
}

// Rules are the configurable preflight rules.
type Rules struct {
	// GitDirty is the severity of uncommitted or untracked files in the Git working tree.
	GitDirty Severity `json:"gitDirty"`
	// ProductionLabels flags workspaces and profile names matching any pattern.
	ProductionLabels LabelRule `json:"productionLabels"`
	// DestructiveLimit flags plans that delete, replace or forget more than Max objects.
	DestructiveLimit *LimitRule `json:"destructiveLimit,omitempty"`
	// AWSAccounts requires a verified AWS caller identity in one of the allowed accounts.
	AWSAccounts *AccountRule `json:"awsAccounts,omitempty"`
}

type LabelRule struct {
	Severity Severity `json:"severity"`
	// Patterns are case-insensitive globs where * matches any run of characters.
	Patterns []string `json:"patterns"`
}

type LimitRule struct {
	Severity Severity `json:"severity"`
	Max      int      `json:"max"`
}

type AccountRule struct {
	Severity Severity `json:"severity"`
	Allowed  []string `json:"allowed"`
}

// DefaultPolicy applies when the project has no policy file.
func DefaultPolicy() Policy {
	return Policy{Version: PolicyVersion, Rules: Rules{
		GitDirty:         Warn,
		ProductionLabels: LabelRule{Severity: Warn, Patterns: []string{"prod", "prod-*", "*-prod", "prod_*", "*_prod", "production", "*production*", "prd", "*-prd", "live"}},
	}}
}

// PolicyInfo describes where the effective policy came from.
type PolicyInfo struct {
	Source  string `json:"source"`
	Version int    `json:"version"`
	Valid   bool   `json:"valid"`
	// Error explains an invalid policy; the file content is never echoed.
	Error string `json:"error,omitempty"`
	Rules Rules  `json:"rules"`
}

var (
	patternRE = regexp.MustCompile(`^[A-Za-z0-9_.*-]{1,64}$`)
	accountRE = regexp.MustCompile(`^[0-9]{12}$`)
)

// ErrPolicy wraps every reason a policy file is refused.
var ErrPolicy = errors.New("invalid preflight policy")

// LoadPolicy reads root/.hclaro/policy.json. A missing file yields the default policy; any
// other problem (symlink, size, syntax, unknown field, unsupported version or value)
// returns ErrPolicy and the default policy, and the caller must treat it as blocking.
func LoadPolicy(root string) (Policy, PolicyInfo) {
	def := DefaultPolicy()
	info := PolicyInfo{Source: "default", Version: PolicyVersion, Valid: true, Rules: def.Rules}
	if root == "" {
		return def, info
	}
	file := filepath.Join(root, filepath.FromSlash(PolicyFile))
	fail := func(reason string) (Policy, PolicyInfo) {
		return def, PolicyInfo{Source: PolicyFile, Version: 0, Valid: false, Error: reason, Rules: def.Rules}
	}
	dir, err := os.Lstat(filepath.Dir(file))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return def, info
	case err != nil || !dir.IsDir():
		return fail(".hclaro must be a regular directory, not a link.")
	}
	fi, err := os.Lstat(file)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return def, info
	case err != nil:
		return fail("The policy file could not be read.")
	case !fi.Mode().IsRegular():
		return fail("The policy file must be a regular file, not a link or device.")
	case fi.Size() > maxPolicy:
		return fail("The policy file is larger than 64 KiB.")
	}
	f, err := os.Open(file)
	if err != nil {
		return fail("The policy file could not be read.")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxPolicy+1))
	if err != nil || len(b) > maxPolicy {
		return fail("The policy file could not be read.")
	}
	p, err := ParsePolicy(b)
	if err != nil {
		return fail(err.Error())
	}
	return p, PolicyInfo{Source: PolicyFile, Version: p.Version, Valid: true, Rules: p.Rules}
}

// ParsePolicy validates a policy document. Omitted rules keep their defaults.
func ParsePolicy(b []byte) (Policy, error) {
	var raw struct {
		Version *int `json:"version"`
		Rules   *struct {
			GitDirty         *Severity    `json:"gitDirty"`
			ProductionLabels *LabelRule   `json:"productionLabels"`
			DestructiveLimit *LimitRule   `json:"destructiveLimit"`
			AWSAccounts      *AccountRule `json:"awsAccounts"`
		} `json:"rules"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil || dec.More() {
		return Policy{}, fmt.Errorf("%w: the file is not valid JSON or has unknown fields", ErrPolicy)
	}
	if raw.Version == nil || *raw.Version != PolicyVersion {
		return Policy{}, fmt.Errorf("%w: version must be %d", ErrPolicy, PolicyVersion)
	}
	p := DefaultPolicy()
	if raw.Rules == nil {
		return p, nil
	}
	r := raw.Rules
	if r.GitDirty != nil {
		if !validSeverity(*r.GitDirty) {
			return Policy{}, fmt.Errorf("%w: rules.gitDirty must be off, info, warn or block", ErrPolicy)
		}
		p.Rules.GitDirty = *r.GitDirty
	}
	if r.ProductionLabels != nil {
		l := *r.ProductionLabels
		if !validSeverity(l.Severity) || len(l.Patterns) > 32 {
			return Policy{}, fmt.Errorf("%w: rules.productionLabels needs a severity and at most 32 patterns", ErrPolicy)
		}
		for _, pat := range l.Patterns {
			if !patternRE.MatchString(pat) {
				return Policy{}, fmt.Errorf("%w: production label patterns may use letters, digits, '.', '_', '-' and '*' (1-64 characters)", ErrPolicy)
			}
		}
		l.Patterns = slices.Clone(l.Patterns)
		if l.Patterns == nil {
			l.Patterns = []string{}
		}
		p.Rules.ProductionLabels = l
	}
	if r.DestructiveLimit != nil {
		l := *r.DestructiveLimit
		if !validSeverity(l.Severity) || l.Severity == Off || l.Max < 0 || l.Max > 100_000 {
			return Policy{}, fmt.Errorf("%w: rules.destructiveLimit needs severity info, warn or block and max between 0 and 100000", ErrPolicy)
		}
		p.Rules.DestructiveLimit = &l
	}
	if r.AWSAccounts != nil {
		a := *r.AWSAccounts
		if !validSeverity(a.Severity) || a.Severity == Off || len(a.Allowed) == 0 || len(a.Allowed) > 50 {
			return Policy{}, fmt.Errorf("%w: rules.awsAccounts needs severity info, warn or block and 1-50 allowed account IDs", ErrPolicy)
		}
		for _, id := range a.Allowed {
			if !accountRE.MatchString(id) {
				return Policy{}, fmt.Errorf("%w: AWS account IDs must be 12 digits", ErrPolicy)
			}
		}
		a.Allowed = slices.Clone(a.Allowed)
		p.Rules.AWSAccounts = &a
	}
	return p, nil
}

func validSeverity(s Severity) bool {
	return s == Off || s == Info || s == Warn || s == Block
}

// matchLabel reports the first pattern that matches value, case-insensitively.
func matchLabel(patterns []string, value string) string {
	v := strings.ToLower(value)
	if v == "" {
		return ""
	}
	for _, p := range patterns {
		// The pattern alphabet excludes path.Match's other metacharacters.
		if ok, err := path.Match(strings.ToLower(p), v); err == nil && ok {
			return p
		}
	}
	return ""
}
