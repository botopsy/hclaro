package engine

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestParseWorkspaceList(t *testing.T) {
	list, err := ParseWorkspaceList([]byte("  default\r\n* dev\n  prod\n  dev\n  bad name\n  AKIAABCDEFGHIJKLMNOP\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(list.Names, []string{"default", "dev", "prod"}) || list.Current != "dev" || list.Hidden != 3 {
		t.Fatalf("%+v", list)
	}
	if _, err := ParseWorkspaceList([]byte("\n")); err == nil {
		t.Fatal("empty list accepted")
	}
	if _, err := ParseWorkspaceList([]byte(strings.Repeat("  w\n", MaxWorkspaces) + "  w1\n")); err != nil {
		t.Fatal("duplicates should not count towards the limit")
	}
}

func envOf(t *testing.T, c *CLI, drop bool, selected string) []string {
	t.Helper()
	cmd, err := c.commandEnv(context.Background(), drop, selected, "show", "-json")
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, item := range cmd.Env {
		if strings.HasPrefix(item, "TF_WORKSPACE=") {
			out = append(out, item)
		}
	}
	return out
}

func TestSelectedWorkspaceIsPerChild(t *testing.T) {
	t.Setenv("TF_WORKSPACE", "inherited")
	launch := &CLI{Name: "terraform", Executable: "terraform", Root: t.TempDir()}
	cmd, _ := launch.command(context.Background(), "show", "-json")
	if !slices.Contains(cmd.Env, "TF_WORKSPACE=inherited") {
		t.Fatal("launch project must keep the inherited workspace")
	}
	hub := &CLI{Name: "terraform", Executable: "terraform", Root: t.TempDir(), SelectedWorkspace: "prod"}
	cmd, _ = hub.command(context.Background(), "show", "-json")
	got := []string{}
	for _, item := range cmd.Env {
		if strings.HasPrefix(item, "TF_WORKSPACE=") {
			got = append(got, item)
		}
	}
	if !slices.Equal(got, []string{"TF_WORKSPACE=prod"}) {
		t.Fatalf("%v", got)
	}
	if got := envOf(t, hub, true, ""); len(got) != 0 {
		t.Fatalf("listing env %v", got)
	}
	bad := &CLI{Name: "terraform", Executable: "terraform", Root: t.TempDir(), SelectedWorkspace: "a b"}
	if _, err := bad.command(context.Background(), "show", "-json"); err == nil {
		t.Fatal("invalid workspace accepted")
	}
}
