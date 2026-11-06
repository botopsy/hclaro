package compare

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"hclaro/internal/state"
)

// Match statuses. "Different" is never "drift": both sides are recorded state, and an
// expected environment difference looks exactly like an unexpected one.
const (
	OnlyLeft     = "only-left"
	OnlyRight    = "only-right"
	Same         = "same"
	Different    = "attribute-different"
	Unknown      = "unknown"
	Uncomparable = "uncomparable"
)

// Statuses lists every match status in display order.
var Statuses = []string{Different, OnlyLeft, OnlyRight, Unknown, Uncomparable, Same}

// Attribute row statuses.
const (
	AttrSame      = "same"
	AttrChanged   = "different"
	AttrOnlyLeft  = "only-left"
	AttrOnlyRight = "only-right"
	// AttrSensitive: a value is redacted on at least one side, so it was not compared.
	AttrSensitive = "sensitive"
	// AttrIgnored: an ignore rule matched; the values are not returned.
	AttrIgnored = "ignored"
)

// Limits keep one comparison bounded whatever the state size.
const (
	maxRows      = 2000
	maxDepth     = 32
	maxListPaths = 5
)

// Counts tallies matches by status.
type Counts struct {
	Total        int `json:"total"`
	Same         int `json:"same"`
	Different    int `json:"different"`
	OnlyLeft     int `json:"onlyLeft"`
	OnlyRight    int `json:"onlyRight"`
	Unknown      int `json:"unknown"`
	Uncomparable int `json:"uncomparable"`
}

func (c *Counts) add(status string) {
	c.Total++
	switch status {
	case Same:
		c.Same++
	case Different:
		c.Different++
	case OnlyLeft:
		c.OnlyLeft++
	case OnlyRight:
		c.OnlyRight++
	case Unknown:
		c.Unknown++
	case Uncomparable:
		c.Uncomparable++
	}
}

// Summary describes one comparison.
type Summary struct {
	Managed Counts `json:"managed"`
	Data    Counts `json:"data"`
	// Mapped counts pairs matched through a mapping rule rather than an identical address.
	Mapped int `json:"mapped"`
	// IgnoredAttributes counts attribute values suppressed by ignore rules; Normalized counts
	// values that compared equal only after a substitution rule.
	IgnoredAttributes int `json:"ignoredAttributes"`
	Normalized        int `json:"normalized"`
	// NotCompared counts redacted attribute values that were never compared.
	NotCompared int `json:"notCompared"`
	// RuleHits has one count per rule, in rule order: addresses mapped, values ignored or
	// values normalized.
	RuleHits []int `json:"ruleHits"`
	Left     Side  `json:"left"`
	Right    Side  `json:"right"`
}

// Side counts one snapshot.
type Side struct {
	Managed   int `json:"managed"`
	Data      int `json:"data"`
	Modules   int `json:"modules"`
	Providers int `json:"providers"`
}

// Match is one row: a left instance, a right instance, or a pair.
type Match struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Mode         string `json:"mode"`
	Type         string `json:"type"`
	LeftAddress  string `json:"leftAddress,omitempty"`
	RightAddress string `json:"rightAddress,omitempty"`
	// LeftID and RightID are the explorer resource IDs in each side's read context.
	LeftID   string `json:"leftId,omitempty"`
	RightID  string `json:"rightId,omitempty"`
	Module   string `json:"module"`
	Provider string `json:"provider"`
	// Rule is the 1-based mapping rule that paired the addresses, 0 for identical addresses.
	Rule int `json:"rule,omitempty"`
	// Reason explains unknown and uncomparable rows.
	Reason string `json:"reason,omitempty"`
	// ProviderNote is set when both sides use one provider from different registries.
	ProviderNote string `json:"providerNote,omitempty"`
	Differences  int    `json:"differences"`
	NotCompared  int    `json:"notCompared"`
	Ignored      int    `json:"ignored"`
	Normalized   int    `json:"normalized"`
	// Paths lists the first differing attribute paths.
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated,omitempty"`
}

