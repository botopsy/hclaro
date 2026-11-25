// This helper is a synthetic az, not the Azure CLI. Never install it on a normal PATH. It
// answers only the read-only calls Hclaro's discovery makes, from the recorded synthetic
// fixtures in fixtures/azure, and never contacts Azure.
//
// HCLARO_FAKE_AZ selects behavior: unset (success), "old" (reports an unsupported CLI
// version), "expired" (every call fails like an expired login), "denied" (disk reads are
// denied) and "throttle" (every list call is throttled). HCLARO_FAKE_AZ_SUBSCRIPTION replaces
// the subscription the fixtures belong to. HCLARO_FAKE_AZ_SPY, when set, receives each argv as
// one NUL-separated line, prefixed with SUB=<ARM_SUBSCRIPTION_ID> and
// SECRET=<whether ARM_CLIENT_SECRET is set>.
package main

import (
	"fmt"
	"os"
	"strings"

	"hclaro/internal/azcli/azclitest"
)

func fail(msg string, code int) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(code)
}

func main() {
	args := os.Args[1:]
	if spy := os.Getenv("HCLARO_FAKE_AZ_SPY"); spy != "" {
		if f, err := os.OpenFile(spy, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			secret := os.Getenv("ARM_CLIENT_SECRET") != ""
			fmt.Fprintf(f, "SUB=%s\x00SECRET=%t\x00%s\n", os.Getenv("ARM_SUBSCRIPTION_ID"), secret, strings.Join(args, "\x00"))
			f.Close()
		}
	}
	mode := os.Getenv("HCLARO_FAKE_AZ")
	switch {
	case mode == "old" && len(args) == 1 && args[0] == "--version":
		fmt.Print("azure-cli                         2.10.0\n")
		return
	case mode == "expired" && len(args) > 0 && args[0] != "--version":
		fail("ERROR: AADSTS70043: The refresh token has expired. Interactive authentication is needed. Run: az login", 1)
	case mode == "throttle" && len(args) > 2 && args[1] != "show":
		fail("ERROR: (TooManyRequests) Too many requests DO_NOT_LEAK_HCLARO_TEST_SECRET", 1)
	case mode == "denied" && len(args) > 0 && args[0] == "disk":
		fail("ERROR: (AuthorizationFailed) The client does not have authorization DO_NOT_LEAK_HCLARO_TEST_SECRET", 1)
	}
	out, err := azclitest.Answer(args, os.Getenv("HCLARO_FAKE_AZ_SUBSCRIPTION"), nil)
	if err != nil {
		fail("ERROR: unsupported fake command", 2)
	}
	os.Stdout.Write(out)
}
