package api

import (
	"context"

	"hclaro/internal/operations"
)

// approve runs the preflight, acknowledges each of its warnings individually and requests
// an approval, as the UI does. A blocked preflight has no binding, so Approve says why.
func approve(m *operations.Manager, id string) (operations.Approval, error) {
	r, err := m.Preflight(context.Background(), id, operations.PreflightRequest{})
	if err != nil {
		return operations.Approval{}, err
	}
	return m.Approve(id, operations.ApprovalRequest{Binding: r.Binding, Acknowledged: r.Acknowledge})
}
