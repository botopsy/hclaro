package discovery

import (
	"hclaro/internal/state"
	"hclaro/internal/workspace"
)

// Binding match statuses. A candidate is never called untracked with certainty: state of
// other workspaces and projects is not read.
const (
	// MatchTracked: exactly one managed instance of the same type in this workspace's
	// recorded state holds the same import ID or ARN, and nothing contradicts it.
	MatchTracked = "already_tracked"
	// MatchPossiblyUntracked: every managed instance of the type exposes a comparable
	// identifier and none equals this object's. Another workspace or project may manage it.
	MatchPossiblyUntracked = "possibly_untracked"
	// MatchUnresolved: the candidate has no verified import ID, so it cannot be compared or
	// imported.
	MatchUnresolved = "unresolved"
	// MatchUnknown: the comparison cannot be decided (ambiguous match, identifiers withheld
	// or missing in state, conflicting ARN, or no readable state).
	MatchUnknown = "unknown"
)

// TerraformBindingMatch is the exact-match result of one candidate against the launch
// workspace's recorded state.
type TerraformBindingMatch struct {
	Status    string   `json:"status"`
	Reason    string   `json:"reason"`
	Addresses []string `json:"addresses"`
	// ResourceIDs are the explorer IDs of matched instances (to open them in the explorer).
	ResourceIDs []string `json:"resourceIds"`
}

// stateBinding is one managed instance's comparable identifiers.
type stateBinding struct {
	address, resourceID string
	ids                 []string
	arn                 string
	comparable          bool
}

// bindingIndex groups managed instances of supported types by type.
type bindingIndex struct {
	ready   bool
	reason  string
	byType  map[string][]stateBinding
	missing map[string]int
}

func stringAttr(attrs map[string]any, key string) string {
	s, _ := attrs[key].(string)
	if s == state.Redacted {
		return ""
	}
	return s
}

func indexState(snap workspace.Snapshot) bindingIndex {
	idx := bindingIndex{byType: map[string][]stateBinding{}, missing: map[string]int{}}
	switch snap.Status {
	case "ready", "empty":
		idx.ready = true
	case "loading":
		idx.reason = "The workspace state is still being read."
		return idx
	default:
		idx.reason = "The workspace state could not be read, so no object can be compared with it."
		return idx
	}
	for _, r := range snap.Index.Resources {
		if r.Mode != "managed" {
			continue
		}
		attrs, _ := r.Attributes.(map[string]any)
		ids, ok := StateIDs(r.Type, attrs)
		if !ok {
			continue
		}
		b := stateBinding{address: r.Address, resourceID: r.ID, arn: stringAttr(attrs, "arn"), ids: ids}
		if b.arn != "" && !arnShape.MatchString(b.arn) {
			b.arn = ""
		}
		// An instance whose identifiers are withheld, missing or in another format cannot be
		// compared, so no object of its type can be called untracked.
		b.comparable = len(b.ids) > 0 || b.arn != ""
		if !b.comparable {
			idx.missing[r.Type]++
		}
		idx.byType[r.Type] = append(idx.byType[r.Type], b)
	}
	return idx
}

// Match labels every candidate against the snapshot. The comparison is exact: an identifier
// that differs only in format is not treated as equal.
func Match(candidates []CloudResourceCandidate, snap workspace.Snapshot) []TerraformBindingMatch {
	idx := indexState(snap)
	out := make([]TerraformBindingMatch, len(candidates))
	for i, c := range candidates {
		out[i] = idx.match(c)
	}
	return out
}

func (idx bindingIndex) match(c CloudResourceCandidate) TerraformBindingMatch {
	m := TerraformBindingMatch{Addresses: []string{}, ResourceIDs: []string{}}
	if c.ImportStatus != ImportIDVerified {
		m.Status, m.Reason = MatchUnresolved, c.ImportNote
		return m
	}
	if !idx.ready {
		m.Status, m.Reason = MatchUnknown, idx.reason
		return m
	}
	var hits []stateBinding
	conflict := false
	for _, b := range idx.byType[c.Type] {
		idHit := false
		for _, id := range b.ids {
			if id == c.ImportID {
				idHit = true
			}
		}
		arnHit := c.ARN != "" && b.arn == c.ARN
		if idHit || arnHit {
			hits = append(hits, b)
			// The same identifier with another recorded ARN (another account, region or
			// partition) is a contradiction, not a match.
			if b.arn != "" && c.ARN != "" && b.arn != c.ARN {
				conflict = true
			}
		}
	}
	for _, h := range hits {
		m.Addresses = append(m.Addresses, h.address)
		m.ResourceIDs = append(m.ResourceIDs, h.resourceID)
	}
	switch {
	case len(hits) > 1:
		m.Status, m.Reason = MatchUnknown, "More than one instance in state holds this identifier. The mapping is ambiguous; resolve it in state before importing anything."
	case len(hits) == 1 && conflict:
		m.Status, m.Reason = MatchUnknown, "An instance in state holds this identifier but records a different ARN (another account or region). It is not treated as a match."
	case len(hits) == 1:
		m.Status, m.Reason = MatchTracked, "Recorded in this workspace's state with the same identifier."
	case idx.missing[c.Type] > 0:
		m.Status, m.Reason = MatchUnknown, "Some "+c.Type+" instances in this workspace's state have no comparable identifier (withheld, missing or in another format), so this object may already be one of them."
	default:
		m.Status, m.Reason = MatchPossiblyUntracked, "Not in this workspace's recorded state. Another workspace, project or tool may still manage it."
	}
	return m
}