// ModuleRow aligns one module path across both sides.
type ModuleRow struct {
	Address     string `json:"address"`
	LeftAddress string `json:"leftAddress,omitempty"`
	Left        int    `json:"left"`
	Right       int    `json:"right"`
	Status      string `json:"status"`
	Counts      Counts `json:"counts"`
}

// ProviderRow aligns one provider across both sides. State records provider sources, not
// versions or aliases, so versions are never reported from a snapshot.
type ProviderRow struct {
	Source      string   `json:"source"`
	LeftSource  string   `json:"leftSource,omitempty"`
	RightSource string   `json:"rightSource,omitempty"`
	Left        int      `json:"left"`
	Right       int      `json:"right"`
	Status      string   `json:"status"`
	Notes       []string `json:"notes"`
}

// Result is a complete comparison. Matches are sorted by address.
type Result struct {
	Summary   Summary       `json:"summary"`
	Matches   []Match       `json:"-"`
	Modules   []ModuleRow   `json:"modules"`
	Providers []ProviderRow `json:"providers"`
	Warnings  []string      `json:"warnings"`
	byID      map[string]int
	built     bool
	left      map[string]state.Resource
	right     map[string]state.Resource
	rules     []Compiled
}

// Attribute is one compared attribute path. Values are already redacted by the state parser;
// sensitive and ignored rows carry no values at all.
type Attribute struct {
	Path       string `json:"path"`
	Status     string `json:"status"`
	Left       any    `json:"left,omitempty"`
	Right      any    `json:"right,omitempty"`
	Normalized bool   `json:"normalized,omitempty"`
	Rule       int    `json:"rule,omitempty"`
}

// Detail is one match with its attribute rows and both redacted attribute trees.
type Detail struct {
	Match
	Attributes []Attribute `json:"attributes"`
	Left       any         `json:"left"`
	Right      any         `json:"right"`
}

