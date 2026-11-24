package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Reveal refusals. None of them carries a value.
var (
	ErrRevealNotFound   = errors.New("resource or attribute path not found")
	ErrRevealNotHidden  = errors.New("value is not redacted")
	ErrRevealTooLong    = errors.New("value is too long to reveal")
	errRevealBadState   = errors.New("invalid state JSON")
	maxRevealPathLength = 64
)

// Reveal returns the one value (a scalar, or a hidden map or list as JSON text) at path of the resource at address, but only when Redact
// hides it. Visible values are refused, so this is not a general state reader. It is a separate, narrow path: Redact itself is unchanged.
// path holds map keys (string) and list indexes (JSON numbers or ints).
func Reveal(input []byte, address string, path []any) (any, error) {
	if len(path) == 0 || len(path) > maxRevealPathLength || len(input) > MaxBytes {
		return nil, ErrRevealNotFound
	}
	var doc struct {
		Format string `json:"format_version"`
		Values *struct {
			Root rawModule `json:"root_module"`
		} `json:"values"`
	}
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	if dec.Decode(&doc) != nil || dec.Decode(new(any)) != io.EOF || !strings.HasPrefix(doc.Format, "1.") || doc.Values == nil {
		return nil, errRevealBadState
	}
	res := findRaw(doc.Values.Root, address, 0)
	if res == nil {
		return nil, ErrRevealNotFound
	}
	raw, hidden := any(res.Values), redact(res.Values, res.Sensitive, "", 0)
	for _, step := range path {
		switch r := raw.(type) {
		case map[string]any:
			key, ok := step.(string)
			if !ok {
				return nil, ErrRevealNotFound
			}
			h, _ := hidden.(map[string]any)
			next, inRaw := r[key]
			shown, inHidden := h[key]
			// A key the policy replaced with "[redacted key]" is absent from hidden.
			if !inRaw || !inHidden {
				return nil, ErrRevealNotFound
			}
			raw, hidden = next, shown
		case []any:
			i, ok := listIndex(step)
			h, _ := hidden.([]any)
			if !ok || i >= len(r) || i >= len(h) {
				return nil, ErrRevealNotFound
			}
			raw, hidden = r[i], h[i]
		default:
			return nil, ErrRevealNotFound
		}
	}
	if hidden != Redacted {
		return nil, ErrRevealNotHidden
	}
	switch v := raw.(type) {
	case string:
		if len(v) > maxShownString {
			return nil, ErrRevealTooLong
		}
		return v, nil
	case bool, json.Number:
		return v, nil
	}
	// A map or list the policy hid as a whole is returned as JSON text, still one value.
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil || len(out) > maxShownString {
		return nil, ErrRevealTooLong
	}
	return string(out), nil
}

func listIndex(step any) (int, bool) {
	switch v := step.(type) {
	case json.Number:
		i, err := v.Int64()
		return int(i), err == nil && i >= 0
	case float64:
		return int(v), v >= 0 && v == float64(int(v))
	}
	return 0, false
}

func findRaw(m rawModule, address string, depth int) *rawResource {
	if depth > 128 {
		return nil
	}
	for i := range m.Resources {
		if m.Resources[i].Address == address {
			return &m.Resources[i]
		}
	}
	for _, child := range m.Children {
		if r := findRaw(child, address, depth+1); r != nil {
			return r
		}
	}
	return nil
}
