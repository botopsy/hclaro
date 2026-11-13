package engine

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"hclaro/fixtures"
)

func TestToolArgsAreFixedAndNeverWrite(t *testing.T) {
	want := map[Tool][][]string{
		ToolValidate:  {{"validate", "-json"}, {"validate", "-no-color"}},
		ToolFormat:    {{"fmt", "-check", "-no-color"}, {"fmt", "-check", "-no-color", "-recursive"}},
		ToolProviders: {{"providers"}},
		ToolOutputs:   {{"output", "-json"}},
	}
	for tool, argvs := range want {
		got := [][]string{}
		for _, json := range []bool{true, false} {
			for _, recursive := range []bool{false, true} {
				args := ToolArgs(tool, json, recursive)
				if !slices.ContainsFunc(got, func(a []string) bool { return slices.Equal(a, args) }) {
					got = append(got, args)
				}
				for _, a := range args {
					for _, banned := range []string{"-write", "-diff", "-upgrade", "-chdir", "-state", "-lock", "-raw", "init", "apply"} {
						if strings.HasPrefix(a, banned) && !(a == "-check") {
							t.Fatalf("%s in %v", banned, args)
						}
					}
				}
				// fmt always checks; a plain fmt would rewrite files.
				if tool == ToolFormat && !slices.Contains(args, "-check") {
					t.Fatalf("fmt without -check: %v", args)
				}
			}
		}
		for _, w := range argvs {
			if !slices.ContainsFunc(got, func(a []string) bool { return slices.Equal(a, w) }) {
				t.Fatalf("%s: %v missing from %v", tool, w, got)
			}
		}
		if len(got) != len(argvs) {
			t.Fatalf("%s: unexpected argv %v", tool, got)
		}
	}
	if ToolArgs("fmt", false, false) != nil || ValidTool("fmt") || ValidTool("init") || !ValidTool(ToolOutputs) {
		t.Fatal("only the four tools are valid")
	}
	if WorkspaceDependent(ToolValidate) || WorkspaceDependent(ToolFormat) || !WorkspaceDependent(ToolProviders) || !WorkspaceDependent(ToolOutputs) {
		t.Fatal("workspace dependence")
	}
}

func TestToolsWorkspaceEnvironmentAndHiddenFlags(t *testing.T) {
	// Configuration-only tools get no TF_WORKSPACE, whatever was inherited or selected.
	c := initCLI(t)
	c.SelectedWorkspace = "dev"
	out, err := c.Validate(context.Background(), true)
	if err != nil || out.ExitCode != 1 {
		t.Fatalf("%v %+v", err, out)
	}
	if text := string(out.Stdout); !strings.Contains(text, "argv=validate -json\n") || !strings.Contains(text, "workspace=<unset> pinned=yes log=OFF") {
		t.Fatalf("%s", text)
	}
	out, _ = c.FormatCheck(context.Background(), true)
	if text := string(out.Stdout); !strings.Contains(text, "argv=fmt -check -no-color -recursive\n") || !strings.Contains(text, "workspace=<unset>") {
		t.Fatalf("%s", text)
	}
	// State readers get the context's workspace, never the inherited one.
	out, _ = c.Providers(context.Background())
	if text := string(out.Stdout); !strings.Contains(text, "argv=providers\n") || !strings.Contains(text, "workspace=dev pinned=yes") {
		t.Fatalf("%s", text)
	}
	// Output stdout is never returned, even on failure.
	_, err = c.OutputsMetadata(context.Background())
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("%v", err)
	}
	c.SelectedWorkspace = ""
	if _, err := c.Providers(context.Background()); !errors.Is(err, ErrToolWorkspace) {
		t.Fatalf("providers without a workspace: %v", err)
	}
	if _, err := c.OutputsMetadata(context.Background()); !errors.Is(err, ErrToolWorkspace) {
		t.Fatalf("outputs without a workspace: %v", err)
	}
	for env, tool := range map[string]func(*CLI) error{
		"TF_CLI_ARGS=-recursive":       func(c *CLI) error { _, err := c.Validate(context.Background(), true); return err },
		"TF_CLI_ARGS_fmt=-write=true":  func(c *CLI) error { _, err := c.FormatCheck(context.Background(), false); return err },
		"TF_CLI_ARGS_validate=-json=x": func(c *CLI) error { _, err := c.Validate(context.Background(), false); return err },
		"TF_CLI_ARGS_output=-raw":      func(c *CLI) error { _, err := c.OutputsMetadata(context.Background()); return err },
		"TF_CLI_ARGS_providers=lock":   func(c *CLI) error { _, err := c.Providers(context.Background()); return err },
	} {
		c := initCLI(t, env)
		c.SelectedWorkspace = "dev"
		if err := tool(c); !errors.Is(err, ErrUnsafeArgs) {
			t.Fatalf("%s: %v", env, err)
		}
	}
	c = initCLI(t, "TF_CLI_ARGS_fmt=-no-color")
	if _, err := c.FormatCheck(context.Background(), false); err != nil {
		t.Fatalf("-no-color refused: %v", err)
	}
}

