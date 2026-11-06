// Package compare matches the resource instances of two redacted state snapshots and
// classifies their differences. It never runs an engine, never sees raw state and never
// compares values that the state redactor withheld.
package compare

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"hclaro/internal/state"
)

// Rule kinds. Rules belong to one comparison request; nothing is persisted.
const (
	// RuleMap pairs left addresses under Left with right addresses under Right, for a renamed
	// module, resource or instance. Without a map rule only identical addresses match.
	RuleMap = "map"
	// RuleIgnore suppresses attribute paths matching Path (optionally for one ResourceType).
	RuleIgnore = "ignore"
	// RuleSubstitute treats the token Left in left string values as Right before comparing,
	// for environment names in values such as "dev-api" and "prod-api".
	RuleSubstitute = "substitute"
)

// MaxRules bounds the rules of one comparison.
const MaxRules = 50

// Rule is one explicit, reviewable matching or ignore rule.
type Rule struct {
	Kind         string `json:"kind"`
	Left         string `json:"left,omitempty"`
	Right        string `json:"right,omitempty"`
	Path         string `json:"path,omitempty"`
	ResourceType string `json:"resourceType,omitempty"`
}

// RuleError names the invalid rule and why.
type RuleError struct {
	Index  int
	Reason string
}

func (e *RuleError) Error() string { return fmt.Sprintf("rule %d: %s", e.Index+1, e.Reason) }

var typePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,127}$`)

// Compiled is a validated rule with parsed address prefixes and path patterns.
type Compiled struct {
	Rule
	pattern []patternSegment
}

// Validate checks every rule and returns the Compiled form. Rules never hold secret-looking
// text: a token or path that looks like a credential is refused, so a definition cannot be
// used to carry or probe a secret.
func Validate(rules []Rule) ([]Compiled, error) {
	if len(rules) > MaxRules {
		return nil, &RuleError{MaxRules, fmt.Sprintf("Use at most %d rules.", MaxRules)}
	}
	out := make([]Compiled, 0, len(rules))
	for i, r := range rules {
		for _, s := range []string{r.Left, r.Right, r.Path, r.ResourceType} {
			if len(s) > 256 || !printable(s) {
				return nil, &RuleError{i, "Use at most 256 printable characters."}
			}
			if s != "" && state.LooksSecret(s) {
				return nil, &RuleError{i, "This value looks like a credential. Rules may not contain secrets."}
			}
		}
		if r.ResourceType != "" && !typePattern.MatchString(r.ResourceType) {
			return nil, &RuleError{i, "Use a resource type such as aws_instance."}
		}
		c := Compiled{Rule: r}
		switch r.Kind {
		case RuleMap:
			if r.Path != "" || r.ResourceType != "" {
				return nil, &RuleError{i, "A mapping rule has only a left and a right address."}
			}
			if !validPrefix(r.Left) || !validPrefix(r.Right) {
				return nil, &RuleError{i, "Use module or resource addresses, for example module.app or aws_instance.web."}
			}
			if r.Left == r.Right {
				return nil, &RuleError{i, "The left and right addresses are the same; exact addresses already match."}
			}
		case RuleIgnore:
			if r.Left != "" || r.Right != "" {
				return nil, &RuleError{i, "An ignore rule has only an attribute path and an optional resource type."}
			}
			p, ok := parsePattern(r.Path)
			if !ok {
				return nil, &RuleError{i, `Use an attribute path such as region, tags.Environment, tags["cost-center"] or ingress[*].cidr_blocks.`}
			}
			c.pattern = p
		case RuleSubstitute:
			if r.Left == "" || r.Right == "" || r.Left == r.Right {
				return nil, &RuleError{i, "Enter two different values, for example dev and prod."}
			}
			if len(r.Left) > 128 || len(r.Right) > 128 {
				return nil, &RuleError{i, "Use at most 128 characters per value."}
			}
			if r.Path != "" {
				p, ok := parsePattern(r.Path)
				if !ok {
					return nil, &RuleError{i, `Use an attribute path such as name or tags.Name, or leave it empty for every attribute.`}
				}
				c.pattern = p
			}
		default:
			return nil, &RuleError{i, "Choose map, ignore or substitute."}
		}
		out = append(out, c)
	}
	return out, nil
}

func printable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// addressSegments splits module.a["x"].aws_instance.b[0] into name and index parts. Index
// brackets may hold numbers or quoted strings with escapes; dots in quotes never split.
func addressSegments(s string) (names, indexes []string, ok bool) {
	i := 0
	for i < len(s) {
		start := i
		for i < len(s) && s[i] != '.' && s[i] != '[' {
			i++
		}
		name := s[start:i]
		if !namePattern.MatchString(name) {
			return nil, nil, false
		}
		index := ""
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
				return nil, nil, false
			}
			index = s[open : i+1]
			if !validIndex(index) {
				return nil, nil, false
			}
			i++
		}
		names, indexes = append(names, name), append(indexes, index)
		if i < len(s) {
			if s[i] != '.' || i == len(s)-1 {
				return nil, nil, false
			}
			i++
		}
	}
	return names, indexes, len(names) > 0
}

func validIndex(index string) bool {
	inner := index[1 : len(index)-1]
	if _, err := strconv.Atoi(inner); err == nil {
		return true
	}
	var key string
	return json.Unmarshal([]byte(inner), &key) == nil
}

// validPrefix accepts a module path (module.a, module.a["k"].module.b) optionally followed by
// a resource (TYPE.NAME or data.TYPE.NAME, optionally with an instance key).
func validPrefix(s string) bool {
	if s == "" {
		return false
	}
	names, indexes, ok := addressSegments(s)
	if !ok {
		return false
	}
	i := 0
	for i < len(names) && names[i] == "module" {
		if i+1 >= len(names) || indexes[i] != "" {
			return false
		}
		i += 2
	}
	rest := names[i:]
	restIdx := indexes[i:]
	switch {
	case len(rest) == 0:
		return true
	case len(rest) == 3 && rest[0] == "data":
		return restIdx[0] == "" && restIdx[1] == ""
	case len(rest) == 2 && rest[0] != "data":
		return restIdx[0] == ""
	}
	return false
}

// mapAddress rewrites address when it starts with the rule's left prefix at a segment
// boundary (end, '.' or '[').
func mapAddress(address, left, right string) (string, bool) {
	if !strings.HasPrefix(address, left) {
		return "", false
	}
	rest := address[len(left):]
	if rest != "" && rest[0] != '.' && rest[0] != '[' {
		return "", false
	}
	// "aws_instance.web[0]" must not match the prefix "aws_instance.web[0" or an index
	// continuation of an already indexed prefix.
	if rest != "" && rest[0] == '[' && strings.HasSuffix(left, "]") {
		return "", false
	}
	return right + rest, true
}

// patternSegment is one segment of an attribute path pattern: a key, an index or "*".
type patternSegment struct {
	key   string
	index int
	any   bool
	isKey bool
}

// parsePattern reads a path in the rendered form used by attribute rows: identifiers
// separated by dots, [N] indexes, ["quoted"] keys and * (or [*]) for any one segment.
func parsePattern(s string) ([]patternSegment, bool) {
	var out []patternSegment
	i := 0
	for i < len(s) {
		switch {
		case s[i] == '[':
			end := strings.IndexByte(s[i:], ']')
			if end < 0 {
				return nil, false
			}
			inner := s[i+1 : i+end]
			if inner == "*" {
				out = append(out, patternSegment{any: true})
				i += end + 1
				break
			}
			if n, err := strconv.Atoi(inner); err == nil && n >= 0 {
				out = append(out, patternSegment{index: n})
				i += end + 1
				break
			}
			// A quoted key may contain ']'; decode the JSON string itself.
			dec := json.NewDecoder(strings.NewReader(s[i+1:]))
			var key string
			if dec.Decode(&key) != nil {
				return nil, false
			}
			close := i + 1 + int(dec.InputOffset())
			if close >= len(s) || s[close] != ']' {
				return nil, false
			}
			out = append(out, patternSegment{key: key, isKey: true})
			i = close + 1
		case s[i] == '.':
			if len(out) == 0 || i == len(s)-1 || s[i+1] == '.' || s[i+1] == '[' {
				return nil, false
			}
			i++
		default:
			if len(out) > 0 && s[i-1] != '.' {
				return nil, false
			}
			start := i
			for i < len(s) && s[i] != '.' && s[i] != '[' {
				i++
			}
			name := s[start:i]
			if name == "*" {
				out = append(out, patternSegment{any: true})
			} else if namePattern.MatchString(name) {
				out = append(out, patternSegment{key: name, isKey: true})
			} else {
				return nil, false
			}
		}
	}
	return out, len(out) > 0 && len(out) <= 64
}

// matches reports whether the pattern matches path or one of its ancestors, so ignoring
// "tags" ignores every tag.
func matches(pattern []patternSegment, path []any) bool {
	if len(pattern) > len(path) {
		return false
	}
	for i, p := range pattern {
		switch s := path[i].(type) {
		case string:
			if !p.any && (!p.isKey || p.key != s) {
				return false
			}
		case int:
			if !p.any && (p.isKey || p.index != s) {
				return false
			}
		}
	}
	return true
}
