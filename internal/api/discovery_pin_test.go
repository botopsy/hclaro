package api

import (
	"errors"
	"testing"

	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

// B08: a launch discovery read that names no fingerprint (the scan and options GETs) pins the
// profile as it is now; a request that contacts AWS must carry the reviewed fingerprint.
func TestLaunchDiscoveryPinNeedsReviewOnlyWhenChecked(t *testing.T) {
	m := operations.New(workspace.New(workspace.Info{Workspace: "default"}), nil, t.TempDir(), operations.Deps{})
	m.Binder = func(string) (operations.Binding, error) {
		return operations.Binding{Context: operations.ExecutionContext{ProfileID: "p", ProfileName: "dev", Fingerprint: "fp"}}, nil
	}
	if pin, _, err := discoveryPin(m, false, "p", ""); err != nil || pin.ProfileID != "p" || pin.ContextFingerprint != "fp" {
		t.Fatalf("unchecked read: %+v %v", pin, err)
	}
	var rejected *operations.RejectError
	if _, _, err := discoveryPin(m, true, "p", ""); !errors.As(err, &rejected) || rejected.Code != "context_changed" {
		t.Fatalf("checked without review: %v", err)
	}
	if _, _, err := discoveryPin(m, true, "p", "fp"); err != nil {
		t.Fatalf("checked with review: %v", err)
	}
}
