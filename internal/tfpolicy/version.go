package tfpolicy

import (
	"regexp"
	"strconv"
)

var versionPattern = regexp.MustCompile(`^1\.(\d+)\.(\d+)$`)

// minor returns the minor version of a stable 1.x version, or -1.
func minor(version string) int {
	m := versionPattern.FindStringSubmatch(version)
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// Supported reports the stable engine versions Hclaro accepts. Conservative compatibility
// range, exercised with official-format synthetic output.
func Supported(name, version string) bool {
	n := minor(version)
	return name == "terraform" && n >= 5 && n <= 17 || name == "tofu" && n >= 6 && n <= 13
}

// EngineLabel is the display name of a known engine, "" for any other name.
func EngineLabel(name string) string {
	switch name {
	case "terraform":
		return "Terraform"
	case "tofu":
		return "OpenTofu"
	}
	return ""
}

// SupportText names the ranges Supported accepts; user-facing messages use it so the range is
// written once (TestSupportTextMatchesSupported pins it to Supported).
const SupportText = "stable Terraform 1.5–1.17 or OpenTofu 1.6–1.13"

// Feature is an engine capability that some supported versions may lack.
type Feature int

const (
	// ImportBlocks: import blocks and -generate-config-out (Terraform 1.5, OpenTofu 1.6).
	ImportBlocks Feature = iota
	// RefreshOnly: `plan -refresh-only` with -out and -json (Terraform 0.15.4, every OpenTofu).
	RefreshOnly
	// Graph: `graph` with documented output. Terraform 1.7 and later print a resource-only
	// graph by default, earlier Terraform and OpenTofu the full walk graph; both are parsed.
	Graph
	// InitJSON: `init -json` (Terraform 1.9). OpenTofu stays on human output because its
	// support has not been verified here.
	InitJSON
)

// Supports reports whether a supported engine version has feature. Import, refresh-only and
// graph hold for the whole Supported range; they stay named so the range can widen safely.
func Supports(name, version string, feature Feature) bool {
	if !Supported(name, version) {
		return false
	}
	if feature == InitJSON {
		return name == "terraform" && minor(version) >= 9
	}
	return true
}