// Compare matches left and right by canonical address, applying mapping rules to left
// addresses, and classifies each match. Rules must come from Validate.
func Compare(left, right state.Index, rules []Compiled) *Result {
	res := &Result{byID: map[string]int{}, left: map[string]state.Resource{}, right: map[string]state.Resource{}, rules: rules, Warnings: []string{}}
	res.Summary.RuleHits = make([]int, len(rules))
	res.Summary.Left = sideOf(left)
	res.Summary.Right = sideOf(right)
	rightBy := map[string]state.Resource{}
	for _, r := range right.Resources {
		rightBy[r.Address] = r
		res.right[r.Address] = r
	}
	// Identical addresses are claimed first; a mapped address that collides with one, or
	// with an earlier mapped address, is refused as a conflict rather than guessed.
	claimed := map[string]bool{}
	type pending struct {
		r      state.Resource
		target string
		rule   int
	}
	var mapped []pending
	var unmatched []state.Resource
	for _, l := range left.Resources {
		res.left[l.Address] = l
		target, rule := l.Address, 0
		for i, c := range rules {
			if c.Kind != RuleMap {
				continue
			}
			if t, ok := mapAddress(l.Address, c.Left, c.Right); ok {
				target, rule = t, i+1
				break
			}
		}
		if rule > 0 {
			mapped = append(mapped, pending{l, target, rule})
			continue
		}
		if r, ok := rightBy[l.Address]; ok {
			claimed[l.Address] = true
			res.add(res.pair(l, r, 0))
		} else {
			unmatched = append(unmatched, l)
		}
	}
	conflicts := map[int]bool{}
	for _, m := range mapped {
		res.Summary.RuleHits[m.rule-1]++
		r, ok := rightBy[m.target]
		switch {
		case ok && claimed[m.target]:
			conflicts[m.rule] = true
			res.add(Match{Status: Uncomparable, Mode: m.r.Mode, Type: m.r.Type, LeftAddress: m.r.Address, LeftID: m.r.ID, Module: res.mapModule(m.r.Module), Provider: m.r.Provider, Rule: m.rule, Reason: "Mapping rule " + strconv.Itoa(m.rule) + " maps this address onto " + m.target + ", which is already matched. Change the rule so each right address is matched once.", Paths: []string{}})
		case ok:
			claimed[m.target] = true
			res.Summary.Mapped++
			res.add(res.pair(m.r, r, m.rule))
		default:
			unmatched = append(unmatched, m.r)
		}
	}
	for rule := range conflicts {
		res.Warnings = append(res.Warnings, "Mapping rule "+strconv.Itoa(rule)+" maps addresses onto right addresses that are already matched. Those instances are marked uncomparable.")
	}
	for _, l := range unmatched {
		res.add(Match{Status: OnlyLeft, Mode: l.Mode, Type: l.Type, LeftAddress: l.Address, LeftID: l.ID, Module: res.mapModule(l.Module), Provider: l.Provider, Paths: []string{}})
	}
	for _, r := range right.Resources {
		if !claimed[r.Address] {
			res.add(Match{Status: OnlyRight, Mode: r.Mode, Type: r.Type, RightAddress: r.Address, RightID: r.ID, Module: r.Module, Provider: r.Provider, Paths: []string{}})
		}
	}
	for i, c := range rules {
		if c.Kind == RuleMap && res.Summary.RuleHits[i] == 0 {
			res.Warnings = append(res.Warnings, "Mapping rule "+strconv.Itoa(i+1)+" ("+c.Left+" → "+c.Right+") matched no left address.")
		}
	}
	sort.Slice(res.Matches, func(i, j int) bool {
		a, b := res.Matches[i].sortKey(), res.Matches[j].sortKey()
		if a != b {
			return a < b
		}
		return res.Matches[i].ID < res.Matches[j].ID
	})
	for i, m := range res.Matches {
		res.byID[m.ID] = i
	}
	res.modules(left, right)
	res.providers(left, right)
	res.built = true
	return res
}

// mapModule applies the first matching mapping rule to a module address.
func (res *Result) mapModule(addr string) string {
	for _, c := range res.rules {
		if c.Kind != RuleMap {
			continue
		}
		if t, ok := mapAddress(addr, c.Left, c.Right); ok {
			return t
		}
	}
	return addr
}

func (m Match) sortKey() string {
	if m.RightAddress != "" {
		return m.RightAddress
	}
	return m.LeftAddress
}

func sideOf(idx state.Index) Side {
	return Side{Managed: idx.Managed, Data: idx.Data, Modules: len(idx.Modules), Providers: len(idx.Providers)}
}

func (res *Result) add(m Match) {
	sum := sha256.Sum256([]byte("L\x00" + m.LeftAddress + "\x00R\x00" + m.RightAddress))
	m.ID = hex.EncodeToString(sum[:16])
	if m.Paths == nil {
		m.Paths = []string{}
	}
	if m.Mode == "data" {
		res.Summary.Data.add(m.Status)
	} else {
		res.Summary.Managed.add(m.Status)
	}
	res.Summary.NotCompared += m.NotCompared
	res.Summary.IgnoredAttributes += m.Ignored
	res.Summary.Normalized += m.Normalized
	res.Matches = append(res.Matches, m)
}

// registries are the default registries. A provider from either is the same provider
// for comparison, which is what a Terraform and an OpenTofu root of one stack record.
var registries = []string{"registry.terraform.io/", "registry.opentofu.org/"}

var providerPattern = regexp.MustCompile(`^provider\["(.+)"\](\..+)?$`)

// ProviderSource returns the provider source without a default registry host and without
// the provider["…"] wrapper some state versions record.
func ProviderSource(p string) string {
	if m := providerPattern.FindStringSubmatch(p); m != nil {
		p = m[1]
	}
	for _, r := range registries {
		if strings.HasPrefix(p, r) {
			return strings.TrimPrefix(p, r)
		}
	}
	return p
}

