package gcpcli

import (
	"context"
	"strings"
	"testing"

	"hclaro/internal/awscli"
)

func list(cmd string) []string {
	return append(strings.Fields(cmd), "--format="+Formats[cmd], "--limit", "101", "--quiet")
}

func TestAllowedRefusesEverythingButReadOnlyCalls(t *testing.T) {
	ok := [][]string{
		{"--version"},
		{"config", "list", "--format=" + Formats["config list"]},
		{"auth", "list", "--format=" + Formats["auth list"]},
		{"projects", "describe", "hclaro-demo-1", "--format=" + Formats["projects describe"]},
	}
	for cmd := range Formats {
		if cmd != "projects describe" {
			ok = append(ok, list(cmd))
		}
	}
	for _, args := range ok {
		if !Allowed(args) {
			t.Errorf("refused %v", args)
		}
	}
	refused := [][]string{
		{},
		{"auth", "print-access-token"},
		{"auth", "application-default", "print-access-token"},
		{"auth", "login"},
		{"auth", "activate-service-account", "--key-file", "k.json"},
		{"auth", "list", "--format=json"},
		{"config", "set", "project", "x"},
		{"config", "list", "--format=json"},
		{"secrets", "versions", "access", "latest", "--secret", "x"},
		{"compute", "instances", "delete", "web-1"},
		{"compute", "instances", "create", "web-1"},
		{"compute", "instances", "start", "web-1"},
		{"compute", "instances", "get-serial-port-output", "web-1"},
		{"compute", "instances", "describe", "web-1"},
		{"compute", "ssh", "web-1"},
		{"compute", "networks", "delete", "default"},
		{"storage", "rm", "gs://x"},
		{"storage", "cp", "gs://x", "."},
		{"storage", "buckets", "delete", "gs://x"},
		{"storage", "buckets", "describe", "gs://x"},
		{"sql", "instances", "delete", "db"},
		{"sql", "users", "list", "--instance", "db"},
		{"projects", "describe", "Bad Project", "--format=" + Formats["projects describe"]},
		{"projects", "describe", "hclaro-demo-1; rm -rf /", "--format=" + Formats["projects describe"]},
		{"projects", "list", "--format=json"},
		{"iam", "service-accounts", "keys", "create", "x"},
		append(list("compute networks list"), "--impersonate-service-account", "x@y.iam.gserviceaccount.com"),
		append(list("compute networks list"), "--project", "other-project-1"),
		append(list("compute networks list"), "--billing-project", "other-project-1"),
		append(list("compute networks list"), "--filter", "name=x"),
		append(list("compute networks list"), "--access-token-file", "/etc/passwd"),
		append(list("compute networks list"), "--log-http"),
		append(list("compute networks list"), "--quiet"),
		append(list("compute networks list"), "--limit", "5"),
		{"compute", "networks", "list", "--format=" + Formats["compute networks list"] + ",metadata"},
		{"compute", "networks", "list", "--format=json(name,description)"},
		{"compute", "networks", "list", "--format=" + Formats["compute networks list"], "--limit", "0"},
		{"compute", "networks", "list", "--format=" + Formats["compute networks list"], "--limit"},
		{"compute", "networks", "list"},
		{"--version", "x"},
		{"version"},
	}
	for _, args := range refused {
		if Allowed(args) {
			t.Errorf("allowed %v", args)
		}
	}
	if _, err := (Exec{}).Run(context.Background(), nil, []string{"auth", "print-access-token"}); awscli.KindOf(err) != awscli.KindRefused {
		t.Fatalf("Exec ran a refused command: %v", err)
	}
}

func TestNoAllowedCommandCanReadATokenOrSecret(t *testing.T) {
	for cmd := range Formats {
		for _, bad := range []string{"token", "secret", "key", "password", "credential", "ssh", "serial", "metadata", "describe-"} {
			if strings.Contains(cmd, bad) {
				t.Errorf("allowlisted command %q mentions %q", cmd, bad)
			}
		}
	}
	for cmd, format := range Formats {
		for _, bad := range []string{"metadata", "password", "token", "secret", "rootPassword", "sslCert", "allowed", "sourceRanges", "authorizedNetworks"} {
			if strings.Contains(format, bad) {
				t.Errorf("%q format projects %q", cmd, bad)
			}
		}
	}
}

func TestEnvironDisablesPromptsAndKeepsCredentialSelection(t *testing.T) {
	env := Environ([]string{"CLOUDSDK_CORE_DISABLE_PROMPTS=0", "cloudsdk_core_verbosity=debug", "CLOUDSDK_CORE_LOG_HTTP=true", "CLOUDSDK_ACTIVE_CONFIG_NAME=prod", "CLOUDSDK_CORE_PROJECT=hclaro-demo-1", "HOME=/home/x"})
	joined := strings.Join(env, "\n")
	for _, bad := range []string{"PROMPTS=0", "=debug", "LOG_HTTP"} {
		if strings.Contains(joined, bad) {
			t.Errorf("environment keeps %q: %v", bad, env)
		}
	}
	for _, want := range []string{"CLOUDSDK_CORE_DISABLE_PROMPTS=1", "CLOUDSDK_CORE_VERBOSITY=error", "CLOUDSDK_ACTIVE_CONFIG_NAME=prod", "CLOUDSDK_CORE_PROJECT=hclaro-demo-1", "HOME=/home/x"} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment lost %q: %v", want, env)
		}
	}
}

func TestClassifyAndMessagesNeverEchoOutput(t *testing.T) {
	cases := map[string]string{
		"ERROR: There was a problem refreshing your current auth tokens: Reauthentication required.": awscli.KindExpired,
		"ERROR: You do not currently have an active account selected.":                              awscli.KindNoCredentials,
		"ERROR: (gcloud.x) HTTPError 403: Required 'compute.networks.list' permission":              awscli.KindAccessDenied,
		"ERROR: HTTPError 429: Quota exceeded (rateLimitExceeded)":                                  awscli.KindThrottled,
		"ERROR: Compute Engine API has not been used in project 1 before or it is disabled.":        awscli.KindRegionDisabled,
		"ERROR: Failed to connect to www.googleapis.com":                                            awscli.KindNetwork,
		"something DO_NOT_LEAK_HCLARO_TEST_SECRET":                                                  awscli.KindFailed,
	}
	for in, want := range cases {
		if got := Classify(in); got != want {
			t.Errorf("Classify(%q) = %q, want %q", in, got, want)
		}
	}
	for kind := range awscliKinds() {
		if m := Message(kind); strings.Contains(m, "AWS") || m == "" {
			t.Errorf("kind %s: message %q", kind, m)
		}
	}
}

func awscliKinds() map[string]bool {
	out := map[string]bool{}
	for kind := range messages {
		out[kind] = true
	}
	return out
}

func TestParseVersion(t *testing.T) {
	if v, ok := ParseVersion([]byte("Google Cloud SDK 480.0.0\nbq 2.1.4\n")); !ok || v != "480.0.0" {
		t.Fatalf("%q %v", v, ok)
	}
	for _, bad := range []string{"", "aws-cli/2.17.0", "Google Cloud SDK 120.0.0\n", "Google Cloud SDK x.y.z"} {
		if _, ok := ParseVersion([]byte(bad)); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}
