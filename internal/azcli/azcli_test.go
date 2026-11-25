package azcli

import (
	"context"
	"strings"
	"testing"

	"hclaro/internal/awscli"
)

const sub = "11111111-1111-1111-1111-111111111111"

func list(cmd string) []string {
	return append(strings.Fields(cmd), "--query", Queries[cmd], "-o", "json", "--only-show-errors")
}

func TestAllowedRefusesEverythingButReadOnlyCalls(t *testing.T) {
	ok := [][]string{{"--version"}, append(list("vm list"), "--subscription", sub)}
	for cmd := range Queries {
		ok = append(ok, list(cmd))
	}
	for _, args := range ok {
		if !Allowed(args) {
			t.Errorf("refused %v", args)
		}
	}
	refused := [][]string{
		{},
		{"account", "get-access-token"},
		{"account", "get-access-token", "--query", "accessToken"},
		{"account", "show", "--query", "accessToken", "-o", "json"},
		{"login"},
		{"login", "--service-principal", "-u", "x", "-p", "y"},
		{"account", "set", "--subscription", sub},
		{"account", "clear"},
		{"keyvault", "secret", "show", "--name", "x"},
		{"keyvault", "secret", "list", "--vault-name", "x"},
		{"vm", "delete", "-n", "web-1"},
		{"vm", "create", "-n", "web-1"},
		{"vm", "start", "-n", "web-1"},
		{"vm", "run-command", "invoke"},
		{"vm", "show", "-n", "web-1"},
		{"vm", "list-ip-addresses"},
		{"group", "delete", "-n", "rg"},
		{"storage", "account", "keys", "list", "-n", "x"},
		{"storage", "account", "show-connection-string", "-n", "x"},
		{"storage", "blob", "list"},
		{"network", "vnet", "subnet", "list"},
		{"network", "nsg", "rule", "list"},
		{"ad", "sp", "credential", "reset"},
		{"rest", "--method", "get", "--url", "https://management.azure.com/"},
		{"extension", "add", "-n", "x"},
		{"config", "set", "x=y"},
		append(list("vm list"), "--debug"),
		append(list("vm list"), "--verbose"),
		append(list("vm list"), "--ids", "x"),
		append(list("vm list"), "--resource-group", "rg"),
		append(list("vm list"), "--show-details"),
		append(list("vm list"), "--subscription", "not-a-guid"),
		append(list("vm list"), "--subscription", sub+"; rm -rf /"),
		append(list("vm list"), "--subscription", sub, "--subscription", sub),
		append(list("vm list"), "--only-show-errors"),
		append(list("vm list"), "--query", Queries["vm list"]),
		{"vm", "list", "--query", Queries["vm list"] + ",osProfile:osProfile", "-o", "json"},
		{"vm", "list", "--query", "[].{id:id}", "-o", "json"},
		{"vm", "list", "--query", Queries["vm list"], "-o", "tsv"},
		{"vm", "list", "--query", Queries["vm list"]},
		{"vm", "list", "-o", "json"},
		{"vm", "list"},
		{"--version", "x"},
		{"version"},
	}
	for _, args := range refused {
		if Allowed(args) {
			t.Errorf("allowed %v", args)
		}
	}
	if _, err := (Exec{}).Run(context.Background(), nil, []string{"account", "get-access-token"}); awscli.KindOf(err) != awscli.KindRefused {
		t.Fatalf("Exec ran a refused command: %v", err)
	}
}

func TestNoAllowedCommandCanReadATokenOrSecret(t *testing.T) {
	for cmd, query := range Queries {
		for _, bad := range []string{"token", "secret", "key", "password", "credential", "ssh", "connection", "rule", "run-command", "show-", "get-"} {
			if strings.Contains(cmd, bad) {
				t.Errorf("allowlisted command %q mentions %q", cmd, bad)
			}
		}
		for _, bad := range []string{"osProfile", "password", "token", "secret", "Key", "customData", "securityRules", "dnsServers", "primaryEndpoints", "accessToken", "properties"} {
			if strings.Contains(query, bad) {
				t.Errorf("%q query projects %q", cmd, bad)
			}
		}
		// az is az.cmd on Windows: nothing cmd.exe or a shell would interpret.
		if strings.ContainsAny(query, " \t\"'`&|<>^%$;()!*?\\") {
			t.Errorf("%q query has a shell metacharacter: %s", cmd, query)
		}
	}
}

