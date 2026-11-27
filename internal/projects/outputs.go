package projects

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"hclaro/internal/engine"
	"hclaro/internal/state"
)

// OutputValue is one output for the "show values" view. A hidden output carries no value: it
// is marked sensitive, or its name or value looks like a secret, and is shown only by an
// explicit reveal. Nothing here is stored, logged or cached.
type OutputValue struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Sensitive bool   `json:"sensitive"`
	Hidden    bool   `json:"hidden"`
	Value     string `json:"value,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ErrOutputNotFound means the workspace has no readable output of that name.
var ErrOutputNotFound = errors.New("output not found")

const (
	maxShownOutput  = 4000
	maxRevealOutput = 64 << 10
)

// outputText renders a value as plain text: strings unquoted, everything else compact JSON.
func outputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (h *Hub) readOutputs(ctx context.Context, id, contextID string) ([]engine.OutputValue, error) {
	t, err := h.prepareTool(id, contextID, engine.ToolOutputs, false)
	if err != nil {
		return nil, err
	}
	defer t.release()
	if t.revalidate() != "" {
		return nil, ErrContextChanged
	}
	return t.tools.OutputValues(ctx)
}

// OutputValues runs `output -json` for an open context and returns every output, with its value
// unless it is sensitive or looks like a secret. It runs only when the user asks.
func (h *Hub) OutputValues(ctx context.Context, id, contextID string) (items []OutputValue, unlisted int, err error) {
	outs, err := h.readOutputs(ctx, id, contextID)
	if err != nil {
		return nil, 0, err
	}
	items = make([]OutputValue, 0, len(outs))
	for _, o := range outs {
		// A name that looks like a secret is not listed, as in the metadata view.
		if state.Suspicious(o.Name) {
			unlisted++
			continue
		}
		text := outputText(o.Value)
		item := OutputValue{Name: o.Name, Type: o.Type, Sensitive: o.Sensitive}
		if o.Sensitive || state.RiskyKey(strings.ToLower(o.Name)) || state.LooksSecret(text) {
			item.Hidden = true
		} else if len(text) > maxShownOutput {
			item.Value, item.Truncated = text[:maxShownOutput], true
		} else {
			item.Value = text
		}
		items = append(items, item)
	}
	return items, unlisted, nil
}

// RevealOutput returns one output's value, even a sensitive one, on explicit request. The
// caller sends it once with no-store and never keeps it.
func (h *Hub) RevealOutput(ctx context.Context, id, contextID, name string) (string, error) {
	outs, err := h.readOutputs(ctx, id, contextID)
	if err != nil {
		return "", err
	}
	for _, o := range outs {
		if o.Name == name {
			text := outputText(o.Value)
			if len(text) > maxRevealOutput {
				return "", ErrOutputTooLong
			}
			return text, nil
		}
	}
	return "", ErrOutputNotFound
}

// ErrOutputTooLong refuses to reveal a value over the display limit.
var ErrOutputTooLong = errors.New("output value is too long to reveal")
