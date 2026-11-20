// Package ids holds the shape checks shared by every package that accepts an ID or a
// workspace name from a file or a request.
package ids

import "regexp"

var (
	id        = regexp.MustCompile(`^[0-9a-f]{32}$`)
	workspace = regexp.MustCompile(`^[A-Za-z0-9_-]{1,90}$`)
)

// Valid reports whether s has the shape of a generated ID: 32 lowercase hex digits.
func Valid(s string) bool { return id.MatchString(s) }

// ValidWorkspace reports whether s has the shape of a workspace name Hclaro accepts.
func ValidWorkspace(s string) bool { return workspace.MatchString(s) }
