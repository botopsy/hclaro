// Package drift classifies a refresh-only plan (`plan -refresh-only`, read back with
// `show -json`) into a redacted drift result. It reuses the plan parser, so raw values never
// leave internal/plan; this package sees only redacted values and diff metadata.
//
// A refresh-only plan compares the objects recorded in state with what providers report
// during the scan. It is not a configuration convergence plan: it says nothing about whether
// configuration matches infrastructure.
package drift

import (
	"errors"
	"fmt"

	"hclaro/internal/plan"
)

// Classifications of one drift record.
const (
	// Changed: the provider reported attribute values that differ from those recorded in state.
	Changed = "changed"
	// Missing: the provider reported that the remote object no longer exists.
	Missing = "missing"
	// CannotVerify: the provider reported the object but some values were unknown during the scan,
	// and no known value differs.
	CannotVerify = "cannot_verify"
	// Unrecognized: an action Hclaro does not expect in a refresh-only plan. Never shown as safe.
	Unrecognized = "unrecognized"
)

// ErrNotRefreshOnly means the document proposes resource changes, which a refresh-only plan
// never does. Its drift records cannot be trusted as a scan result.
var ErrNotRefreshOnly = errors.New("the plan proposes resource changes, so it is not a refresh-only result")

// ResourceChange is one object whose remote state differs from what state records.
type ResourceChange struct {
	ID             string   `json:"id"`
	Address        string   `json:"address"`
	Module         string   `json:"module"`
	Type           string   `json:"type"`
	Name           string   `json:"name"`
	Index          any      `json:"index,omitempty"`
	Provider       string   `json:"provider"`
	Deposed        string   `json:"deposed,omitempty"`
	Actions        []string `json:"actions"`
	Classification string   `json:"classification"`
	AttributeCount int      `json:"attributeChanges"`
	// CannotVerify counts attributes whose observed value was unknown during the scan.
	CannotVerify int `json:"cannotVerify"`
	// ResourceID is the opaque ID of the same address in the current state snapshot, set only
	// when the scan is current and the address is recorded there; it deep-links the inspector.
	ResourceID string `json:"resourceId,omitempty"`
}

