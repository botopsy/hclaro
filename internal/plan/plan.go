// Package plan normalizes `show -json <saved plan>` output into a redacted review model.
// Raw plan values never leave this package; only redacted values and diff metadata do.
package plan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"hclaro/internal/state"
)

// Classifications. Anything the engine reports outside the documented
// combinations is "unknown" and must never be presented as safe.
const (
	Create  = "create"
	Update  = "update"
	Delete  = "delete"
	Replace = "replace"
	Read    = "read"
	NoOp    = "no-op"
	Forget  = "forget"
	Unknown = "unknown"
)

// Unknown values are rendered with this marker after redaction.
const KnownAfterApply = "[known after apply]"

const (
	maxRows  = 1000
	maxDepth = 32
)

var ErrFormat = errors.New("unsupported plan format")

type Change struct {
	ID              string   `json:"id"`
	Address         string   `json:"address"`
	PreviousAddress string   `json:"previousAddress,omitempty"`
	Module          string   `json:"module"`
	Mode            string   `json:"mode"`
	Type            string   `json:"type"`
	Name            string   `json:"name"`
	Index           any      `json:"index,omitempty"`
	Provider        string   `json:"provider"`
	Deposed         string   `json:"deposed,omitempty"`
	Actions         []string `json:"actions"`
	Action          string   `json:"action"`
	Reason          string   `json:"reason,omitempty"`
	Importing       bool     `json:"importing,omitempty"`
	AttributeCount  int      `json:"attributeChanges"`
}

type AttributeChange struct {
	Path              string `json:"path"`
	Before            any    `json:"before"`
	After             any    `json:"after"`
	Kind              string `json:"kind"`
	Unknown           bool   `json:"unknown,omitempty"`
	Sensitive         bool   `json:"sensitive,omitempty"`
	ForcesReplacement bool   `json:"forcesReplacement,omitempty"`
}

type Detail struct {
	Change
	Attributes []AttributeChange `json:"attributes"`
	Truncated  bool              `json:"truncated,omitempty"`
	Before     any               `json:"before"`
	After      any               `json:"after"`
}

type Output struct {
	Name      string   `json:"name"`
	Actions   []string `json:"actions"`
	Action    string   `json:"action"`
	Before    any      `json:"before"`
	After     any      `json:"after"`
	Unknown   bool     `json:"unknown,omitempty"`
	Sensitive bool     `json:"sensitive,omitempty"`
}

// Review is the redacted result of one parsed plan document.
type Review struct {
	FormatVersion string            `json:"formatVersion"`
	EngineVersion string            `json:"engineVersion"`
	Timestamp     string            `json:"timestamp,omitempty"`
	Errored       bool              `json:"errored"`
	Summary       map[string]int    `json:"summary"`
	Changes       []Change          `json:"changes"`
	Drift         []Change          `json:"drift"`
	Outputs       []Output          `json:"outputs"`
	Warnings      []string          `json:"warnings"`
	Details       map[string]Detail `json:"-"`
}

type rawChange struct {
	Actions         []string `json:"actions"`
	Before          any      `json:"before"`
	After           any      `json:"after"`
	AfterUnknown    any      `json:"after_unknown"`
	BeforeSensitive any      `json:"before_sensitive"`
	AfterSensitive  any      `json:"after_sensitive"`
	ReplacePaths    [][]any  `json:"replace_paths"`
	Importing       any      `json:"importing"`
}

type rawResourceChange struct {
	Address         string    `json:"address"`
	PreviousAddress string    `json:"previous_address"`
	Module          string    `json:"module_address"`
	Mode            string    `json:"mode"`
	Type            string    `json:"type"`
	Name            string    `json:"name"`
	Index           any       `json:"index"`
	Provider        string    `json:"provider_name"`
	Deposed         string    `json:"deposed"`
	Reason          string    `json:"action_reason"`
	Change          rawChange `json:"change"`
}

var (
	versionPattern = regexp.MustCompile(`^\d{1,3}\.\d{1,3}\.\d{1,3}(-[0-9A-Za-z.]{1,32})?$`)
	reasonPattern  = regexp.MustCompile(`^[a-z_]{1,64}$`)
	deposedPattern = regexp.MustCompile(`^[0-9a-f]{1,32}$`)
	outputPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,127}$`)
	identPattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	timePattern    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T[0-9:.]+Z$`)
)

