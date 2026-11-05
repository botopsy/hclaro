// Package fixtures contains explicitly synthetic data for tests and the fake engine.
package fixtures

import _ "embed"

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
)
