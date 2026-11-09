// Package fixtures contains explicitly synthetic data for tests and the fake engine.
package fixtures

import "embed"

//go:embed state.json
var State []byte

//go:embed empty.json
var Empty []byte

// Plan fixtures are synthetic `show -json` plan documents and `plan -json` UI streams.
var (
	//go:embed plan/all-actions.json
	PlanAllActions []byte
	//go:embed plan/collateral.json
	PlanCollateral []byte
	//go:embed plan/no-changes.json
	PlanNoChanges []byte
	//go:embed plan/errored.json
	PlanErrored []byte
	//go:embed plan/plan-log.jsonl
	PlanLog []byte
	//go:embed plan/plan-log-locked.jsonl
	PlanLogLocked []byte
	//go:embed plan/apply-log.jsonl
	ApplyLog []byte
	//go:embed plan/import.json
	PlanImport []byte
	//go:embed plan/import-generated.tf
	ImportGenerated []byte
	//go:embed tools/validate-valid.json
	ValidateValid []byte
	//go:embed tools/validate-invalid.json
	ValidateInvalid []byte
	//go:embed tools/validate-noinit.json
	ValidateNoInit []byte
	//go:embed tools/outputs.json
	Outputs []byte
	//go:embed tools/providers.txt
	Providers []byte
)

// Drift fixtures are synthetic refresh-only `show -json` plan documents and UI streams.
var (
	//go:embed drift/no-drift.json
	DriftNone []byte
	//go:embed drift/changed-tag.json
	DriftChangedTag []byte
	//go:embed drift/security-group.json
	DriftSecurityGroup []byte
	//go:embed drift/deleted.json
	DriftDeleted []byte
	//go:embed drift/unknown.json
	DriftUnknown []byte
	//go:embed drift/inaccessible.json
	DriftInaccessible []byte
	//go:embed drift/mixed.json
	DriftMixed []byte
	//go:embed drift/mixed-tofu.json
	DriftMixedTofu []byte
	//go:embed drift/not-refresh-only.json
	DriftNotRefreshOnly []byte
	//go:embed drift/log.jsonl
	DriftLog []byte
	//go:embed drift/log-denied.jsonl
	DriftLogDenied []byte
	//go:embed drift/log-sso.jsonl
	DriftLogSSO []byte
	//go:embed drift/log-locked.jsonl
	DriftLogLocked []byte
)

// Graph fixtures are synthetic `graph` DOT outputs (see scripts/fixtures/generate-graph.py).
var (
	//go:embed graph/terraform.dot
	GraphTerraform []byte
	//go:embed graph/tofu.dot
	GraphTofu []byte
	//go:embed graph/large.dot
	GraphLarge []byte
	//go:embed graph/edge-cases.dot
	GraphEdgeCases []byte
	//go:embed graph/cycle.dot
	GraphCycle []byte
	//go:embed graph/malformed.dot
	GraphMalformed []byte
)

// Workspace comparison fixtures are synthetic `show -json` states (see
// scripts/fixtures/generate-compare.py).
var (
	//go:embed compare/dev.json
	CompareDev []byte
	//go:embed compare/prod.json
	CompareProd []byte
	//go:embed compare/prod-tofu.json
	CompareProdTofu []byte
	//go:embed compare/prod-renamed.json
	CompareProdRenamed []byte
)

// AWS holds recorded, sanitized and fully synthetic AWS CLI v2 responses for cloud discovery
// (M16): account 111122223333 (an AWS documentation example), canary values in tags and
// fields Hclaro must never decode or show.
//
//go:embed aws/*.json
var AWS embed.FS

// CostInfracost is a synthetic Infracost v0.10 `breakdown --format json` report for the
// all-actions plan (M17). Prices are illustrative; a canary sits in one component name.
//
//go:embed cost/infracost-breakdown.json
var CostInfracost []byte