// Classify maps the engine's ordered action list to a review category.
// delete,create and create,delete are both replacements; the order is kept in Actions.
func Classify(actions []string) string {
	switch strings.Join(actions, ",") {
	case "create":
		return Create
	case "update":
		return Update
	case "delete":
		return Delete
	case "delete,create", "create,delete":
		return Replace
	case "read":
		return Read
	case "no-op":
		return NoOp
	case "forget":
		return Forget
	}
	return Unknown
}

// Parse fails closed on non-plan input, unknown major formats and unsafe metadata.
func Parse(input []byte) (Review, error) {
	review := Review{Summary: map[string]int{}, Changes: []Change{}, Drift: []Change{}, Outputs: []Output{}, Warnings: []string{}, Details: map[string]Detail{}}
	if len(input) > state.MaxBytes {
		return review, errors.New("plan exceeds 64 MiB limit")
	}
	var doc struct {
		Format    string               `json:"format_version"`
		Version   string               `json:"terraform_version"`
		Timestamp string               `json:"timestamp"`
		Errored   bool                 `json:"errored"`
		Planned   json.RawMessage      `json:"planned_values"`
		Values    json.RawMessage      `json:"values"`
		Changes   []rawResourceChange  `json:"resource_changes"`
		Drift     []rawResourceChange  `json:"resource_drift"`
		Outputs   map[string]rawChange `json:"output_changes"`
		Deferred  []json.RawMessage    `json:"deferred_changes"`
	}
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	if dec.Decode(&doc) != nil {
		return review, ErrFormat
	}
	if dec.Decode(new(any)) != io.EOF {
		return review, ErrFormat
	}
	// A state document has "values"; a plan always has "planned_values".
	if !strings.HasPrefix(doc.Format, "1.") || len(doc.Planned) == 0 || len(doc.Values) > 0 {
		return review, ErrFormat
	}
	review.FormatVersion = doc.Format
	if versionPattern.MatchString(doc.Version) {
		review.EngineVersion = doc.Version
	}
	if timePattern.MatchString(doc.Timestamp) {
		review.Timestamp = doc.Timestamp
	}
	review.Errored = doc.Errored
	if doc.Errored {
		review.Warnings = append(review.Warnings, "The engine reported this plan as errored. It is incomplete and must not be applied.")
	}
	if len(doc.Deferred) > 0 {
		review.Warnings = append(review.Warnings, fmt.Sprintf("%d deferred change(s) are not shown. Deferred changes are not supported by this version of Hclaro.", len(doc.Deferred)))
	}
	if len(doc.Changes)+len(doc.Drift) > 100_000 {
		return review, errors.New("plan exceeds 100,000 change limit")
	}
	seen := map[string]bool{}
	for _, raw := range doc.Changes {
		detail, err := normalize(raw, false)
		if err != nil {
			return Review{}, err
		}
		if seen[detail.ID] {
			return Review{}, errors.New("duplicate resource change")
		}
		seen[detail.ID] = true
		review.Changes = append(review.Changes, detail.Change)
		review.Details[detail.ID] = detail
		review.Summary[detail.Action]++
	}
	for _, raw := range doc.Drift {
		detail, err := normalize(raw, true)
		if err != nil {
			return Review{}, err
		}
		if seen[detail.ID] {
			return Review{}, errors.New("duplicate drift record")
		}
		seen[detail.ID] = true
		review.Drift = append(review.Drift, detail.Change)
		review.Details[detail.ID] = detail
	}
	names := make([]string, 0, len(doc.Outputs))
	for name := range doc.Outputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !outputPattern.MatchString(name) || state.Suspicious(name) {
			return Review{}, errors.New("unsafe output name")
		}
		raw := doc.Outputs[name]
		out := Output{Name: name, Actions: actions(raw.Actions), Action: Classify(raw.Actions)}
		out.Sensitive = raw.BeforeSensitive == true || raw.AfterSensitive == true
		out.Unknown = raw.AfterUnknown == true
		// Output values have arbitrary names and are often secrets: only structure is shown.
		out.Before = state.Redact(raw.Before, raw.BeforeSensitive, "")
		// after_unknown can mark parts of a value, such as elements of a list of new IDs.
		out.After = overlayUnknown(state.Redact(raw.After, raw.AfterSensitive, ""), raw.AfterUnknown, 0)
		review.Outputs = append(review.Outputs, out)
	}
	if review.Summary[Unknown] > 0 {
		review.Warnings = append(review.Warnings, fmt.Sprintf("%d change(s) use an action combination Hclaro does not recognize. Treat them as potentially destructive and inspect them with the CLI.", review.Summary[Unknown]))
	}
	return review, nil
}

