// Package state parses the official show JSON format. Raw input never leaves this package.
package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
)

const Redacted = "[redacted]"
const MaxBytes = 64 << 20

var ErrNoState = errors.New("no state snapshot recorded")

type Resource struct {
	ID         string `json:"id"`
	Address    string `json:"address"`
	Mode       string `json:"mode"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	Provider   string `json:"provider"`
	Module     string `json:"module"`
	Index      any    `json:"index,omitempty"`
	Attributes any    `json:"attributes"`
}

type Module struct {
	Address string `json:"address"`
	Parent  string `json:"parent"`
	Count   int    `json:"count"`
}

type Index struct {
	Resources []Resource          `json:"resources"`
	Modules   []Module            `json:"modules"`
	Providers map[string]int      `json:"providers"`
	Types     map[string]int      `json:"types"`
	Managed   int                 `json:"managed"`
	Data      int                 `json:"data"`
	ByID      map[string]Resource `json:"-"`
}

type rawResource struct {
	Address   string `json:"address"`
	Mode      string `json:"mode"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Provider  string `json:"provider_name"`
	Index     any    `json:"index"`
	Values    any    `json:"values"`
	Sensitive any    `json:"sensitive_values"`
}
type rawModule struct {
	Address   string        `json:"address"`
	Resources []rawResource `json:"resources"`
	Children  []rawModule   `json:"child_modules"`
}

func Empty() Index {
	return Index{Resources: []Resource{}, Modules: []Module{}, Providers: map[string]int{}, Types: map[string]int{}, ByID: map[string]Resource{}}
}

// Parse fails closed on plans, unknown major formats, duplicate addresses and malformed records.
// Canonical addresses are supplied by the CLI, never reconstructed by splitting on dots.
func Parse(input []byte) (Index, error) {
	idx := Empty()
	if len(input) > MaxBytes {
		return idx, errors.New("state exceeds 64 MiB limit")
	}
	var doc struct {
		Format string `json:"format_version"`
		Values *struct {
			Root rawModule `json:"root_module"`
		} `json:"values"`
		Changes json.RawMessage `json:"resource_changes"`
		Plan    json.RawMessage `json:"planned_values"`
	}
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	if dec.Decode(&doc) != nil {
		return idx, errors.New("invalid state JSON")
	}
	if dec.Decode(new(any)) != io.EOF {
		return idx, errors.New("invalid trailing state JSON")
	}
	if !strings.HasPrefix(doc.Format, "1.") || len(doc.Changes) > 0 || len(doc.Plan) > 0 {
		return idx, errors.New("unsupported state format")
	}
	if doc.Values == nil {
		return idx, ErrNoState
	}
	seen := map[string]bool{}
	var walk func(rawModule, string, int) error
	walk = func(m rawModule, parent string, depth int) error {
		if depth > 128 {
			return errors.New("module nesting exceeds limit")
		}
		if depth > 0 && (m.Address == "" || !strings.HasPrefix(m.Address, "module.")) {
			return errors.New("invalid module address")
		}
		mod := Module{Address: m.Address, Parent: parent, Count: len(m.Resources)}
		idx.Modules = append(idx.Modules, mod)
		for _, r := range m.Resources {
			if len(idx.Resources) >= 100_000 {
				return errors.New("state exceeds 100,000 resource limit")
			}
			if r.Address == "" || r.Name == "" || r.Type == "" || (r.Mode != "managed" && r.Mode != "data") || seen[r.Address] {
				return errors.New("invalid or duplicate resource record")
			}
			seen[r.Address] = true
			// Metadata itself can contain user-controlled instance keys. Reject secret-shaped
			// metadata rather than returning a misleading, partially masked address.
			for _, s := range []string{r.Address, r.Name, r.Type, r.Provider, m.Address} {
				if Suspicious(s) {
					return errors.New("unsafe resource metadata")
				}
			}
			hash := sha256.Sum256([]byte(r.Address))
			resource := Resource{ID: hex.EncodeToString(hash[:16]), Address: r.Address, Mode: r.Mode, Type: r.Type, Name: r.Name, Provider: r.Provider, Module: m.Address, Index: redact(r.Index, nil, "index", 0), Attributes: redact(r.Values, r.Sensitive, "", 0)}
			idx.Resources = append(idx.Resources, resource)
			idx.ByID[resource.ID] = resource
			idx.Providers[r.Provider]++
			idx.Types[r.Type]++
			if r.Mode == "managed" {
				idx.Managed++
			} else {
				idx.Data++
			}
		}
		for _, child := range m.Children {
			if err := walk(child, m.Address, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(doc.Values.Root, "", 0); err != nil {
		return Empty(), err
	}
	sort.Slice(idx.Resources, func(i, j int) bool { return idx.Resources[i].Address < idx.Resources[j].Address })
	return idx, nil
}

var safeKeys = map[string]bool{
	"id": true, "arn": true, "name": true, "region": true, "availability_zone": true,
	"instance_type": true, "engine": true, "engine_version": true, "version": true,
	"cidr_block": true, "vpc_id": true, "subnet_id": true, "instance_state": true,
	"status": true, "bucket": true, "resource_group_name": true, "location": true,
	"index": true, "size": true, "port": true, "enabled": true, "count": true,
	"allocated_storage": true, "multi_az": true, "encrypted": true,
}

// RiskyKey reports attribute names that are always redacted, whatever their value.
func RiskyKey(key string) bool {
	key = strings.ToLower(key)
	for _, part := range []string{"secret", "password", "passwd", "token", "credential", "private", "access_key", "api_key", "connection", "user_data", "certificate", "kubeconfig", "authorization"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}

// Suspicious catches common credential shapes even under otherwise safe attribute keys.
func Suspicious(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"do_not_leak", "-----begin ", "password=", "password:", "secret=", "token=", "bearer ", "akia", "asia", "://"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return strings.Count(value, ".") == 2 && strings.HasPrefix(value, "eyJ")
}

// Redact applies the state redaction policy to a value with its sensitivity marker.
// key is the attribute name the value belongs to; only allowlisted keys reveal scalars.
func Redact(value, marker any, key string) any { return redact(value, marker, key, 0) }

func redact(value, marker any, key string, depth int) any {
	if marker == true || RiskyKey(key) || depth > 64 {
		return Redacted
	}
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		masks, _ := marker.(map[string]any)
		for k, item := range v {
			// Unknown map keys can themselves be secrets (e.g. for_each or arbitrary tags).
			if Suspicious(k) || len(k) > 128 {
				out["[redacted key]"] = Redacted
				continue
			}
			out[k] = redact(item, masks[k], k, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(v))
		masks, _ := marker.([]any)
		for i, item := range v {
			var mask any
			if i < len(masks) {
				mask = masks[i]
			}
			out[i] = redact(item, mask, key, depth+1)
		}
		return out
	case nil:
		return nil
	case string:
		if safeKeys[key] && len(v) <= 512 && !Suspicious(v) {
			return v
		}
	case bool, json.Number:
		if safeKeys[key] {
			return v
		}
	}
	return Redacted
}
