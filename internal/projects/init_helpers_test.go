package projects

import "hclaro/internal/operations"

// InitStatus is InitStatusFor without an execution profile.
func (h *Hub) InitStatus(id string) (InitStatus, error) { return h.InitStatusFor(id, "") }

// StartInit is StartInitWith without an execution profile.
func (h *Hub) StartInit(id, expected string) (operations.Operation, error) {
	return h.StartInitWith(id, expected, "")
}
