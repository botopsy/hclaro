package cost

import (
	"bytes"
	"encoding/json"
	"math/big"
	"regexp"
	"sort"
	"strings"

	"hclaro/internal/plan"
	"hclaro/internal/state"
)

// Item statuses. Only estimated and partial items carry amounts.
const (
	StatusEstimated    = "estimated"
	StatusPartial      = "partial"
	StatusUsageUnknown = "usage_unknown"
	StatusUnsupported  = "unsupported"
	StatusFree         = "free"
	StatusNotEstimated = "not_estimated"
)

const (
	maxItems      = 2000
	maxComponents = 20
	maxResources  = 100_000
)

// CostComponent is one priced dimension of a resource as reported by the provider.
// Amounts are decimal strings; nil means the provider gave no price (usage-based).
type CostComponent struct {
	Name            string  `json:"name"`
	Unit            string  `json:"unit"`
	MonthlyQuantity *string `json:"monthlyQuantity"`
	Price           *string `json:"price"`
	MonthlyCost     *string `json:"monthlyCost"`
	UsageBased      bool    `json:"usageBased,omitempty"`
}

// CostLineItem is the estimate for one changed resource instance of the plan.
type CostLineItem struct {
	Address      string `json:"address"`
	ResourceType string `json:"resourceType"`
	Action       string `json:"action"`
	Status       string `json:"status"`
	// PastMonthly, Monthly and Delta are rounded to cents and nil when not estimated.
	PastMonthly *string `json:"pastMonthly"`
	Monthly     *string `json:"monthly"`
	Delta       *string `json:"delta"`
	// UnpricedComponents counts components without a price (usage-based or unknown).
	UnpricedComponents int             `json:"unpricedComponents"`
	Components         []CostComponent `json:"components"`
	Note               string          `json:"note,omitempty"`
	delta              *big.Rat
}

// TypeSummary aggregates changed instances by resource type.
type TypeSummary struct {
	ResourceType string  `json:"resourceType"`
	Count        int     `json:"count"`
	Estimated    int     `json:"estimated"`
	NotEstimated int     `json:"notEstimated"`
	Delta        *string `json:"delta"`
}

// EstimateCoverage says how much of the plan's change the figures cover. Percent is the
// share of cost-bearing changed instances (all changed managed instances except those the
// provider reports as free) that have a price; nil when nothing is cost-bearing.
type EstimateCoverage struct {
	Changed         int            `json:"changed"`
	CostBearing     int            `json:"costBearing"`
	Estimated       int            `json:"estimated"`
	Partial         int            `json:"partial"`
	UsageUnknown    int            `json:"usageUnknown"`
	Unsupported     int            `json:"unsupported"`
	Free            int            `json:"free"`
	NotEstimated    int            `json:"notEstimated"`
	Percent         *int           `json:"percent"`
	UnsupportedType map[string]int `json:"unsupportedTypes"`
}

// Caveat is a typed limitation of an estimate.
type Caveat struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   int    `json:"count,omitempty"`
}

// Totals are the aggregate amounts of one estimate, rounded to cents.
type Totals struct {
	Currency string `json:"currency"`
	// MonthlyDelta is the sum of the estimated items' deltas: the projected recurring monthly
	// change of the plan. Added, Removed and Changed split it by action.
	MonthlyDelta   string `json:"monthlyDelta"`
	AddedMonthly   string `json:"addedMonthly"`
	RemovedMonthly string `json:"removedMonthly"`
	ChangedDelta   string `json:"changedDelta"`
	// PastMonthly and ProjectedMonthly cover every priced resource of the configuration,
	// before and after the plan, including unchanged ones.
	PastMonthly      string `json:"pastMonthly"`
	ProjectedMonthly string `json:"projectedMonthly"`
}

// Breakdown is the aggregated, sanitized result of one provider report.
type Breakdown struct {
	Totals
	Coverage  EstimateCoverage `json:"coverage"`
	Caveats   []Caveat         `json:"caveats"`
	Types     []TypeSummary    `json:"types"`
	Items     []CostLineItem   `json:"items"`
	Truncated bool             `json:"truncated"`
}

// PlanChange is one resource change of the reviewed plan.
type PlanChange struct {
	Address string
	Type    string
	Mode    string
	Action  string
}

// ChangesOf lists the managed resource changes of a review.
func ChangesOf(r plan.Review) []PlanChange {
	out := make([]PlanChange, 0, len(r.Changes))
	for _, c := range r.Changes {
		if c.Deposed != "" {
			continue
		}
		out = append(out, PlanChange{Address: c.Address, Type: c.Type, Mode: c.Mode, Action: c.Action})
	}
	return out
}

