package graph

import (
	"regexp"
	"strings"
)

// Node kinds. Resource and data nodes are configuration resources; every other kind is an
// engine-internal node that Hclaro never maps to state.
const (
	KindResource  = "resource"
	KindData      = "data"
	KindModule    = "module"
	KindProvider  = "provider"
	KindVariable  = "variable"
	KindLocal     = "local"
	KindOutput    = "output"
	KindCheck     = "check"
	KindEphemeral = "ephemeral"
	KindUnknown   = "unknown"
	// kindMeta nodes (the root, count boundaries, close and destroy nodes) are bookkeeping of
	// the engine's walk. They are dropped: keeping them would connect every resource.
	kindMeta = "meta"
)

type segment struct {
	name     string
	index    string
	hasIndex bool
}

var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// splitAddress splits an address like module.a["x"].aws_instance.b[0] into segments. Index
// brackets may contain quoted strings with escaped quotes. ok is false for anything else.
func splitAddress(s string) ([]segment, bool) {
	var out []segment
	i := 0
	for i < len(s) {
		start := i
		for i < len(s) && s[i] != '.' && s[i] != '[' {
			i++
		}
		seg := segment{name: s[start:i]}
		if seg.name == "" {
			return nil, false
		}
		if i < len(s) && s[i] == '[' {
			open := i
			quoted := false
			for i++; i < len(s); i++ {
				c := s[i]
				if quoted {
					if c == '\\' {
						i++
					} else if c == '"' {
						quoted = false
					}
					continue
				}
				if c == '"' {
					quoted = true
				} else if c == ']' {
					break
				}
			}
			if i >= len(s) {
				return nil, false
			}
			seg.index, seg.hasIndex = s[open:i+1], true
			i++
		}
		out = append(out, seg)
		if i < len(s) {
			if s[i] != '.' || i == len(s)-1 {
				return nil, false
			}
			i++
		}
	}
	return out, len(out) > 0
}

func (s segment) String() string { return s.name + s.index }

func join(segs []segment) string {
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.String()
	}
	return strings.Join(parts, ".")
}

// parsed is a classified node address.
type parsed struct {
	kind string
	// module is the module path ("module.a.module.b"), empty for the root module.
	module string
	// address is the full normalized address; resource is set for resource and data nodes.
	address, resource string
	typ, name         string
	instanced         bool
}

// classify reads a normalized node name (no "[root] " prefix or suffix).
func classify(name string) parsed {
	if name == "root" || strings.HasPrefix(name, "meta.") {
		return parsed{kind: kindMeta, address: name}
	}
	segs, ok := splitAddress(name)
	if !ok {
		return parsed{kind: KindUnknown, address: name}
	}
	var modules []segment
	i := 0
	for i+1 < len(segs) && segs[i].name == "module" && !segs[i].hasIndex && namePattern.MatchString(segs[i+1].name) {
		modules = append(modules, segs[i], segs[i+1])
		i += 2
	}
	p := parsed{module: join(modules), address: name}
	rest := segs[i:]
	// A key on any segment, module instances included, makes the node one instance.
	instanced := false
	for _, s := range segs {
		instanced = instanced || s.hasIndex
	}
	valid := func(ss ...segment) bool {
		for _, s := range ss {
			if !namePattern.MatchString(s.name) {
				return false
			}
		}
		return true
	}
	switch {
	case len(rest) == 0:
		p.kind = KindModule
	case rest[0].name == "provider" && rest[0].hasIndex && (len(rest) == 1 || len(rest) == 2 && !rest[1].hasIndex):
		p.kind = KindProvider
	case len(rest) == 2 && rest[0].name == "provider" && !rest[0].hasIndex && !rest[1].hasIndex:
		// Legacy provider.<name> notation.
		p.kind = KindProvider
	case len(rest) == 2 && !rest[0].hasIndex && !rest[1].hasIndex && valid(rest...) && (rest[0].name == "var" || rest[0].name == "local" || rest[0].name == "output" || rest[0].name == "check"):
		p.kind = map[string]string{"var": KindVariable, "local": KindLocal, "output": KindOutput, "check": KindCheck}[rest[0].name]
	case len(rest) == 3 && (rest[0].name == "data" || rest[0].name == "ephemeral") && !rest[0].hasIndex && !rest[1].hasIndex && valid(rest...):
		p.kind, p.typ, p.name = KindData, rest[1].name, rest[2].name
		if rest[0].name == "ephemeral" {
			// Ephemeral resources are never recorded in state, so they are never mapped.
			p.kind = KindEphemeral
			break
		}
		p.resource, p.instanced = name, instanced
	case len(rest) == 2 && !rest[0].hasIndex && valid(rest...):
		p.kind, p.typ, p.name = KindResource, rest[0].name, rest[1].name
		p.resource, p.instanced = name, instanced
	default:
		p.kind = KindUnknown
	}
	return p
}

// ConfigAddress removes every instance key from a resource instance address:
// module.a["x"].aws_instance.b[0] becomes module.a.aws_instance.b. ok is false when the
// address cannot be parsed, in which case it must not be mapped to anything.
func ConfigAddress(address string) (string, bool) {
	segs, ok := splitAddress(address)
	if !ok {
		return "", false
	}
	for i := range segs {
		segs[i].index, segs[i].hasIndex = "", false
	}
	return join(segs), true
}

// suffix handling for full-format node names such as "[root] aws_instance.a (expand)".
var suffixPattern = regexp.MustCompile(`\s+\(([^()]*)\)$`)

// normalize strips the "[root] " prefix and the walk-phase suffix. drop is set for nodes that
// only exist for the engine's walk; odd is set for an unrecognized suffix (the node is kept
// as written and classified as unknown).
func normalize(id string) (name string, drop, odd bool) {
	name = strings.TrimPrefix(id, "[root] ")
	for {
		m := suffixPattern.FindStringSubmatchIndex(name)
		if m == nil {
			return name, false, false
		}
		suffix := name[m[2]:m[3]]
		switch {
		// orphan: a resource recorded in state but no longer configured (Terraform 1.5, OpenTofu).
		// "expand, reference" and "expand, input": OpenTofu's two nodes for one module variable.
		case suffix == "expand" || suffix == "prepare state" || suffix == "orphan" || suffix == "expand, reference" || suffix == "expand, input":
			name = name[:m[0]]
		case suffix == "close" || suffix == "EachMode fixup" || strings.HasPrefix(suffix, "destroy") || strings.HasPrefix(suffix, "deposed") || suffix == "cleanup":
			return name[:m[0]], true, false
		default:
			return id, false, true
		}
	}
}
