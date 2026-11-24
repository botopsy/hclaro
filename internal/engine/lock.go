package engine

import (
	"strings"

	"hclaro/internal/state"
)

// LockInfo is the holder of a state lock, read from the "Lock Info:" block of an engine
// diagnostic. Path and Info are dropped: they can name backends or hold free text.
type LockInfo struct {
	ID        string `json:"id,omitempty"`
	Who       string `json:"who"`
	Operation string `json:"operation"`
	Created   string `json:"created,omitempty"`
}

const maxLockField = 200

// ParseLockInfo reads the lock holder from sanitized engine diagnostics text (Terraform and
// OpenTofu print the same block). It returns nil unless both Who and Operation are present
// and plain, so garbled or secret-shaped text keeps the generic "locked" message.
func ParseLockInfo(text string) *LockInfo {
	_, block, ok := strings.Cut(lockedNoise.ReplaceAllString(text, " "), "Lock Info:")
	if !ok {
		return nil
	}
	var info LockInfo
	for _, line := range strings.Split(block, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		value = strings.TrimSpace(value)
		if !ok || value == "" {
			continue
		}
		if len(value) > maxLockField || strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) || state.LooksSecret(value) {
			continue
		}
		switch strings.ToLower(key) {
		case "id":
			info.ID = value
		case "who":
			info.Who = value
		case "operation":
			info.Operation = strings.ToLower(strings.TrimPrefix(value, "OperationType"))
		case "created":
			info.Created = value
		}
	}
	if info.Who == "" || info.Operation == "" {
		return nil
	}
	return &info
}