func (res *Result) pair(l, r state.Resource, rule int) Match {
	m := Match{Mode: r.Mode, Type: r.Type, LeftAddress: l.Address, RightAddress: r.Address, LeftID: l.ID, RightID: r.ID, Module: r.Module, Provider: r.Provider, Rule: rule}
	switch {
	case l.Mode != r.Mode:
		m.Status, m.Reason = Uncomparable, "One side is a managed resource and the other a data source."
		return m
	case l.Type != r.Type:
		m.Status, m.Reason = Uncomparable, "The resource types differ ("+l.Type+" and "+r.Type+")."
		return m
	case ProviderSource(l.Provider) != ProviderSource(r.Provider):
		m.Status, m.Reason = Uncomparable, "The provider sources differ ("+ProviderSource(l.Provider)+" and "+ProviderSource(r.Provider)+"), so attributes may not mean the same thing."
		return m
	case l.Attributes == state.Redacted || r.Attributes == state.Redacted:
		m.Status, m.Reason = Uncomparable, "The attributes of one side are withheld by redaction."
		return m
	}
	if l.Provider != r.Provider {
		m.ProviderNote = "Same provider from different registries (" + l.Provider + " and " + r.Provider + ")."
	}
	rows, truncated := res.diff(l, r, false)
	m.Truncated = truncated
	for _, a := range rows {
		switch a.Status {
		case AttrChanged, AttrOnlyLeft, AttrOnlyRight:
			m.Differences++
			if len(m.Paths) < maxListPaths {
				m.Paths = append(m.Paths, a.Path)
			}
		case AttrSensitive:
			m.NotCompared++
		case AttrIgnored:
			m.Ignored++
		}
		if a.Normalized {
			m.Normalized++
		}
	}
	switch {
	case m.Differences > 0:
		m.Status = Different
	case m.NotCompared > 0:
		m.Status, m.Reason = Unknown, "No visible differences, but "+plural(m.NotCompared, "redacted value was", "redacted values were")+" not compared. Equality cannot be confirmed."
	case truncated:
		m.Status, m.Reason = Unknown, "The attributes are too large to compare completely."
	default:
		m.Status = Same
	}
	return m
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// diff walks both redacted attribute trees. With all set it also returns same rows (for the
// detail view); otherwise it keeps only rows that matter for classification.
func (res *Result) diff(l, r state.Resource, all bool) ([]Attribute, bool) {
	rows := []Attribute{}
	truncated := false
	var walk func(path []any, a, b any, depth int)
	walk = func(path []any, a, b any, depth int) {
		if len(rows) >= maxRows {
			truncated = true
			return
		}
		if idx, ok := res.ignored(r.Type, path); ok {
			res.hit(idx)
			rows = append(rows, Attribute{Path: render(path), Status: AttrIgnored, Rule: idx + 1})
			return
		}
		am, aMap := a.(map[string]any)
		bm, bMap := b.(map[string]any)
		al, aList := a.([]any)
		bl, bList := b.([]any)
		switch {
		case a == state.Redacted || b == state.Redacted:
			rows = append(rows, Attribute{Path: render(path), Status: AttrSensitive})
		case aMap && bMap && depth < maxDepth:
			keys := make([]string, 0, len(am)+len(bm))
			for k := range am {
				keys = append(keys, k)
			}
			for k := range bm {
				if _, ok := am[k]; !ok {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for _, k := range keys {
				av, aok := am[k]
				bv, bok := bm[k]
				child := append(append([]any{}, path...), k)
				switch {
				case k == "[redacted key]":
					rows = append(rows, Attribute{Path: render(child), Status: AttrSensitive})
				case !aok:
					res.oneSided(&rows, child, r.Type, AttrOnlyRight, bv)
				case !bok:
					res.oneSided(&rows, child, r.Type, AttrOnlyLeft, av)
				default:
					walk(child, av, bv, depth+1)
				}
			}
		case aList && bList && depth < maxDepth:
			for i := 0; i < max(len(al), len(bl)); i++ {
				child := append(append([]any{}, path...), i)
				switch {
				case i >= len(al):
					res.oneSided(&rows, child, r.Type, AttrOnlyRight, bl[i])
				case i >= len(bl):
					res.oneSided(&rows, child, r.Type, AttrOnlyLeft, al[i])
				default:
					walk(child, al[i], bl[i], depth+1)
				}
			}
		default:
			if containsRedacted(a, 0) || containsRedacted(b, 0) {
				rows = append(rows, Attribute{Path: render(path), Status: AttrSensitive})
				return
			}
			if reflect.DeepEqual(a, b) {
				if all {
					rows = append(rows, Attribute{Path: render(path), Status: AttrSame, Left: a, Right: b})
				}
				return
			}
			if as, ok := a.(string); ok {
				if bs, ok := b.(string); ok {
					if idx, ok := res.substitutes(r.Type, path, as, bs); ok {
						res.hit(idx)
						rows = append(rows, Attribute{Path: render(path), Status: AttrSame, Left: a, Right: b, Normalized: true, Rule: idx + 1})
						return
					}
				}
			}
			rows = append(rows, Attribute{Path: render(path), Status: AttrChanged, Left: a, Right: b})
		}
	}
	walk(nil, l.Attributes, r.Attributes, 0)
	if !all {
		rows = relevant(rows)
	}
	return rows, truncated
}

// hits are counted only while the result is built, not again for detail views, which may
// run concurrently.
func (res *Result) hit(idx int) {
	if !res.built {
		res.Summary.RuleHits[idx]++
	}
}

func relevant(rows []Attribute) []Attribute {
	out := rows[:0]
	for _, r := range rows {
		if r.Status != AttrSame || r.Normalized {
			out = append(out, r)
		}
	}
	return out
}

// oneSided records a value present on one side only. A redacted value is still reported
// as present, never with its value.
func (res *Result) oneSided(rows *[]Attribute, path []any, typ, status string, value any) {
	if idx, ok := res.ignored(typ, path); ok {
		res.hit(idx)
		*rows = append(*rows, Attribute{Path: render(path), Status: AttrIgnored, Rule: idx + 1})
		return
	}
	a := Attribute{Path: render(path), Status: status}
	if !containsRedacted(value, 0) {
		if status == AttrOnlyLeft {
			a.Left = value
		} else {
			a.Right = value
		}
	}
	*rows = append(*rows, a)
}

func containsRedacted(v any, depth int) bool {
	if depth > maxDepth {
		return true
	}
	switch t := v.(type) {
	case string:
		return t == state.Redacted
	case map[string]any:
		for k, item := range t {
			if k == "[redacted key]" || containsRedacted(item, depth+1) {
				return true
			}
		}
	case []any:
		for _, item := range t {
			if containsRedacted(item, depth+1) {
				return true
			}
		}
	}
	return false
}

func (res *Result) ignored(typ string, path []any) (int, bool) {
	if len(path) == 0 {
		return 0, false
	}
	for i, c := range res.rules {
		if c.Kind == RuleIgnore && (c.ResourceType == "" || c.ResourceType == typ) && matches(c.pattern, path) {
			return i, true
		}
	}
	return 0, false
}

func (res *Result) substitutes(typ string, path []any, a, b string) (int, bool) {
	for i, c := range res.rules {
		if c.Kind != RuleSubstitute || c.ResourceType != "" && c.ResourceType != typ || c.pattern != nil && !matches(c.pattern, path) {
			continue
		}
		if strings.Contains(a, c.Left) && strings.ReplaceAll(a, c.Left, c.Right) == b {
			return i, true
		}
	}
	return 0, false
}

var identPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// render formats a path like attribute rows elsewhere: tags.Name, ingress[0].cidr_blocks[1],
// tags["cost.center"].
func render(path []any) string {
	var b strings.Builder
	for i, segment := range path {
		switch s := segment.(type) {
		case int:
			b.WriteString("[" + strconv.Itoa(s) + "]")
		case string:
			if identPattern.MatchString(s) {
				if i > 0 {
					b.WriteString(".")
				}
				b.WriteString(s)
			} else {
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

// Match returns one match by ID.
func (res *Result) Match(id string) (Match, bool) {
	i, ok := res.byID[id]
	if !ok {
		return Match{}, false
	}
	return res.Matches[i], true
}

// Detail returns a match with every attribute row and both redacted trees.
func (res *Result) Detail(id string) (Detail, bool) {
	m, ok := res.Match(id)
	if !ok {
		return Detail{}, false
	}
	d := Detail{Match: m, Attributes: []Attribute{}}
	l, lok := res.left[m.LeftAddress]
	r, rok := res.right[m.RightAddress]
	if lok {
		d.Left = l.Attributes
	}
	if rok {
		d.Right = r.Attributes
	}
	if lok && rok && (m.Status == Same || m.Status == Different || m.Status == Unknown) {
		d.Attributes, _ = res.diff(l, r, true)
	}
	return d, true
}

func (res *Result) modules(left, right state.Index) {
	rows := map[string]*ModuleRow{}
	get := func(addr string) *ModuleRow {
		if row, ok := rows[addr]; ok {
			return row
		}
		row := &ModuleRow{Address: addr}
		rows[addr] = row
		return row
	}
	for _, m := range left.Modules {
		target := res.mapModule(m.Address)
		row := get(target)
		row.Left += m.Count
		if target != m.Address {
			row.LeftAddress = m.Address
		}
	}
	for _, m := range right.Modules {
		get(m.Address).Right += m.Count
	}
	for _, m := range res.Matches {
		row := get(m.Module)
		row.Counts.add(m.Status)
	}
	leftHas, rightHas := map[string]bool{}, map[string]bool{}
	for _, m := range left.Modules {
		leftHas[m.Address] = true
	}
	for _, m := range right.Modules {
		rightHas[m.Address] = true
	}
	res.Modules = make([]ModuleRow, 0, len(rows))
	for addr, row := range rows {
		inLeft := leftHas[addr] || row.LeftAddress != ""
		switch {
		case inLeft && rightHas[addr]:
			row.Status = "both"
		case inLeft:
			row.Status = OnlyLeft
		case rightHas[addr]:
			row.Status = OnlyRight
		default:
			// A module with no direct resources on either side, reached through a mapped
			// module path of a left-only instance.
			row.Status = OnlyLeft
		}
		res.Modules = append(res.Modules, *row)
	}
	sort.Slice(res.Modules, func(i, j int) bool { return res.Modules[i].Address < res.Modules[j].Address })
}

func (res *Result) providers(left, right state.Index) {
	rows := map[string]*ProviderRow{}
	get := func(p string) *ProviderRow {
		key := ProviderSource(p)
		if row, ok := rows[key]; ok {
			return row
		}
		row := &ProviderRow{Source: key, Notes: []string{}}
		rows[key] = row
		return row
	}
	for p, n := range left.Providers {
		row := get(p)
		row.Left += n
		row.LeftSource = p
	}
	for p, n := range right.Providers {
		row := get(p)
		row.Right += n
		row.RightSource = p
	}
	res.Providers = make([]ProviderRow, 0, len(rows))
	for _, row := range rows {
		switch {
		case row.Left > 0 && row.Right > 0:
			row.Status = "both"
			if row.LeftSource != row.RightSource {
				row.Notes = append(row.Notes, "Recorded from different registries: "+row.LeftSource+" and "+row.RightSource+".")
			}
		case row.Left > 0:
			row.Status = OnlyLeft
		default:
			row.Status = OnlyRight
		}
		res.Providers = append(res.Providers, *row)
	}
	sort.Slice(res.Providers, func(i, j int) bool { return res.Providers[i].Source < res.Providers[j].Source })
}