// AttributeObservation compares one attribute: the value recorded in state and the value
// observed during the scan. Values are redacted.
type AttributeObservation struct {
	Path string `json:"path"`
	// Recorded is the value recorded in state before the scan.
	Recorded any `json:"recorded"`
	// Observed is the value the provider reported during the scan; nil when Kind is
	// cannot_verify or removed.
	Observed any `json:"observed"`
	// Kind is changed, added, removed or cannot_verify.
	Kind      string `json:"kind"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

// Detail is a resource change with its attribute observations.
type Detail struct {
	ResourceChange
	Attributes []AttributeObservation `json:"attributes"`
	Truncated  bool                   `json:"truncated,omitempty"`
	Recorded   any                    `json:"recorded"`
	Observed   any                    `json:"observed"`
}

// Summary counts resource changes by classification.
type Summary struct {
	Changed      int `json:"changed"`
	Missing      int `json:"missing"`
	CannotVerify int `json:"cannotVerify"`
	Unrecognized int `json:"unrecognized"`
	Total        int `json:"total"`
	// OutputChanges counts root outputs whose value would change; values are never returned.
	OutputChanges int `json:"outputChanges"`
}

// Result is the redacted outcome of one refresh-only plan document.
type Result struct {
	EngineVersion string
	Errored       bool
	// Incomplete is set when the engine reported the plan errored or incomplete, or deferred
	// work: the absence of a record then proves nothing.
	Incomplete bool
	Summary    Summary
	Resources  []ResourceChange
	Details    map[string]Detail
	Warnings   []string
}

// Analyze parses and classifies a refresh-only plan document. It fails closed on anything
// plan.Parse refuses and on documents that propose resource changes.
func Analyze(document []byte) (Result, error) {
	review, err := plan.Parse(document)
	if err != nil {
		return Result{}, err
	}
	return FromReview(review)
}

// FromReview classifies an already parsed review.
func FromReview(review plan.Review) (Result, error) {
	for _, c := range review.Changes {
		if c.Action != plan.NoOp {
			return Result{}, ErrNotRefreshOnly
		}
	}
	r := Result{EngineVersion: review.EngineVersion, Errored: review.Errored, Resources: []ResourceChange{}, Details: map[string]Detail{}, Warnings: []string{}}
	r.Incomplete = review.Incomplete || review.Deferred > 0
	data := 0
	for _, c := range review.Drift {
		if c.Mode != "managed" {
			// Data sources are read on every run; a changed result is not drift.
			data++
			continue
		}
		d := review.Details[c.ID]
		out := Detail{Attributes: make([]AttributeObservation, 0, len(d.Attributes)), Truncated: d.Truncated, Recorded: d.Before, Observed: relabel(d.After, 0)}
		unknown := 0
		for _, a := range d.Attributes {
			o := AttributeObservation{Path: a.Path, Recorded: a.Before, Observed: a.After, Kind: a.Kind, Sensitive: a.Sensitive}
			if a.Unknown {
				o.Kind, o.Observed = CannotVerify, nil
				unknown++
			}
			out.Attributes = append(out.Attributes, o)
		}
		class := Unrecognized
		switch c.Action {
		case plan.Delete:
			class = Missing
			out.Observed = nil
		case plan.Update:
			class = Changed
			if unknown > 0 && unknown == len(d.Attributes) {
				class = CannotVerify
			}
		case plan.NoOp:
			if unknown > 0 {
				class = CannotVerify
			} else {
				// Nothing differs; the engine listed it anyway. Not drift.
				continue
			}
		}
		out.ResourceChange = ResourceChange{
			ID: c.ID, Address: c.Address, Module: c.Module, Type: c.Type, Name: c.Name, Index: c.Index,
			Provider: c.Provider, Deposed: c.Deposed, Actions: c.Actions, Classification: class,
			AttributeCount: len(d.Attributes), CannotVerify: unknown,
		}
		r.Resources = append(r.Resources, out.ResourceChange)
		r.Details[c.ID] = out
		switch class {
		case Changed:
			r.Summary.Changed++
		case Missing:
			r.Summary.Missing++
		case CannotVerify:
			r.Summary.CannotVerify++
		default:
			r.Summary.Unrecognized++
		}
	}
	r.Summary.Total = len(r.Resources)
	for _, o := range review.Outputs {
		if o.Action != plan.NoOp {
			r.Summary.OutputChanges++
		}
	}
	// The plan parser's warnings talk about applying; these are about the scan.
	if review.Errored {
		r.Warnings = append(r.Warnings, "The engine reported the scan as errored. Its results are incomplete: objects not listed were not necessarily checked.")
	} else if r.Incomplete {
		r.Warnings = append(r.Warnings, "The engine reported the scan as incomplete. Objects not listed were not necessarily checked.")
	}
	if review.Deferred > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d deferred change(s) were not evaluated during the scan.", review.Deferred))
	}
	if data > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d data source record(s) were read again during the scan and are not drift; they are not listed.", data))
	}
	if r.Summary.Unrecognized > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d record(s) use an action Hclaro does not expect in a refresh-only plan. Inspect them with the CLI; they are not treated as safe.", r.Summary.Unrecognized))
	}
	if r.Summary.OutputChanges > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d root output value(s) differ from what state records. Output values are not shown.", r.Summary.OutputChanges))
	}
	return r, nil
}

// UnverifiedMarker replaces the plan parser's "known after apply" marker in observed values:
// in a scan nothing is applied, the value was simply not known.
const UnverifiedMarker = "[cannot verify]"

func relabel(v any, depth int) any {
	if depth > 64 {
		return v
	}
	switch t := v.(type) {
	case string:
		if t == plan.KnownAfterApply {
			return UnverifiedMarker
		}
	case map[string]any:
		for k, item := range t {
			t[k] = relabel(item, depth+1)
		}
	case []any:
		for i, item := range t {
			t[i] = relabel(item, depth+1)
		}
	}
	return v
}
