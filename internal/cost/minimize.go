package cost

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"hclaro/internal/state"
)

// MaxInput bounds the minimized cost input kept in memory per plan and written for the
// adapter. A plan whose minimized input is larger cannot be estimated.
const MaxInput = 8 << 20

// maxDocument mirrors the plan parser's limit on `show -json` documents.
const maxDocument = 64 << 20

// maxValueString drops longer strings: prices never depend on large free-text values.
const maxValueString = 1024

const maxValueDepth = 32

var (
	// ErrInputTooLarge reports a plan whose minimized input exceeds MaxInput.
	ErrInputTooLarge = errors.New("cost input too large")
	// ErrInputFormat reports a document that is not a `show -json` plan.
	ErrInputFormat = errors.New("unsupported plan document")
)

// droppedKeyParts are attribute-name fragments whose values carry free text, documents or
// scripts rather than pricing dimensions. They are removed from the cost input in addition
// to everything the state redaction policy hides (sensitive markers, risky names and
// credential-shaped or random-looking strings).
var droppedKeyParts = []string{
	"tags", "labels", "description", "policy", "document", "user_data", "custom_data", "metadata",
	"environment", "variables", "content", "script", "template", "command", "body", "inline",
	"comment", "message", "notes", "triggers", "annotation",
}