func TestSubscriptionComesFromTheComposedEnvironment(t *testing.T) {
	cmd := list("vm list")
	got := withSubscription([]string{"A=b", "ARM_SUBSCRIPTION_ID=" + sub}, cmd)
	if !Allowed(got) || got[len(got)-2] != "--subscription" || got[len(got)-1] != sub {
		t.Fatalf("%v", got)
	}
	for name, env := range map[string][]string{"none": nil, "invalid": {"ARM_SUBSCRIPTION_ID=x; rm"}, "empty": {"ARM_SUBSCRIPTION_ID="}} {
		if got := withSubscription(env, cmd); len(got) != len(cmd) {
			t.Errorf("%s: %v", name, got)
		}
	}
	// The last value wins (Compose appends), and account list takes no subscription.
	if got := withSubscription([]string{"ARM_SUBSCRIPTION_ID=x", "ARM_SUBSCRIPTION_ID=" + sub}, cmd); got[len(got)-1] != sub {
		t.Errorf("%v", got)
	}
	if got := withSubscription([]string{"ARM_SUBSCRIPTION_ID=" + sub}, list("account list")); len(got) != len(list("account list")) {
		t.Errorf("%v", got)
	}
	if got := withSubscription([]string{"ARM_SUBSCRIPTION_ID=" + sub}, []string{"--version"}); len(got) != 1 {
		t.Errorf("%v", got)
	}
}

func TestEnvironDisablesPromptsAndKeepsCredentialSelection(t *testing.T) {
	env := Environ([]string{"AZURE_CORE_OUTPUT=table", "azure_core_collect_telemetry=true", "AZURE_CORE_NO_COLOR=false", "ARM_SUBSCRIPTION_ID=" + sub, "AZURE_CONFIG_DIR=/home/x/.azure", "ARM_CLIENT_ID=app", "HOME=/home/x"})
	joined := strings.Join(env, "\n")
	for _, bad := range []string{"=table", "TELEMETRY=true", "NO_COLOR=false"} {
		if strings.Contains(joined, bad) {
			t.Errorf("environment keeps %q: %v", bad, env)
		}
	}
	for _, want := range []string{"AZURE_CORE_OUTPUT=json", "AZURE_CORE_COLLECT_TELEMETRY=false", "AZURE_CORE_DISABLE_CONFIRM_PROMPT=true", "ARM_SUBSCRIPTION_ID=" + sub, "AZURE_CONFIG_DIR=/home/x/.azure", "ARM_CLIENT_ID=app", "HOME=/home/x"} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment lost %q: %v", want, env)
		}
	}
}

func TestClassifyAndMessagesNeverEchoOutput(t *testing.T) {
	cases := map[string]string{
		"ERROR: AADSTS70043: The refresh token has expired. Interactive authentication is needed.": awscli.KindExpired,
		"ERROR: Please run 'az login' to setup account.":                                           awscli.KindNoCredentials,
		"ERROR: The subscription of 'x' doesn't exist in cloud 'AzureCloud'.":                      awscli.KindProfileNotFound,
		"ERROR: (AuthorizationFailed) The client does not have authorization to perform action":    awscli.KindAccessDenied,
		"ERROR: (TooManyRequests) The request is being throttled":                                  awscli.KindThrottled,
		"ERROR: (MissingSubscriptionRegistration) The subscription is not registered":              awscli.KindRegionDisabled,
		"ERROR: HTTPSConnectionPool: Max retries exceeded":                                         awscli.KindNetwork,
		"something DO_NOT_LEAK_HCLARO_TEST_SECRET":                                                 awscli.KindFailed,
	}
	for in, want := range cases {
		if got := Classify(in); got != want {
			t.Errorf("Classify(%q) = %q, want %q", in, got, want)
		}
	}
	for kind := range messages {
		if m := Message(kind); strings.Contains(m, "AWS") || m == "" || strings.Contains(m, "gcloud") {
			t.Errorf("kind %s: message %q", kind, m)
		}
	}
}

func TestParseVersion(t *testing.T) {
	if v, ok := ParseVersion([]byte("azure-cli                         2.60.0\n\ncore  2.60.0\n")); !ok || v != "2.60.0" {
		t.Fatalf("%q %v", v, ok)
	}
	for _, bad := range []string{"", "aws-cli/2.17.0", "azure-cli 2.10.0\n", "azure-cli 1.99.0", "azure-cli x.y.z", "azure-cli 99999.1.1"} {
		if _, ok := ParseVersion([]byte(bad)); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}