func actions(in []string) []string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		if len(a) <= 32 && identPattern.MatchString(strings.ReplaceAll(a, "-", "_")) {
			out = append(out, a)
		} else {
			out = append(out, "invalid")
		}
	}
	return out
}

func normalize(raw rawResourceChange, drift bool) (Detail, error) {
	if raw.Address == "" || len(raw.Address) > 1024 || raw.Type == "" || raw.Name == "" || (raw.Mode != "managed" && raw.Mode != "data") {
		return Detail{}, errors.New("invalid resource change record")
	}
	if len(raw.Change.Actions) == 0 || len(raw.Change.Actions) > 4 {
		return Detail{}, errors.New("invalid resource change actions")
	}
	// Same policy as state: secret-shaped metadata rejects the plan rather than
	// producing a misleading, partially masked address.
	for _, s := range []string{raw.Address, raw.PreviousAddress, raw.Module, raw.Type, raw.Name, raw.Provider} {
		if state.Suspicious(s) || len(s) > 1024 {
			return Detail{}, errors.New("unsafe resource metadata")
		}
	}
	if raw.Deposed != "" && !deposedPattern.MatchString(raw.Deposed) {
		return Detail{}, errors.New("invalid deposed object key")
	}
	key := raw.Address + "\x00" + raw.Deposed
	if drift {
		key = "drift\x00" + key
	}
	hash := sha256.Sum256([]byte(key))
	c := Change{
		ID: hex.EncodeToString(hash[:16]), Address: raw.Address, PreviousAddress: raw.PreviousAddress,
		Module: raw.Module, Mode: raw.Mode, Type: raw.Type, Name: raw.Name,
		Index: state.Redact(raw.Index, nil, "index"), Provider: raw.Provider, Deposed: raw.Deposed,
		Actions: actions(raw.Change.Actions), Action: Classify(raw.Change.Actions),
		Importing: raw.Change.Importing != nil,
	}
	if reasonPattern.MatchString(raw.Reason) {
		c.Reason = raw.Reason
	}
	ch := raw.Change
	d := Detail{Change: c, Attributes: []AttributeChange{}}
	var walkErr error
	d.Attributes, d.Truncated, walkErr = diff(ch)
	if walkErr != nil {
		return Detail{}, walkErr
	}
	d.AttributeCount = len(d.Attributes)
	d.Before = state.Redact(ch.Before, ch.BeforeSensitive, "")
	d.After = overlayUnknown(state.Redact(ch.After, ch.AfterSensitive, ""), ch.AfterUnknown, 0)
	return d, nil
}

// diff walks raw before/after values and reports changed leaves. Equality is decided
// on raw values, so changes to redacted values are still visible as changes.
func diff(ch rawChange) ([]AttributeChange, bool, error) {
	rows := []AttributeChange{}
	truncated := false
	var walk func(path []any, before, after, unknown any, depth int)
	walk = func(path []any, before, after, unknown any, depth int) {
		if len(rows) >= maxRows {
			truncated = true
			return
		}
		bm, bIsMap := before.(map[string]any)
		am, aIsMap := after.(map[string]any)
		um, _ := unknown.(map[string]any)
		bl, bIsList := before.([]any)
		al, aIsList := after.([]any)
		ul, _ := unknown.([]any)
		switch {
		case unknown == true:
			rows = append(rows, row(ch, path, before, nil, true))
		case depth < maxDepth && (bIsMap || before == nil) && (aIsMap || after == nil) && (bIsMap || aIsMap || um != nil):
			keys := map[string]bool{}
			for k := range bm {
				keys[k] = true
			}
			for k := range am {
				keys[k] = true
			}
			for k := range um {
				keys[k] = true
			}
			sorted := make([]string, 0, len(keys))
			for k := range keys {
				sorted = append(sorted, k)
			}
			sort.Strings(sorted)
			for _, k := range sorted {
				walk(append(append([]any{}, path...), k), bm[k], am[k], um[k], depth+1)
			}
		case depth < maxDepth && (bIsList || before == nil) && (aIsList || after == nil) && (bIsList || aIsList || ul != nil):
			n := max(len(bl), len(al), len(ul))
			for i := 0; i < n; i++ {
				var b, a, u any
				if i < len(bl) {
					b = bl[i]
				}
				if i < len(al) {
					a = al[i]
				}
				if i < len(ul) {
					u = ul[i]
				}
				walk(append(append([]any{}, path...), i), b, a, u, depth+1)
			}
		default:
			if !reflect.DeepEqual(before, after) {
				rows = append(rows, row(ch, path, before, after, false))
			}
		}
	}
	walk(nil, ch.Before, ch.After, ch.AfterUnknown, 0)
	return rows, truncated, nil
}

