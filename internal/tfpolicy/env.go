// Package tfpolicy owns the rules for what Hclaro feeds Terraform/OpenTofu: which inherited
// environment variables are controlled, the TF_CLI_ARGS refusal, which variable files may be
// passed and which engine versions and features are supported. engine, profiles, workspace
// and operations use it, so the rules are written once.
package tfpolicy

import (
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"hclaro/internal/ids"
	"hclaro/internal/state"
)

// Rule says how Hclaro treats an inherited variable.
type Rule int

const (
	// Passed variables are not controlled by Hclaro.
	Passed Rule = iota
	// EngineOwned variables (TF_LOG*, TF_IN_AUTOMATION, CHECKPOINT_DISABLE) are dropped from
	// every child and replaced by Hclaro's own values (see ChildEnv).
	EngineOwned
	// Workspace is TF_WORKSPACE: a context's workspace replaces it.
	Workspace
	// CLIArgs is TF_CLI_ARGS*: anything but -no-color refuses (see UnsafeArgs).
	CLIArgs
	// DataDir is TF_DATA_DIR: it picks the data directory, so a profile cannot list it.
	DataDir
	// AWSProfile is AWS_PROFILE and AWS_DEFAULT_PROFILE, set by an execution profile.
	AWSProfile
	// AWSRegion is AWS_REGION and AWS_DEFAULT_REGION, set by an execution profile.
	AWSRegion
)

// Controlled returns the rule for an environment variable name. Names compare without case
// on every OS: Windows reads them that way, and elsewhere folding only filters or hashes a
// lowercase variant too, which is stricter than the engine and never weaker.
func Controlled(name string) Rule {
	upper := strings.ToUpper(name)
	switch {
	case strings.HasPrefix(upper, "TF_LOG"), upper == "TF_IN_AUTOMATION", upper == "CHECKPOINT_DISABLE":
		return EngineOwned
	case upper == "TF_WORKSPACE":
		return Workspace
	case strings.HasPrefix(upper, "TF_CLI_ARGS"):
		return CLIArgs
	case upper == "TF_DATA_DIR":
		return DataDir
	case upper == "AWS_PROFILE", upper == "AWS_DEFAULT_PROFILE":
		return AWSProfile
	case upper == "AWS_REGION", upper == "AWS_DEFAULT_REGION":
		return AWSRegion
	}
	return Passed
}

// EngineName is name as the engine looks it up on this OS: case-folded on Windows, where
// environment names are case-insensitive, and unchanged elsewhere. The plan fingerprint
// hashes a variable only under a name the engine reads, so a variable the engine ignores
// (tf_var_x on Linux) never invalidates a plan, and one it reads (tf_var_x on Windows)
// always does. Filtering (Controlled) folds on every OS instead, which is only stricter.
func EngineName(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

// ChildEnv is the environment of one engine child: environ without EngineOwned variables
// (and TF_WORKSPACE when dropWorkspace), then TF_IN_AUTOMATION=1, CHECKPOINT_DISABLE=1 and
// TF_LOG=OFF, so inherited logging can never persist raw state via TF_LOG_PATH.
func ChildEnv(environ []string, dropWorkspace bool) []string {
	out := make([]string, 0, len(environ)+3)
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		switch Controlled(key) {
		case EngineOwned:
			continue
		case Workspace:
			if dropWorkspace {
				continue
			}
		}
		out = append(out, item)
	}
	return append(out, "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1", "TF_LOG=OFF")
}

var ErrUnsafeArgs = errors.New("inherited TF_CLI_ARGS contains unsupported arguments; only -no-color is accepted")

// flagName matches a bare flag name; anything after "=" is a value and is never reported.
var flagName = regexp.MustCompile(`^--?[A-Za-z][A-Za-z0-9_-]{0,40}$`)

// unsafeArgsVar reports whether the variable key is read for one of commands, each a
// subcommand as typed ("workspace select"). The engines read TF_CLI_ARGS and
// TF_CLI_ARGS_<subcommand> with words joined by "_" and dashes turned into "_"
// (TF_CLI_ARGS_workspace_select); every leading word group counts, so TF_CLI_ARGS_workspace
// does too. No commands means any TF_CLI_ARGS*. Names compare without case, as Controlled.
func unsafeArgsVar(key string, commands []string) bool {
	upper := strings.ToUpper(key)
	if upper == "TF_CLI_ARGS" || len(commands) == 0 && Controlled(key) == CLIArgs {
		return true
	}
	for _, c := range commands {
		name := "TF_CLI_ARGS"
		for _, word := range strings.Fields(c) {
			name += "_" + strings.ToUpper(strings.ReplaceAll(word, "-", "_"))
			if upper == name {
				return true
			}
		}
	}
	return false
}

// UnsafeArgs reports an inherited TF_CLI_ARGS variable read for one of commands (see
// unsafeArgsVar) that adds anything other than -no-color.
func UnsafeArgs(environ []string, commands ...string) bool {
	for _, item := range environ {
		key, value, _ := strings.Cut(item, "=")
		if unsafeArgsVar(key, commands) && slices.ContainsFunc(strings.Fields(value), func(arg string) bool { return arg != "-no-color" }) {
			return true
		}
	}
	return false
}

// UnsafeArgsHelp explains which inherited TF_CLI_ARGS variables block the commands (see
// unsafeArgsVar) and how to remove them. It names variables and flag names only, never values;
// arguments that are not a plain flag are counted, not shown. It returns "" when none block.
// shortcut: a secret passed space-separated after a flag and itself shaped like "-name" would
// be named; such values are not expected from TF_CLI_ARGS.
func UnsafeArgsHelp(environ []string, commands ...string) string {
	found, unset := []string{}, []string{}
	for _, item := range environ {
		key, value, _ := strings.Cut(item, "=")
		if !unsafeArgsVar(key, commands) {
			continue
		}
		names, other := []string{}, 0
		for _, arg := range strings.Fields(value) {
			name, _, _ := strings.Cut(arg, "=")
			switch {
			case arg == "-no-color":
			case flagName.MatchString(name):
				names = append(names, name)
			default:
				other++
			}
		}
		if other > 0 {
			names = append(names, fmt.Sprintf("%d other argument(s)", other))
		}
		if len(names) > 0 {
			found = append(found, key+" adds "+strings.Join(names, ", "))
			unset = append(unset, key)
		}
	}
	if len(found) == 0 {
		return ""
	}
	return "Found: " + strings.Join(found, "; ") + ". Hidden flags such as -target, -replace, -lock=false or -auto-approve would change what runs without appearing in the reviewed command, so only -no-color is accepted. Unset them in the shell or environment that starts Hclaro (for example `unset " + strings.Join(unset, " ") + "`) and restart Hclaro. An execution profile cannot override inherited TF_CLI_ARGS settings, and Hclaro never removes them for you."
}

// ValidWorkspace accepts the conservative workspace name subset Hclaro displays and passes
// to TF_WORKSPACE.
func ValidWorkspace(name string) bool {
	return ids.ValidWorkspace(name) && !state.Suspicious(name)
}
