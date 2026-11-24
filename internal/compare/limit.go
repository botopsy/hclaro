package compare

import (
	"slices"
	"strconv"
	"strings"

	"hclaro/internal/state"
)

// MaxAddresses bounds a comparison's address filter.
const MaxAddresses = 200

// ValidAddresses checks a filter's shape only: whether the left state holds them is known
// after its read, and Limit reports the ones it does not.
func ValidAddresses(addresses []string) bool {
	if len(addresses) > MaxAddresses {
		return false
	}
	seen := map[string]bool{}
	for _, a := range addresses {
		if a == "" || len(a) > 1024 || strings.HasPrefix(a, "-") || seen[a] || strings.ContainsFunc(a, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return false
		}
		seen[a] = true
	}
	return true
}

// Limit narrows both sides to the selected left addresses and their counterparts, matched by
// the same rules Compare uses. It returns a warning when selected addresses are not in the
// left state. Rules must come from Validate. No addresses means no filter.
func Limit(left, right state.Index, rules []Compiled, addresses []string) (state.Index, state.Index, []string) {
	if len(addresses) == 0 {
		return left, right, nil
	}
	want := map[string]bool{}
	for _, a := range addresses {
		want[a] = true
	}
	keepRight := map[string]bool{}
	var l []state.Resource
	for _, r := range left.Resources {
		if !want[r.Address] {
			continue
		}
		l = append(l, r)
		target := r.Address
		for _, c := range rules {
			if c.Kind != RuleMap {
				continue
			}
			if t, ok := mapAddress(r.Address, c.Left, c.Right); ok {
				target = t
				break
			}
		}
		keepRight[target] = true
	}
	var warnings []string
	if n := len(addresses) - len(l); n > 0 {
		warnings = []string{strconv.Itoa(n) + " selected address" + map[bool]string{true: "es are", false: " is"}[n > 1] + " not in the left state and " + map[bool]string{true: "are", false: "is"}[n > 1] + " not compared."}
	}
	var r []state.Resource
	for _, x := range right.Resources {
		if keepRight[x.Address] {
			r = append(r, x)
		}
	}
	return subset(left, l), subset(right, r), warnings
}

// subset rebuilds an index from some of src's resources.
func subset(src state.Index, rs []state.Resource) state.Index {
	idx := state.Empty()
	parents := map[string]string{}
	for _, m := range src.Modules {
		parents[m.Address] = m.Parent
	}
	counts := map[string]int{}
	for _, r := range rs {
		idx.Resources = append(idx.Resources, r)
		idx.ByID[r.ID] = r
		idx.Providers[r.Provider]++
		idx.Types[r.Type]++
		counts[r.Module]++
		if r.Mode == "managed" {
			idx.Managed++
		} else {
			idx.Data++
		}
	}
	for addr, n := range counts {
		idx.Modules = append(idx.Modules, state.Module{Address: addr, Parent: parents[addr], Count: n})
	}
	slices.SortFunc(idx.Modules, func(a, b state.Module) int { return strings.Compare(a.Address, b.Address) })
	return idx
}
