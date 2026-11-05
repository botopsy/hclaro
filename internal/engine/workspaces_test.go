package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestWorkspaceArgsAreFixedAndNamesCannotBeFlags(t *testing.T) {
	for _, a := range []WorkspaceAction{WorkspaceNew, WorkspaceSelect, WorkspaceDelete} {
		args, err := WorkspaceArgs(a, "staging")
		if err != nil || !slices.Equal(args, []string{"workspace", string(a), "staging"}) {
			t.Fatalf("%v %v", args, err)
		}
	}
	for _, name := range []string{"-force", "--force", "-or-create", "-lock=false", "-state=x", "", "a b", "../prod", "dev/x", "dev;rm", "dev\n", strings.Repeat("a", 91), "_x", "x\x00"} {
		if _, err := WorkspaceArgs(WorkspaceNew, name); !errors.Is(err, ErrInvalidWorkspaceArg) {
			t.Errorf("accepted %q", name)
		}
	}
	for _, a := range []WorkspaceAction{"", "show", "list", "new -force", "NEW"} {
		if _, err := WorkspaceArgs(a, "staging"); err == nil {
			t.Errorf("accepted action %q", a)
		}
	}
	if !ValidArgWorkspace("9to5") || !ValidArgWorkspace("dev_eu-1") || ValidArgWorkspace("-dev") {
		t.Fatal("argument name rules")
	}
}

func TestChangeWorkspaceDropsTFWorkspaceAndRefusesHiddenFlags(t *testing.T) {
	c := initCLI(t)
	var out []string
	err := c.ChangeWorkspace(context.Background(), WorkspaceNew, "staging", func(stream string, b []byte) { out = append(out, string(b)) })
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("%v", err)
	}
	text := strings.Join(out, "\n")
	if !strings.Contains(text, "argv=workspace new staging\n") || !strings.Contains(text, "workspace=<unset> pinned=yes log=OFF") {
		t.Fatalf("%s", text)
	}
	for _, env := range []string{"TF_CLI_ARGS=-force", "TF_CLI_ARGS_workspace=-lock=false", "TF_CLI_ARGS_workspace=-no-color -force"} {
		c := initCLI(t, env)
		if err := c.ChangeWorkspace(context.Background(), WorkspaceDelete, "staging", func(string, []byte) {}); !errors.Is(err, ErrUnsafeArgs) {
			t.Fatalf("%s: %v", env, err)
		}
	}
	if err := initCLI(t, "TF_CLI_ARGS_workspace=-no-color").ChangeWorkspace(context.Background(), WorkspaceSelect, "dev", func(string, []byte) {}); errors.Is(err, ErrUnsafeArgs) {
		t.Fatal("-no-color refused")
	}
	if err := c.ChangeWorkspace(context.Background(), WorkspaceDelete, "-force", func(string, []byte) { t.Fatal("ran") }); !errors.Is(err, ErrInvalidWorkspaceArg) {
		t.Fatal(err)
	}
}

func TestWorkspaceStateUsesChildWorkspaceOnly(t *testing.T) {
	c := initCLI(t)
	b, err := c.WorkspaceState(context.Background(), "staging")
	// The helper exits 1 after printing, so only the refusal path is checked here; the
	// environment it saw is asserted through the synthetic CLI in the projects tests.
	if err == nil || b != nil {
		t.Fatalf("%s %v", b, err)
	}
	if _, err := c.WorkspaceState(context.Background(), "a b"); !errors.Is(err, ErrInvalidWorkspaceArg) {
		t.Fatal(err)
	}
}

func TestClassifyWorkspace(t *testing.T) {
	for text, want := range map[string]string{
		`Workspace "dev" already exists`:                          "workspace_exists",
		`Workspace "qa" doesn't exist.`:                           "workspace_missing",
		`Workspace "dev" is your active workspace.`:               "workspace_active",
		"Cannot delete the default workspace":                     "workspace_default",
		`Workspace "qa" is currently tracking the following`:      "workspace_not_empty",
		"workspaces not supported":                                "workspaces_unsupported",
		"The selected workspace is overridden using TF_WORKSPACE": "workspace_overridden",
		"Error acquiring the state lock":                          "state_locked",
		"No valid credential sources found":                       "credentials",
		"something else":                                          "",
	} {
		if got := ClassifyWorkspace(text); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
}