func droppedKey(key string) bool {
	if state.RiskyKey(key) {
		return true
	}
	key = strings.ToLower(key)
	for _, part := range droppedKeyParts {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}

var regionValue = regexp.MustCompile(`^[a-z]{2}(-gov|-iso[a-z]?)?-[a-z]+-[0-9]{1,2}$|^[a-z]+-[a-z]+[0-9]{1,2}$|^[a-z]+[a-z0-9-]{0,30}$`)

// digitRun finds runs of digits; a run of exactly 12 is treated as an AWS account ID.
var digitRun = regexp.MustCompile(`[0-9]+`)

// maskAccounts replaces every 12-digit run (an AWS account ID in an ARN, ECR or SQS URL,
// bucket name, owner field…) with zeros. Prices never depend on it; the rest is kept.
func maskAccounts(s string) string {
	return digitRun.ReplaceAllStringFunc(s, func(d string) string {
		if len(d) == 12 {
			return "000000000000"
		}
		return d
	})
}

// referencePattern bounds a configuration reference (an address such as var.region or
// aws_instance.web.id). Anything else is dropped.
var referencePattern = regexp.MustCompile(`^[A-Za-z0-9_.\[\]"-]{1,256}$`)

// clean applies the cost input policy to one value. keep is false when the value must be
// dropped entirely.
func clean(value, marker any, key string, depth int) (any, bool) {
	if marker == true || depth > maxValueDepth || (key != "" && droppedKey(key)) {
		return nil, false
	}
	switch v := value.(type) {
	case nil, bool, json.Number:
		return v, true
	case string:
		if len(v) > maxValueString || state.LooksSecret(v) || strings.ContainsAny(v, "\x00\r\n") {
			return nil, false
		}
		return maskAccounts(v), true
	case map[string]any:
		masks, _ := marker.(map[string]any)
		out := map[string]any{}
		pair := state.RiskyPair(v)
		for k, item := range v {
			if len(k) > 128 || state.Suspicious(k) || pair && strings.EqualFold(k, "value") {
				continue
			}
			if c, ok := clean(item, masks[k], k, depth+1); ok {
				out[k] = c
			}
		}
		return out, true
	case []any:
		masks, _ := marker.([]any)
		out := make([]any, len(v))
		for i, item := range v {
			var mask any
			if i < len(masks) {
				mask = masks[i]
			}
			// A dropped element keeps its position so indexes stay meaningful.
			out[i], _ = clean(item, mask, key, depth+1)
		}
		return out, true
	}
	return nil, false
}

// cleanValues filters a resource's attribute object with its sensitivity markers.
func cleanValues(values, sensitive any) any {
	if values == nil {
		return nil
	}
	c, ok := clean(values, sensitive, "", 0)
	if !ok {
		return nil
	}
	return c
}

func str(m map[string]any, key string) (string, bool) {
	s, ok := m[key].(string)
	return s, ok
}

// copyStrings copies the named string fields that pass check.
func copyStrings(dst, src map[string]any, check func(string) bool, keys ...string) {
	for _, k := range keys {
		if s, ok := str(src, k); ok && check(s) {
			dst[k] = s
		}
	}
}

func safeName(s string) bool { return len(s) <= 512 && !state.Suspicious(s) }

// moduleTree filters planned_values.root_module or prior_state.values.root_module.
func moduleTree(raw any, depth int) map[string]any {
	in, ok := raw.(map[string]any)
	if !ok || depth > 128 {
		return map[string]any{}
	}
	out := map[string]any{}
	copyStrings(out, in, safeName, "address")
	if resources, ok := in["resources"].([]any); ok {
		kept := []any{}
		for _, item := range resources {
			r, ok := item.(map[string]any)
			if !ok {
				continue
			}
			address, _ := str(r, "address")
			if address == "" || !safeName(address) {
				continue
			}
			res := map[string]any{}
			copyStrings(res, r, safeName, "address", "mode", "type", "name", "provider_name")
			if idx, ok := r["index"]; ok {
				if s, isString := idx.(string); !isString || safeName(s) {
					res["index"] = idx
				}
			}
			if v, ok := r["schema_version"].(json.Number); ok {
				res["schema_version"] = v
			}
			res["values"] = cleanValues(r["values"], r["sensitive_values"])
			res["sensitive_values"] = map[string]any{}
			kept = append(kept, res)
		}
		out["resources"] = kept
	}
	if children, ok := in["child_modules"].([]any); ok {
		kept := []any{}
		for _, c := range children {
			kept = append(kept, moduleTree(c, depth+1))
		}
		out["child_modules"] = kept
	}
	return out
}

// expressions keeps only the references of each top-level expression: addresses that let a
// cost tool relate resources (for example an address attached to an instance). Constant
// values and nested blocks are dropped.
func expressions(raw any) map[string]any {
	in, ok := raw.(map[string]any)
	out := map[string]any{}
	if !ok {
		return out
	}
	for k, item := range in {
		e, ok := item.(map[string]any)
		if !ok || len(k) > 128 || state.Suspicious(k) {
			continue
		}
		refs, ok := e["references"].([]any)
		if !ok {
			continue
		}
		kept := []any{}
		for _, ref := range refs {
			if s, ok := ref.(string); ok && referencePattern.MatchString(s) && !state.Suspicious(s) {
				kept = append(kept, s)
			}
		}
		if len(kept) > 0 {
			out[k] = map[string]any{"references": kept}
		}
	}
	return out
}

func configModule(raw any, depth int) map[string]any {
	in, ok := raw.(map[string]any)
	out := map[string]any{}
	if !ok || depth > 128 {
		return out
	}
	if resources, ok := in["resources"].([]any); ok {
		kept := []any{}
		for _, item := range resources {
			r, ok := item.(map[string]any)
			if !ok {
				continue
			}
			res := map[string]any{}
			copyStrings(res, r, safeName, "address", "mode", "type", "name", "provider_config_key")
			res["expressions"] = expressions(r["expressions"])
			for _, k := range []string{"count_expression", "for_each_expression"} {
				if e := expressions(map[string]any{"e": r[k]}); len(e) > 0 {
					res[k] = e["e"]
				}
			}
			kept = append(kept, res)
		}
		out["resources"] = kept
	}
	if calls, ok := in["module_calls"].(map[string]any); ok {
		kept := map[string]any{}
		for name, item := range calls {
			c, ok := item.(map[string]any)
			if !ok || !safeName(name) {
				continue
			}
			// The module source is not needed for pricing and can embed URLs or tokens.
			kept[name] = map[string]any{"module": configModule(c["module"], depth+1)}
		}
		out["module_calls"] = kept
	}
	return out
}

// providerConfig keeps provider names and a literal region; other provider arguments
// (endpoints, credentials, assume-role settings, default tags) are dropped.
func providerConfig(raw any) map[string]any {
	in, ok := raw.(map[string]any)
	out := map[string]any{}
	if !ok {
		return out
	}
	for key, item := range in {
		p, ok := item.(map[string]any)
		if !ok || !safeName(key) {
			continue
		}
		kept := map[string]any{}
		copyStrings(kept, p, safeName, "name", "full_name", "alias", "module_address")
		if exprs, ok := p["expressions"].(map[string]any); ok {
			if region, ok := exprs["region"].(map[string]any); ok {
				e := map[string]any{}
				if s, ok := region["constant_value"].(string); ok && regionValue.MatchString(s) {
					e["constant_value"] = s
				}
				if refs := expressions(map[string]any{"r": region}); len(refs) > 0 {
					e["references"] = refs["r"].(map[string]any)["references"]
				}
				if len(e) > 0 {
					kept["expressions"] = map[string]any{"region": e}
				}
			}
		}
		out[key] = kept
	}
	return out
}

// Minimize derives the cost input from a `show -json` plan document: the smallest subset a
// cost tool needs to price the planned and prior resources. It keeps the document shape
// (format and engine version, planned_values, prior_state values, resource_changes with
// actions and filtered before/after values, provider names with a literal region, and
// configuration references) and drops variables, outputs, drift, checks, module sources,
// provider arguments other than the region, every value marked sensitive, attributes with
// secret-like or free-text names, credential-shaped or random-looking strings and long
// strings. The result holds no state outputs, no variable values and no credentials; it can
// still contain ordinary attribute values such as instance types, sizes, regions and IDs.
func Minimize(document []byte) ([]byte, error) {
	if len(document) > maxDocument {
		return nil, ErrInputTooLarge
	}
	dec := json.NewDecoder(bytes.NewReader(document))
	dec.UseNumber()
	var raw map[string]any
	if dec.Decode(&raw) != nil || dec.More() {
		return nil, ErrInputFormat
	}
	format, _ := str(raw, "format_version")
	if !strings.HasPrefix(format, "1.") {
		return nil, ErrInputFormat
	}
	if _, ok := raw["planned_values"]; !ok {
		if _, ok := raw["resource_changes"]; !ok {
			return nil, ErrInputFormat
		}
	}
	out := map[string]any{"format_version": format}
	if v, ok := str(raw, "terraform_version"); ok && len(v) <= 64 && !state.Suspicious(v) {
		out["terraform_version"] = v
	}
	if pv, ok := raw["planned_values"].(map[string]any); ok {
		out["planned_values"] = map[string]any{"root_module": moduleTree(pv["root_module"], 0)}
	} else {
		out["planned_values"] = map[string]any{"root_module": map[string]any{}}
	}
	if ps, ok := raw["prior_state"].(map[string]any); ok {
		prior := map[string]any{}
		copyStrings(prior, ps, safeName, "format_version", "terraform_version")
		if values, ok := ps["values"].(map[string]any); ok {
			prior["values"] = map[string]any{"root_module": moduleTree(values["root_module"], 0)}
		}
		out["prior_state"] = prior
	}
	changes := []any{}
	if rc, ok := raw["resource_changes"].([]any); ok {
		for _, item := range rc {
			r, ok := item.(map[string]any)
			if !ok {
				continue
			}
			address, _ := str(r, "address")
			if address == "" || !safeName(address) {
				continue
			}
			res := map[string]any{}
			copyStrings(res, r, safeName, "address", "module_address", "mode", "type", "name", "provider_name")
			if idx, ok := r["index"]; ok {
				if s, isString := idx.(string); !isString || safeName(s) {
					res["index"] = idx
				}
			}
			change := map[string]any{}
			if c, ok := r["change"].(map[string]any); ok {
				if actions, ok := c["actions"].([]any); ok {
					kept := []any{}
					for _, a := range actions {
						if s, ok := a.(string); ok && len(s) <= 16 {
							kept = append(kept, s)
						}
					}
					change["actions"] = kept
				}
				change["before"] = cleanValues(c["before"], c["before_sensitive"])
				change["after"] = cleanValues(c["after"], c["after_sensitive"])
				if unknown, ok := clean(c["after_unknown"], nil, "", 0); ok {
					change["after_unknown"] = unknown
				}
			}
			res["change"] = change
			changes = append(changes, res)
		}
	}
	out["resource_changes"] = changes
	if cfg, ok := raw["configuration"].(map[string]any); ok {
		out["configuration"] = map[string]any{
			"provider_config": providerConfig(cfg["provider_config"]),
			"root_module":     configModule(cfg["root_module"], 0),
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, ErrInputFormat
	}
	if len(b) > MaxInput {
		return nil, ErrInputTooLarge
	}
	return b, nil
}