type rawComponent struct {
	Name            string  `json:"name"`
	Unit            string  `json:"unit"`
	MonthlyQuantity *string `json:"monthlyQuantity"`
	Price           *string `json:"price"`
	MonthlyCost     *string `json:"monthlyCost"`
	UsageBased      bool    `json:"usageBased"`
}

type rawResource struct {
	Name           string         `json:"name"`
	ResourceType   string         `json:"resourceType"`
	CostComponents []rawComponent `json:"costComponents"`
	Subresources   []rawResource  `json:"subresources"`
}

type rawBreakdown struct {
	Resources []rawResource `json:"resources"`
}

type rawSummary struct {
	Unsupported map[string]int `json:"unsupportedResourceCounts"`
	NoPrice     map[string]int `json:"noPriceResourceCounts"`
}

type rawProject struct {
	Breakdown     *rawBreakdown `json:"breakdown"`
	PastBreakdown *rawBreakdown `json:"pastBreakdown"`
	Summary       *rawSummary   `json:"summary"`
}

type rawReport struct {
	Version  string       `json:"version"`
	Currency string       `json:"currency"`
	Projects []rawProject `json:"projects"`
	Summary  *rawSummary  `json:"summary"`
}

var (
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	decimalPattern  = regexp.MustCompile(`^-?[0-9]{1,15}(\.[0-9]{1,12})?$`)
	reportVersion   = regexp.MustCompile(`^0\.[0-9]+$`)
)

func decimal(s *string) (*big.Rat, bool) {
	if s == nil {
		return nil, true
	}
	if !decimalPattern.MatchString(*s) {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(*s)
	return r, ok
}

// Money formats r to cents, rounding half away from zero.
func Money(r *big.Rat) string { return r.FloatString(2) }

func moneyPtr(r *big.Rat) *string {
	if r == nil {
		return nil
	}
	s := Money(r)
	return &s
}

// trimmed formats a quantity or unit price with up to digits decimals, without trailing zeros.
func trimmed(s *string, digits int) *string {
	r, ok := decimal(s)
	if !ok || r == nil {
		return nil
	}
	out := r.FloatString(digits)
	if strings.Contains(out, ".") {
		out = strings.TrimRight(strings.TrimRight(out, "0"), ".")
	}
	return &out
}

func safeText(s string, limit int) string {
	if len(s) > limit || state.Suspicious(s) || state.LooksSecret(s) || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "[withheld]"
	}
	return s
}

// priced sums the known monthly costs of a resource and its subresources and counts the
// components without a price.
type priced struct {
	known      *big.Rat
	unpriced   int
	components []CostComponent
}

func price(r rawResource, depth int, into *priced) bool {
	if depth > 16 {
		return false
	}
	for _, c := range r.CostComponents {
		cost, ok := decimal(c.MonthlyCost)
		if !ok {
			return false
		}
		if _, ok := decimal(c.MonthlyQuantity); !ok {
			return false
		}
		if _, ok := decimal(c.Price); !ok {
			return false
		}
		if cost == nil {
			into.unpriced++
		} else {
			if into.known == nil {
				into.known = new(big.Rat)
			}
			into.known.Add(into.known, cost)
		}
		if len(into.components) < maxComponents {
			into.components = append(into.components, CostComponent{
				Name: safeText(c.Name, 200), Unit: safeText(c.Unit, 32),
				MonthlyQuantity: trimmed(c.MonthlyQuantity, 4), Price: trimmed(c.Price, 6),
				MonthlyCost: moneyPtr(cost), UsageBased: c.UsageBased,
			})
		}
	}
	for _, s := range r.Subresources {
		if !price(s, depth+1, into) {
			return false
		}
	}
	return true
}

func zero() *big.Rat { return new(big.Rat) }

func orZero(r *big.Rat) *big.Rat {
	if r == nil {
		return zero()
	}
	return r
}

