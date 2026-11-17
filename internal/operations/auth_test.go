package operations

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/preflight"
	"hclaro/internal/profiles"
)

// M20: the execution profile's expected account guards apply, a sign-in after the preflight
// refuses its binding, and aws-vault shows in every reviewed command.

func authPlan(t *testing.T, in profiles.Input) (*Manager, *engine.Fake, string) {
	t.Helper()
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions, ApplyLines: lines(fixtures.ApplyLog)}
	m, _, store, _ := profileSetup(t, fake, []string{"PATH=/bin", "AWS_ACCESS_KEY_ID=AKIA_TEST_CANARY"})
	m.Reload = func(context.Context) {}
	p, err := store.Create(testProject, in)
	if err != nil {
		t.Fatal(err)
	}
	op, err := m.StartPlanWith(PlanStart{ProfileID: p.ID, ContextFingerprint: reviewedFingerprint(t, m, p.ID)})
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	if done.Status != Succeeded {
		t.Fatalf("%+v", done)
	}
	return m, fake, done.PlanID
}

func TestExpectedAccountMismatchNeverApplies(t *testing.T) {
	m, fake, id := authPlan(t, profiles.Input{Name: "prod", AWSProfile: "prod", AWSExpectedAccount: "111122223333"})
	sts := &identityAt{id: verifiedAs("444455556666")}
	m.IdentityProbe = sts.probe
	// Unverified: blocked, fail closed.
	if r := runPreflight(t, m, id, PreflightRequest{}); !r.Blocked || mustCheck(t, r, preflight.CheckAWSExpected).Status != preflight.Unknown {
		t.Fatalf("%+v", r.Checks)
	}
	// Verified in another account: blocked even though STS succeeded.
	r := runPreflight(t, m, id, PreflightRequest{VerifyIdentity: true})
	if !r.Blocked || r.Binding != "" || mustCheck(t, r, preflight.CheckAWSExpected).Status != preflight.Fail {
		t.Fatalf("%+v", r.Checks)
	}
	if _, err := m.Approve(id, ApprovalRequest{Binding: r.Binding, Acknowledged: r.Acknowledge}); err == nil {
		t.Fatal("approved a mismatched account")
	}
	// The right account passes; switching accounts before apply refuses the apply.
	sts.set(verifiedAs("111122223333"))
	r = runPreflight(t, m, id, PreflightRequest{VerifyIdentity: true})
	if r.Blocked {
		t.Fatalf("%+v", r.Checks)
	}
	a, err := m.Approve(id, ApprovalRequest{Binding: r.Binding, Acknowledged: r.Acknowledge})
	if err != nil {
		t.Fatal(err)
	}
	sts.set(verifiedAs("444455556666"))
	if _, err := m.StartApply(id, a.Token, a.Phrase); rejected(err) != "AUTH_CONTEXT_STALE" || slices.Contains(fake.Calls(), "apply") {
		t.Fatalf("%v %v", err, fake.Calls())
	}
}

func TestSignInAfterPreflightRefusesTheBinding(t *testing.T) {
	m, fake, id := authPlan(t, profiles.Input{Name: "dev", AWSProfile: "dev"})
	var epoch atomic.Uint64
	m.AuthEpoch = func(project, profile string) uint64 { return epoch.Load() }
	r := runPreflight(t, m, id, PreflightRequest{VerifyIdentity: true})
	if r.Blocked {
		t.Fatalf("%+v", r.Checks)
	}
	epoch.Add(1) // the user signed in to AWS after reviewing
	if _, err := m.Approve(id, ApprovalRequest{Binding: r.Binding, Acknowledged: r.Acknowledge}); rejected(err) != "AUTH_CONTEXT_STALE" {
		t.Fatalf("%v", err)
	}
	// Approved, then a sign-in: the apply is refused and nothing runs.
	r = runPreflight(t, m, id, PreflightRequest{VerifyIdentity: true})
	a, err := m.Approve(id, ApprovalRequest{Binding: r.Binding, Acknowledged: r.Acknowledge})
	if err != nil {
		t.Fatal(err)
	}
	epoch.Add(1)
	if _, err := m.StartApply(id, a.Token, a.Phrase); rejected(err) != "AUTH_CONTEXT_STALE" || slices.Contains(fake.Calls(), "apply") {
		t.Fatalf("%v %v", err, fake.Calls())
	}
	// Only a new preflight and approval apply.
	if a, err = approveVerified(m, id); err != nil {
		t.Fatal(err)
	}
	op, err := m.StartApply(id, a.Token, a.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	if done := wait(t, m, op.ID); done.Outcome != "applied" {
		t.Fatalf("%+v", done)
	}
}

func TestAWSVaultIsInTheReviewedCommand(t *testing.T) {
	m, _, id := authPlan(t, profiles.Input{Name: "prod", AWSProfile: "prod", AWSAuth: profiles.AWSVault})
	p, _ := m.Plan(id)
	if p.Context.AWSAuth != profiles.AWSVault {
		t.Fatalf("%+v", p.Context)
	}
	r := runPreflight(t, m, id, PreflightRequest{})
	if got := strings.Join(r.Command, " "); !strings.HasPrefix(got, "aws-vault exec prod -- terraform apply") {
		t.Fatalf("command %q", got)
	}
	ops := m.Operations()
	if got := strings.Join(ops[len(ops)-1].Command, " "); !strings.HasPrefix(got, "aws-vault exec prod -- terraform plan") {
		t.Fatalf("plan command %q", got)
	}
}

// A profile that names an AWS profile cannot be approved without a verified identity, and the
// approval is bound to it: a sign-in made outside Hclaro (a terminal) that changes the
// principal refuses the apply even without an expected-account guard or a policy rule.
func TestApprovalIsBoundToTheReviewedIdentity(t *testing.T) {
	m, fake, id := authPlan(t, profiles.Input{Name: "dev", AWSProfile: "dev"})
	sts := &identityAt{id: verifiedAs("111122223333")}
	m.IdentityProbe = sts.probe
	r := runPreflight(t, m, id, PreflightRequest{})
	if !r.Blocked || mustCheck(t, r, preflight.CheckAWSReviewed).Status != preflight.Unknown {
		t.Fatalf("unverified identity must block: %+v", r.Checks)
	}
	r = runPreflight(t, m, id, PreflightRequest{VerifyIdentity: true})
	if r.Blocked || mustCheck(t, r, preflight.CheckAWSReviewed).Status != preflight.Pass {
		t.Fatalf("%+v", r.Checks)
	}
	a, err := m.Approve(id, ApprovalRequest{Binding: r.Binding, Acknowledged: r.Acknowledge})
	if err != nil {
		t.Fatal(err)
	}
	sts.set(verifiedAs("444455556666"))
	if _, err := m.StartApply(id, a.Token, a.Phrase); rejected(err) != "AUTH_CONTEXT_STALE" || slices.Contains(fake.Calls(), "apply") {
		t.Fatalf("%v %v", err, fake.Calls())
	}
	// The approval was consumed: the old token cannot be replayed after switching back.
	sts.set(verifiedAs("111122223333"))
	if _, err := m.StartApply(id, a.Token, a.Phrase); err == nil {
		t.Fatal("replayed a consumed approval")
	}
}
