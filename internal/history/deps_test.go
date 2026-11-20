package history_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The record store is imported by the packages that run operations, never the other way
// round, and operations does not depend on the services only the API serves.
func TestDependencyDirection(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	for pkg, banned := range map[string][]string{
		"hclaro/internal/history":    {"hclaro/internal/assist", "hclaro/internal/operations", "hclaro/internal/projects"},
		"hclaro/internal/operations": {"hclaro/internal/assist", "hclaro/internal/discovery"},
	} {
		out, err := exec.Command("go", "list", "-deps", pkg).Output()
		if err != nil {
			t.Fatalf("go list %s: %v", pkg, err)
		}
		deps := strings.Fields(string(out))
		for _, b := range banned {
			if slices.Contains(deps, b) {
				t.Errorf("%s imports %s", pkg, b)
			}
		}
	}
}
