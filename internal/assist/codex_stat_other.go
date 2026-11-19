//go:build !darwin && !linux

package assist

import "errors"

// statKeyOf: Codex runs only on macOS (CODEX-INVESTIGATION.md §7); elsewhere the gate refuses
// first and this never succeeds.
func statKeyOf(string) (string, error) { return "", errors.New("codex runs only on macOS") }
