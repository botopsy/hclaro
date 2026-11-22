package ids

import (
	"strings"
	"testing"
)

func TestShapes(t *testing.T) {
	for s, want := range map[string]bool{strings.Repeat("a", 32): true, strings.Repeat("0", 31): false, strings.Repeat("A", 32): false, strings.Repeat("a", 32) + "\n": false, "": false} {
		if Valid(s) != want {
			t.Errorf("Valid(%q) = %v", s, !want)
		}
	}
	for s, want := range map[string]bool{"default": true, "prod-eu_1": true, strings.Repeat("w", 90): true, strings.Repeat("w", 91): false, "": false, "a/b": false, "a b": false, "../x": false} {
		if ValidWorkspace(s) != want {
			t.Errorf("ValidWorkspace(%q) = %v", s, !want)
		}
	}
}

func TestNew(t *testing.T) {
	a, b := New(), New()
	if !Valid(a) || !Valid(b) || a == b {
		t.Fatalf("%q %q", a, b)
	}
}
