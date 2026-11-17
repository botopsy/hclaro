package operations

import (
	"context"
)

// approve runs the preflight, acknowledges each of its warnings individually and requests
// an approval, as the UI does. A blocked preflight has no binding, so Approve says why.
func approve(m *Manager, id string) (Approval, error) {
	r, err := m.Preflight(context.Background(), id, PreflightRequest{})
	if err != nil {
		return Approval{}, err
	}
	return m.Approve(id, ApprovalRequest{Binding: r.Binding, Acknowledged: r.Acknowledge})
}

// approveVerified is approve after the user chose Verify AWS identity in the preflight, as a
// plan whose profile names an AWS profile requires (M20).
func approveVerified(m *Manager, id string) (Approval, error) {
	r, err := m.Preflight(context.Background(), id, PreflightRequest{VerifyIdentity: true})
	if err != nil {
		return Approval{}, err
	}
	return m.Approve(id, ApprovalRequest{Binding: r.Binding, Acknowledged: r.Acknowledge})
}
