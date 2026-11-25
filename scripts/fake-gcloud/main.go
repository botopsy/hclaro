// This helper is a synthetic gcloud, not the Google Cloud CLI. Never install it on a normal
// PATH. It answers only the read-only calls Hclaro's discovery makes, from the recorded
// synthetic fixtures in fixtures/gcp, and never contacts Google Cloud.
//
// HCLARO_FAKE_GCLOUD selects behavior: unset (success), "old" (reports an unsupported SDK
// version), "expired" (every call fails like an expired login), "denied" (Cloud SQL reads
// are denied) and "throttle" (every list call is throttled). HCLARO_FAKE_GCLOUD_PROJECT
// replaces the project `config list` reports. HCLARO_FAKE_GCLOUD_SPY, when set, receives
// each argv as one NUL-separated line, prefixed with CONFIG=<CLOUDSDK_ACTIVE_CONFIG_NAME>,
// PROJECT=<CLOUDSDK_CORE_PROJECT> and KEYS=<whether GOOGLE_APPLICATION_CREDENTIALS is set>.
package main

import (
	"fmt"
	"os"
	"strings"

	"hclaro/internal/gcpcli/gcpclitest"
)

func fail(msg string, code int) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(code)
}

func main() {
	args := os.Args[1:]
	if spy := os.Getenv("HCLARO_FAKE_GCLOUD_SPY"); spy != "" {
		if f, err := os.OpenFile(spy, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			keys := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != ""
			fmt.Fprintf(f, "CONFIG=%s\x00PROJECT=%s\x00KEYS=%t\x00%s\n", os.Getenv("CLOUDSDK_ACTIVE_CONFIG_NAME"), os.Getenv("CLOUDSDK_CORE_PROJECT"), keys, strings.Join(args, "\x00"))
			f.Close()
		}
	}
	mode := os.Getenv("HCLARO_FAKE_GCLOUD")
	switch {
	case mode == "old" && len(args) == 1 && args[0] == "--version":
		fmt.Print("Google Cloud SDK 120.0.0\n")
		return
	case mode == "expired" && len(args) > 0 && args[0] != "--version":
		fail("ERROR: (gcloud.compute.networks.list) There was a problem refreshing your current auth tokens: Reauthentication required. Please run: gcloud auth login", 1)
	case mode == "throttle" && len(args) > 2 && args[len(args)-1] == "--quiet":
		fail("ERROR: (gcloud) HTTPError 429: Quota exceeded for quota metric DO_NOT_LEAK_HCLARO_TEST_SECRET (rateLimitExceeded)", 1)
	case mode == "denied" && len(args) > 0 && args[0] == "sql":
		fail("ERROR: (gcloud.sql.instances.list) HTTPError 403: The caller does not have permission DO_NOT_LEAK_HCLARO_TEST_SECRET", 1)
	}
	out, err := gcpclitest.Answer(args, os.Getenv("HCLARO_FAKE_GCLOUD_PROJECT"), nil)
	if err != nil {
		fail("ERROR: unsupported fake command", 2)
	}
	os.Stdout.Write(out)
}