// Aggregate parses a provider report (Infracost JSON output, schema 0.x) and computes the
// estimate of the plan's changes. Amounts are summed exactly and rounded only for display.
// A price is attributed only to addresses the reviewed plan contains; nothing is invented
// for resources the provider did not price.
func Aggregate(report []byte, changes []PlanChange) (Breakdown, error) {
	dec := json.NewDecoder(bytes.NewReader(report))
	var raw rawReport
	if dec.Decode(&raw) != nil || dec.More() || !reportVersion.MatchString(raw.Version) {
		return Breakdown{}, fail(KindMalformed)
	}
	if !currencyPattern.MatchString(raw.Currency) {
		return Breakdown{}, fail(KindMalformed)
	}
	known := map[string]PlanChange{}
	for _, c := range changes {
		known[c.Address] = c
	}
	past, now := map[string]priced{}, map[string]priced{}
	unsupported, noPrice := map[string]int{}, map[string]int{}
	unattributed, count := 0, 0
	addSummary := func(s *rawSummary) {
		if s == nil {
			return
		}
		for t, n := range s.Unsupported {
			if safeName(t) && n > 0 {
				unsupported[t] = max(unsupported[t], n)
			}
		}
		for t, n := range s.NoPrice {
			if safeName(t) && n > 0 {
				noPrice[t] = max(noPrice[t], n)
			}
		}
	}
	collect := func(b *rawBreakdown, into map[string]priced) bool {
		if b == nil {
			return true
		}
		for _, r := range b.Resources {
			count++
			if count > maxResources {
				return false
			}
			var p priced
			if !price(r, 0, &p) {
				return false
			}
			if _, ok := known[r.Name]; !ok {
				if p.known != nil && p.known.Sign() != 0 {
					unattributed++
				}
				continue
			}
			if prev, dup := into[r.Name]; dup {
				// The same address in two projects: add both, the provider priced it twice.
				if p.known != nil {
					prev.known = new(big.Rat).Add(orZero(prev.known), p.known)
				}
				prev.unpriced += p.unpriced
				into[r.Name] = prev
				continue
			}
			into[r.Name] = p
		}
		return true
	}
	for _, p := range raw.Projects {
		if !collect(p.PastBreakdown, past) || !collect(p.Breakdown, now) {
			return Breakdown{}, fail(KindMalformed)
		}
		addSummary(p.Summary)
	}
	addSummary(raw.Summary)

	out := Breakdown{Totals: Totals{Currency: raw.Currency}, Coverage: EstimateCoverage{UnsupportedType: map[string]int{}}, Caveats: []Caveat{}, Types: []TypeSummary{}, Items: []CostLineItem{}}
	pastTotal, nowTotal := zero(), zero()
	for _, p := range past {
		pastTotal.Add(pastTotal, orZero(p.known))
	}
	for _, p := range now {
		nowTotal.Add(nowTotal, orZero(p.known))
	}
	delta, added, removed, changed := zero(), zero(), zero(), zero()
	usageUnpriced, forgotten := 0, 0
	types := map[string]*TypeSummary{}
	typeDelta := map[string]*big.Rat{}
	for _, c := range changes {
		if c.Mode != "managed" {
			continue
		}
		switch c.Action {
		case plan.NoOp, plan.Read:
			continue
		}
		item := CostLineItem{Address: c.Address, ResourceType: c.Type, Action: c.Action, Components: []CostComponent{}}
		p, hasPast := past[c.Address]
		n, hasNow := now[c.Address]
		pastExpected := c.Action != plan.Create
		nowExpected := c.Action != plan.Delete && c.Action != plan.Forget
		out.Coverage.Changed++
		switch {
		case c.Action == plan.Forget:
			// Forgetting removes the object from state only; it keeps running and billing.
			item.Status, item.Note = StatusNotEstimated, "Removed from state only: the object keeps running and billing outside this configuration."
			forgotten++
		case c.Action == plan.Unknown:
			item.Status, item.Note = StatusNotEstimated, "Unrecognized action; no estimate."
		case !hasPast && !hasNow:
			switch {
			case noPrice[c.Type] > 0:
				item.Status = StatusFree
			case unsupported[c.Type] > 0:
				item.Status = StatusUnsupported
				out.Coverage.UnsupportedType[c.Type]++
			default:
				item.Status = StatusNotEstimated
			}
		case (pastExpected && !hasPast) || (nowExpected && !hasNow):
			// One side of a change is missing: a delta would be a guess.
			item.Status, item.Note = StatusNotEstimated, "The provider priced only one side of this change."
		default:
			unpriced := p.unpriced + n.unpriced
			hasKnown := p.known != nil || n.known != nil
			item.UnpricedComponents = unpriced
			switch {
			case !hasKnown && unpriced > 0:
				item.Status = StatusUsageUnknown
			case unpriced > 0:
				item.Status = StatusPartial
			default:
				item.Status = StatusEstimated
			}
			if unpriced > 0 {
				usageUnpriced++
			}
			if hasKnown {
				pv, nv := zero(), zero()
				if hasPast {
					pv = orZero(p.known)
					item.PastMonthly = moneyPtr(pv)
				}
				if hasNow {
					nv = orZero(n.known)
					item.Monthly = moneyPtr(nv)
				}
				d := new(big.Rat).Sub(nv, pv)
				item.delta, item.Delta = d, moneyPtr(d)
				delta.Add(delta, d)
				switch c.Action {
				case plan.Create:
					added.Add(added, nv)
				case plan.Delete:
					removed.Add(removed, pv)
				default:
					changed.Add(changed, d)
				}
			}
			if hasNow {
				item.Components = n.components
			} else {
				item.Components = p.components
			}
		}
		switch item.Status {
		case StatusEstimated:
			out.Coverage.Estimated++
		case StatusPartial:
			out.Coverage.Partial++
		case StatusUsageUnknown:
			out.Coverage.UsageUnknown++
		case StatusUnsupported:
			out.Coverage.Unsupported++
		case StatusFree:
			out.Coverage.Free++
		default:
			out.Coverage.NotEstimated++
		}
		t := types[c.Type]
		if t == nil {
			t = &TypeSummary{ResourceType: c.Type}
			types[c.Type] = t
		}
		t.Count++
		if item.Delta != nil {
			t.Estimated++
			typeDelta[c.Type] = new(big.Rat).Add(orZero(typeDelta[c.Type]), item.delta)
		} else if item.Status != StatusFree {
			t.NotEstimated++
		}
		out.Items = append(out.Items, item)
	}
	out.Coverage.CostBearing = out.Coverage.Changed - out.Coverage.Free
	if out.Coverage.CostBearing > 0 {
		pct := (out.Coverage.Estimated + out.Coverage.Partial) * 100 / out.Coverage.CostBearing
		out.Coverage.Percent = &pct
	}
	out.MonthlyDelta, out.AddedMonthly, out.RemovedMonthly, out.ChangedDelta = Money(delta), Money(added), Money(removed), Money(changed)
	out.PastMonthly, out.ProjectedMonthly = Money(pastTotal), Money(nowTotal)

	for name, t := range types {
		t.Delta = moneyPtr(typeDelta[name])
		out.Types = append(out.Types, *t)
	}
	sort.Slice(out.Types, func(i, j int) bool { return out.Types[i].ResourceType < out.Types[j].ResourceType })
	sort.SliceStable(out.Items, func(i, j int) bool {
		a, b := out.Items[i].delta, out.Items[j].delta
		switch {
		case a != nil && b != nil:
			if c := new(big.Rat).Abs(a).Cmp(new(big.Rat).Abs(b)); c != 0 {
				return c > 0
			}
		case a != nil:
			return true
		case b != nil:
			return false
		}
		return out.Items[i].Address < out.Items[j].Address
	})
	if len(out.Items) > maxItems {
		out.Items, out.Truncated = out.Items[:maxItems], true
	}
	out.Caveats = caveats(out, raw.Currency, usageUnpriced, forgotten, unattributed)
	return out, nil
}