func row(ch rawChange, path []any, before, after any, unknown bool) AttributeChange {
	r := AttributeChange{Path: render(path), Unknown: unknown}
	bs, as := sensitiveAt(ch.BeforeSensitive, path), sensitiveAt(ch.AfterSensitive, path)
	r.Sensitive = bs || as
	r.Before = leaf(before, path, bs)
	if unknown {
		r.After = KnownAfterApply
	} else {
		r.After = leaf(after, path, as)
	}
	switch {
	case unknown:
		r.Kind = "unknown"
	case before == nil:
		r.Kind = "added"
	case after == nil:
		r.Kind = "removed"
	default:
		r.Kind = "changed"
	}
	for _, p := range ch.ReplacePaths {
		if prefix(p, path) || prefix(path, p) {
			r.ForcesReplacement = true
		}
	}
	return r
}

// leaf redacts a value at a path: any risky ancestor key or sensitivity marker hides it.
func leaf(value any, path []any, sensitive bool) any {
	if value == nil {
		return nil
	}
	if sensitive {
		return state.Redacted
	}
	key := ""
	for _, segment := range path {
		if k, ok := segment.(string); ok {
			if state.RiskyKey(k) {
				return state.Redacted
			}
			key = k
		}
	}
	return state.Redact(value, nil, key)
}

func sensitiveAt(marker any, path []any) bool {
	for _, segment := range path {
		if marker == true {
			return true
		}
		switch m := marker.(type) {
		case map[string]any:
			k, _ := segment.(string)
			marker = m[k]
		case []any:
			i, ok := segment.(int)
			if !ok || i >= len(m) {
				return false
			}
			marker = m[i]
		default:
			return false
		}
	}
	return marker == true
}

func prefix(short, long []any) bool {
	if len(short) > len(long) {
		return false
	}
	for i := range short {
		if fmt.Sprint(short[i]) != fmt.Sprint(long[i]) {
			return false
		}
	}
	return true
}

// render formats a path like tags["Name"] or ingress[0].cidr_blocks; secret-shaped keys are masked.
func render(path []any) string {
	var b strings.Builder
	for i, segment := range path {
		switch s := segment.(type) {
		case int:
			b.WriteString("[" + strconv.Itoa(s) + "]")
		case string:
			switch {
			case state.Suspicious(s) || len(s) > 128:
				b.WriteString("[redacted key]")
			case identPattern.MatchString(s):
				if i > 0 {
					b.WriteString(".")
				}
				b.WriteString(s)
			default:
				quoted, _ := json.Marshal(s)
				b.WriteString("[" + string(quoted) + "]")
			}
		}
	}
	if b.Len() == 0 {
		return "(value)"
	}
	return b.String()
}

// overlayUnknown marks unknown values in an already-redacted tree.
func overlayUnknown(redacted, unknown any, depth int) any {
	if unknown == true {
		return KnownAfterApply
	}
	if depth > maxDepth {
		return redacted
	}
	switch u := unknown.(type) {
	case map[string]any:
		m, ok := redacted.(map[string]any)
		if !ok {
			if redacted != nil {
				return redacted
			}
			m = map[string]any{}
		}
		for k, item := range u {
			if state.Suspicious(k) || len(k) > 128 {
				continue
			}
			if v := overlayUnknown(m[k], item, depth+1); v != nil {
				m[k] = v
			}
		}
		if len(m) == 0 {
			return redacted
		}
		return m
	case []any:
		l, ok := redacted.([]any)
		if !ok {
			return redacted
		}
		for i := range l {
			if i < len(u) {
				l[i] = overlayUnknown(l[i], u[i], depth+1)
			}
		}
		return l
	}
	return redacted
}