func TestParseOutputsKeepsMetadataOnly(t *testing.T) {
	o, err := ParseOutputs(fixtures.Outputs)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(o)
	for _, secret := range []string{fixtures.Canary, "plain-unflagged-secret-7f3a", "network-dev-logs", "example.invalid", "value"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("%q in %s", secret, b)
		}
	}
	want := []OutputMeta{
		{Name: "bucket_names", Type: "list(string)"},
		{Name: "connection_string", Type: "string"},
		{Name: "db_password", Type: "string", Sensitive: true},
		{Name: "endpoints", Type: "object (2 attributes)"},
	}
	if !slices.Equal(o.Items, want) || o.Hidden != 1 {
		t.Fatalf("%+v", o)
	}
	for _, bad := range []string{"", "null", "[]", "not json", `{"a":`} {
		if _, err := ParseOutputs([]byte(bad)); !errors.Is(err, ErrUnreadableOutputs) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if o, err := ParseOutputs([]byte("{}")); err != nil || len(o.Items) != 0 {
		t.Fatalf("empty outputs: %+v %v", o, err)
	}
	for raw, want := range map[string]string{
		`"bool"`: "bool", `["set",["map","number"]]`: "set(map(number))", `["tuple",["string","bool"]]`: "tuple (2 elements)",
		`"weird"`: "unknown", `["list"]`: "unknown", ``: "unknown",
	} {
		if got := TypeLabel(json.RawMessage(raw)); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
}

func TestOutputsFromNeverReturnsRawOutput(t *testing.T) {
	_, err := outputsFrom(Capture{Stdout: fixtures.Outputs, Stderr: []byte("Error: Backend initialization required, please run \"terraform init\"\npassword=" + fixtures.Canary), ExitCode: 1})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Hint != "init_required" || strings.Contains(err.Error(), fixtures.Canary) {
		t.Fatalf("%v", err)
	}
	if _, err := outputsFrom(Capture{Stdout: fixtures.Outputs, Truncated: true}); !errors.Is(err, ErrUnreadableOutputs) {
		t.Fatalf("truncated: %v", err)
	}
}

func TestClassifyTool(t *testing.T) {
	for text, want := range map[string]string{
		"flag provided but not defined: -json":                                "unsupported_flag",
		"Error: Unsupported Terraform Core version":                           "version_mismatch",
		"Error: Missing required provider":                                    "plugins_missing",
		"Error: Inconsistent dependency lock file":                            "plugins_missing",
		"Error: Module not installed":                                         "init_required",
		`Error: Backend initialization required, please run "terraform init"`: "init_required",
		"Error: Currently selected workspace \"qa\" does not exist":           "workspace_missing",
		"Error acquiring the state lock":                                      "state_locked",
		"No valid credential sources found":                                   "credentials",
		"Error: Unsupported argument":                                         "",
	} {
		if got := ClassifyTool(text); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
}

func TestClassifyToolLockReleaseAndAssociation(t *testing.T) {
	for text, want := range map[string]string{
		"Error acquiring the state lock\nLock Info:":                  "state_locked",
		"Error releasing the state lock\nLock Info:":                  "",
		"Releasing state lock. This may take a few moments...\nError": "",
		"Error: aws_route_table_association.a: InvalidRouteTableID":    "",
	} {
		if got := ClassifyTool(text); got != want {
			t.Errorf("%q: %q want %q", text, got, want)
		}
	}
}
