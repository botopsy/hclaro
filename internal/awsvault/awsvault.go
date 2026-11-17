// Package awsvault carries an execution profile's choice of the aws-vault credential adapter
// from the composed child environment to the few places that start a process with it (the
// engine adapter, the STS identity probe and the read-only AWS CLI runner).
//
// The choice travels as Marker inside the composed environment because that environment is
// already what every operation pins, reviews and fingerprints. Marker is set only by
// profiles.Compose, which removes any inherited copy; Hclaro also removes it from its own
// process environment at startup. Split removes it before a child starts, so it never
// reaches aws-vault, Terraform or the AWS CLI.
package awsvault

import (
	"errors"
	"os/exec"
	"regexp"
	"strings"
)

// Marker names the aws-vault profile a composed environment runs through.
const Marker = "HCLARO_AWS_VAULT_PROFILE"

// ErrMissing means the profile selects aws-vault but it is not on PATH.
var ErrMissing = errors.New("aws-vault was not found on PATH. Install aws-vault, or choose the AWS CLI method for this profile")

var profileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+=@,-]{0,63}$`)

// ValidProfile reports whether name is a safe aws-vault profile name: it cannot start with -
// and has no spaces, quotes or shell characters.
func ValidProfile(name string) bool { return profileName.MatchString(name) }

// Split returns the aws-vault profile named in environ ("" for none) and environ without the
// marker. Only an environment composed for an execution profile may be passed; Hclaro's own
// environment never carries the marker.
func Split(environ []string) (profile string, rest []string) {
	rest = make([]string, 0, len(environ))
	for _, item := range environ {
		key, value, _ := strings.Cut(item, "=")
		if strings.EqualFold(key, Marker) {
			profile = value
			continue
		}
		rest = append(rest, item)
	}
	return profile, rest
}

// Strip removes the marker from environ without honouring it.
func Strip(environ []string) []string {
	_, rest := Split(environ)
	return rest
}

// Prefix is the argv that runs a command through aws-vault: [aws-vault, exec, <profile>, --].
// The vault profile and the command are passed as separate arguments; no shell runs.
func Prefix(profile string) ([]string, error) {
	if !ValidProfile(profile) {
		return nil, errors.New("the aws-vault profile name is not valid")
	}
	vault, err := exec.LookPath("aws-vault")
	if err != nil {
		return nil, ErrMissing
	}
	return []string{vault, "exec", profile, "--"}, nil
}

// Command returns the executable and arguments that run exe with args, through aws-vault
// when profile is set.
func Command(profile, exe string, args []string) (string, []string, error) {
	if profile == "" {
		return exe, args, nil
	}
	prefix, err := Prefix(profile)
	if err != nil {
		return "", nil, err
	}
	return prefix[0], append(append(prefix[1:], exe), args...), nil
}

// Display is the reviewed form of a command run through aws-vault, for command previews and
// history: "aws-vault exec <profile> --" followed by the command. Without a profile it is
// the command unchanged.
func Display(profile string, command []string) []string {
	if profile == "" {
		return command
	}
	return append([]string{"aws-vault", "exec", profile, "--"}, command...)
}