func caveats(b Breakdown, currency string, usage, forgotten, unattributed int) []Caveat {
	c := []Caveat{{Code: "not_a_bill", Message: "Estimates use public list prices and a 730-hour month. They exclude discounts, savings plans, reserved capacity, credits, free tiers, taxes and support plans, and are not your cloud bill."}}
	if n := b.Coverage.Unsupported + b.Coverage.NotEstimated; n > 0 {
		c = append(c, Caveat{Code: "not_estimated", Message: "Some changed resources have no estimate (unsupported resource types or missing prices). They are not counted as zero cost.", Count: n})
	}
	if usage > 0 {
		c = append(c, Caveat{Code: "usage_based", Message: "Usage-based charges (requests, data transfer, storage growth and similar) are not priced: no usage file is used. Affected totals cover fixed charges only.", Count: usage})
	}
	if b.Coverage.Free > 0 {
		c = append(c, Caveat{Code: "free", Message: "Some changed resources have no direct price according to the provider (for example IAM objects). Related usage can still cost money.", Count: b.Coverage.Free})
	}
	if forgotten > 0 {
		c = append(c, Caveat{Code: "forget", Message: "Forgotten resources are removed from state only; they keep running and billing.", Count: forgotten})
	}
	if unattributed > 0 {
		c = append(c, Caveat{Code: "unattributed", Message: "The provider priced items that do not match an address of this plan. They are left out of every amount shown here.", Count: unattributed})
	}
	if currency != "USD" {
		c = append(c, Caveat{Code: "exchange_rate", Message: "Prices are converted from USD to " + currency + " by the provider with its own exchange rate; amounts are approximate."})
	}
	return c
}
