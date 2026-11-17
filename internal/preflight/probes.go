package preflight

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"hclaro/internal/discovery"
	"hclaro/internal/state"
)

// GitProbe reads the Git working tree status of root. Tests inject fakes.
type GitProbe func(ctx context.Context, root string) GitResult

// IdentityProbe asks a cloud CLI for the caller identity using environ. Tests inject fakes.
type IdentityProbe func(ctx context.Context, environ []string) Identity

const (
	gitTimeout      = 10 * time.Second
	identityTimeout = 20 * time.Second
	maxProbeOutput  = 1 << 20
	maxPaths        = 10
)

// insideRepository reports whether root or a parent has a .git entry. It reads only names.
func insideRepository(root string) bool {
	for dir := root; ; {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// GitRunner runs one allowlisted, read-only Git command in dir and returns at most limit
// bytes of stdout and whether more was written. gitctx.Exec.Run is the real one: it disables
// fsmonitor, the untracked cache, optional locks and prompts, drops inherited GIT_*
// variables and discards stderr.
type GitRunner func(ctx context.Context, dir string, limit int, args ...string) ([]byte, bool, error)

// GitStatus runs `git status --porcelain=v1 -z --untracked-files=normal` in root through run,
// so status neither runs repository-configured programs nor writes to the repository, and
// an inherited GIT_DIR or GIT_INDEX_FILE cannot make it describe another repository.
func GitStatus(ctx context.Context, root string, run GitRunner) GitResult {
	if !insideRepository(root) {
		return GitResult{State: GitNotRepo, Paths: []string{}}
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	out, overflow, err := run(ctx, root, maxProbeOutput, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return GitResult{State: GitUnavailable, Paths: []string{}}
	}
	r := ParsePorcelain(out)
	r.Incomplete = overflow
	return r
}

// ParsePorcelain parses `git status --porcelain=v1 -z` output.
func ParsePorcelain(b []byte) GitResult {
	r := GitResult{State: GitClean, Paths: []string{}}
	fields := bytes.Split(b, []byte{0})
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		xy, name := string(f[:2]), string(f[3:])
		if xy == "??" {
			r.Untracked++
		} else {
			r.Modified++
		}
		// A rename or copy is followed by its source path.
		if xy[0] == 'R' || xy[0] == 'C' {
			i++
		}
		if len(r.Paths) < maxPaths {
			if p := cleanPath(name); p != "" {
				r.Paths = append(r.Paths, p)
			}
		}
	}
	if r.Modified+r.Untracked > 0 {
		r.State = GitDirty
	}
	// Truncated means not every changed path is listed.
	r.Truncated = len(r.Paths) < r.Modified+r.Untracked
	return r
}

// cleanPath makes a repository path safe to display, or drops it.
func cleanPath(p string) string {
	if len(p) > 200 || state.Suspicious(p) {
		return ""
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return p
}

var (
	arnRE     = regexp.MustCompile(`^arn:aws[a-z-]{0,20}:(sts|iam)::[0-9]{12}:[A-Za-z0-9+=,.@/_-]{1,512}$`)
	accountID = regexp.MustCompile(`^[0-9]{12}$`)
)

// AWSIdentity runs `aws sts get-caller-identity --output json` with environ, through
// aws-vault when the composed environment selects it, exactly as the engine would run. It is
// called only after the user asks for it: STS is a read-only call, but it contacts AWS. The
// pager and prompts are disabled, stdin is empty and stderr is only classified. Only the
// account ID and a validated ARN are returned; the user ID and any other output are dropped.
func AWSIdentity(ctx context.Context, environ []string) Identity {
	out, err := discovery.Exec{Timeout: identityTimeout}.Run(ctx, environ, []string{"sts", "get-caller-identity", "--output", "json"})
	if err != nil {
		kind := discovery.KindOf(err)
		state := "failed"
		if kind == discovery.KindMissingCLI || kind == discovery.KindVaultMissing {
			state = "unavailable"
		}
		return Identity{State: state, Kind: kind, Message: discovery.Message(kind)}
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
