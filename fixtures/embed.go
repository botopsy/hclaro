// Package fixtures contains explicitly synthetic data for tests and the fake engine.
package fixtures

import _ "embed"

//go:embed state.json
var State []byte

//go:embed empty.json
var Empty []byte
