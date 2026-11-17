// This helper is a synthetic aws-vault, not aws-vault. Never install it on a normal PATH.
// It supports `--version` and `exec <profile> -- <command> [args...]`: it records the argv,
// then runs the command with planted canary credentials in its environment (as aws-vault
// would pass temporary credentials) and exits with its exit code. It never contacts AWS.
//
// HCLARO_FAKE_VAULT_SPY, when set, receives each argv as one NUL-separated line.
// HCLARO_FAKE_VAULT=locked fails like a locked keychain that needs a prompt; "mfa" behaves
// like aws-vault 6.3.1 with an MFA profile: it prints the prompt on stderr and reads the code
// from stdin, failing with EOF when stdin is empty.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const canary = "DO_NOT_LEAK_HCLARO_TEST_SECRET"

func main() {
	args := os.Args[1:]
	if path := os.Getenv("HCLARO_FAKE_VAULT_SPY"); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "%s\n", strings.Join(args, "\x00"))
			f.Close()
		}
	}
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(os.Stderr, "v7.2.0-synthetic")
		return
	}
	if len(args) < 4 || args[0] != "exec" || args[2] != "--" {
		fmt.Fprintln(os.Stderr, "aws-vault: error: synthetic aws-vault supports exec <profile> -- <command> only")
		os.Exit(2)
	}
	if os.Getenv("HCLARO_FAKE_VAULT") == "mfa" {
		fmt.Fprint(os.Stderr, "Enter token for arn:aws:iam::111122223333:mfa/test: ")
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			fmt.Fprintln(os.Stderr, "aws-vault: error: exec: Failed to get credentials for "+args[1]+": "+err.Error())
			os.Exit(1)
		}
	}
	if os.Getenv("HCLARO_FAKE_VAULT") == "locked" {
		fmt.Fprintln(os.Stderr, "aws-vault: error: exec: Failed to get credentials for "+args[1]+": keyring is locked; a password prompt needs a terminal "+canary)
		os.Exit(1)
	}
	cmd := exec.Command(args[3], args[4:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "AWS_VAULT="+args[1], "AWS_ACCESS_KEY_ID=AKIA_TEST_CANARY", "AWS_SECRET_ACCESS_KEY="+canary, "AWS_SESSION_TOKEN=SESSION_TOKEN_TEST_CANARY")
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "aws-vault: error: exec: "+err.Error())
		os.Exit(1)
	}
}
