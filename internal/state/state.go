// Package state parses the official show JSON format. Raw input never leaves this package.
package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
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
			resource := Resource{ID: hex.EncodeToString(hash[:16]), Address: r.Address, Mode: r.Mode, Type: r.Type, Name: r.Name, Provider: r.Provider, Module: m.Address, Index: InstanceKey(r.Index), Attributes: redact(r.Values, r.Sensitive, "", 0)}
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

// riskyKeyParts are attribute-name fragments whose values are always redacted,
// whether or not the provider marks them sensitive.
var riskyKeyParts = []string{
	"secret", "password", "passwd", "passphrase", "token", "credential",
	"private_key", "client_key", "signing_key", "key_material", "access_key", "api_key", "license_key",
	"connection_string", "user_data", "custom_data", "certificate_body", "certificate_chain", "certificate_pem", "cert_pem",
	"kubeconfig", "kube_config", "authorization", "auth_header", "sas_url", "sas_token",
}

// RiskyKey reports attribute names that are always redacted, whatever their value. Names
// are compared as lower-case letters and digits only, so privateKey, x-api-key and names
// with invisible separators match like private_key and api_key.
func RiskyKey(key string) bool {
	key = alnum(key)
	for _, part := range riskyKeyParts {
		if strings.Contains(key, alnum(part)) {
			return true
		}
	}
	return false
}

func alnum(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return -1
	}, s)
}

var (
	awsAccessKeyID = regexp.MustCompile(`(^|[^A-Z0-9])(AKIA|ASIA|AGPA|AIDA|AROA|ANPA)[A-Z0-9]{16}([^A-Z0-9]|$)`)
	urlUserinfo    = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^/\s@]*:[^/\s@]*@`)
	tokenRun       = regexp.MustCompile(`[A-Za-z0-9+/=_]{20,}`)
	// quotedSecretKey is a secret-named key inside JSON text, also when the JSON is escaped
	// in another string: "password": or \"db_token\":.
	quotedSecretKey = regexp.MustCompile(`(?i)["'][a-z0-9_.-]*(passw(or)?d|passphrase|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)[a-z0-9_.-]*\\*["']\s*:`)
	// basicAuth is a whole value that is an HTTP Basic credential.
	basicAuth = regexp.MustCompile(`(?i)^\s*basic\s+[A-Za-z0-9+/]+={0,2}\s*$`)
)

// knownTokenPrefixes are well-known vendor credential prefixes.
var knownTokenPrefixes = []string{"ghp_", "gho_", "ghs_", "ghu_", "github_pat_", "glpat-", "xoxb-", "xoxp-", "xoxa-", "sk_live_", "rk_live_", "sk-ant-"}

// Suspicious catches common credential shapes: PEM blocks, key=value secrets, bearer
// tokens, JWTs, AWS access key IDs, URLs with embedded passwords and vendor token
// prefixes. It is also used on addresses and names, where a match rejects the input,
// so it deliberately excludes the random-string heuristic in LooksSecret.
func Suspicious(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"do_not_leak", "-----begin ", "password=", "password:", "passwd=", "pwd=", "secret=", "token=", "apikey=", "api_key=", "bearer "} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	for _, prefix := range knownTokenPrefixes {
		if strings.Contains(value, prefix) {
			return true
		}
	}
	if strings.Count(value, ".") == 2 && strings.HasPrefix(value, "eyJ") {
		return true
	}
	return awsAccessKeyID.MatchString(value) || urlUserinfo.MatchString(value) || quotedSecretKey.MatchString(value)
}

// LooksSecret is Suspicious plus long random-looking strings: generated secrets that no
// provider marked sensitive. It decides whether an attribute value is shown.
func LooksSecret(value string) bool {
	if Suspicious(value) || basicAuth.MatchString(value) {
		return true
	}
	for _, run := range tokenRun.FindAllString(value, -1) {
		if randomLooking(run) {
			return true
		}
	}
	return false
}

// randomLooking flags generated secrets that no provider marked sensitive. Random
// strings switch between upper case, lower case and digits far more often than words,
// identifiers (CamelCase, snake_case) or paths do. Cloud resource IDs such as
// i-0abc… are split on '-' before they get here and stay below the 20-character floor.
// Long hex strings (hashes, etags) are deliberately caught too: many vendor API keys
// are plain hex and cannot be told apart from a digest.
func randomLooking(run string) bool {
	class := func(r rune) int {
		switch {
		case r >= 'a' && r <= 'z':
			return 1
		case r >= 'A' && r <= 'Z':
			return 2
		case r >= '0' && r <= '9':
			return 3
		}
		return 0
	}
	seen := map[int]bool{}
	switches, pairs, prev := 0, 0, -1
	for _, r := range run {
		c := class(r)
		if c == 0 {
			prev = -1
			continue
		}
		seen[c] = true
		if prev != -1 {
			pairs++
			if c != prev {
				switches++
			}
		}
		prev = c
	}
	return len(seen) >= 2 && pairs > 0 && float64(switches)/float64(pairs) >= 0.3
}

// InstanceKey returns a count/for_each key for display. Keys are part of the address,
// which is already checked with Suspicious, so the random-string heuristic is not applied.
func InstanceKey(key any) any {
	switch k := key.(type) {
	case nil, json.Number:
		return k
	case string:
		if !Suspicious(k) {
			return k
		}
	}
	return Redacted
}

// maxShownString bounds a single revealed string; longer values are redacted.
const maxShownString = 16 << 10

// Redact applies the state redaction policy to a value with its sensitivity marker.
// key is the attribute name the value belongs to.
func Redact(value, marker any, key string) any { return redact(value, marker, key, 0) }

func redact(value, marker any, key string, depth int) any {
	if marker == true || RiskyKey(key) || depth > 64 {
		return Redacted
	}
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		masks, _ := marker.(map[string]any)
		// A name/value pair ({"name": "DB_PASSWORD", "value": …}, as in container and
		// function environments) takes its risk from the sibling name.
		pair := false
		for k, item := range v {
			if name, ok := item.(string); ok && (strings.EqualFold(k, "name") || strings.EqualFold(k, "key")) && RiskyKey(name) {
				pair = true
			}
		}
		for k, item := range v {
			// Unknown map keys can themselves be secrets (e.g. for_each or arbitrary tags).
			if Suspicious(k) || len(k) > 128 {
				out["[redacted key]"] = Redacted
				continue
			}
			if pair && strings.EqualFold(k, "value") {
				out[k] = Redacted
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
		if len(v) <= maxShownString && !LooksSecret(v) && !embeddedSecret(v, key, depth) {
			return v
		}
	case bool, json.Number:
		return v
	}
	return Redacted
}

// embeddedSecret reports a string holding a JSON document (a container definition, a policy,
// a serialized environment) in which the redaction policy would hide anything. The whole
// string is then hidden, since it is displayed as one value.
func embeddedSecret(v, key string, depth int) bool {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" || trimmed[0] != '{' && trimmed[0] != '[' {
		return false
	}
	d := json.NewDecoder(strings.NewReader(trimmed))
	d.UseNumber()
	var doc any
	if d.Decode(&doc) != nil {
		return false
	}
	return !reflect.DeepEqual(doc, redact(doc, nil, key, depth+1))
}
