// Package imports prepares declarative import blocks: it validates the target address and
// import ID, checks the project configuration and state for collisions, renders the block
// and writes it to a new file. It never runs an engine command; the operation manager plans
// the import like any other plan.
package imports

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Address is a parsed managed resource instance address, such as
// module.network["eu"].aws_subnet.private[0]. Only the subset needed for import targets is
// accepted: module calls with optional keys, then one managed resource with an optional key.
type Address struct {
	Modules []Step
	Type    string
	Name    string
	Key     *Key
}

// Step is one module call in an address.
type Step struct {
	Name string
	Key  *Key
}

// Key is a count index (Number) or a for_each key (Text).
type Key struct {
	Number *int
	Text   *string
}

const maxAddress = 1024

var (
	identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*`)
	typeName   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

var ErrAddress = errors.New("invalid resource instance address")

// ParseAddress accepts a canonical resource instance address. String keys may not contain
// quotes, backslashes, control characters or template sequences, so the address can be
// written into HCL verbatim and needs no escaping.
func ParseAddress(text string) (Address, error) {
	var a Address
	if text == "" || len(text) > maxAddress || strings.HasPrefix(text, "data.") {
		return a, ErrAddress
	}
	rest := text
	for strings.HasPrefix(rest, "module.") {
		rest = rest[len("module."):]
		name, key, tail, err := segment(rest)
		if err != nil || len(a.Modules) == 32 {
			return a, ErrAddress
		}
		a.Modules = append(a.Modules, Step{Name: name, Key: key})
		if !strings.HasPrefix(tail, ".") {
			return a, ErrAddress
		}
		rest = tail[1:]
	}
	kind, tail, ok := strings.Cut(rest, ".")
	if !ok || !typeName.MatchString(kind) || kind == "data" || kind == "module" || kind == "resource" {
		return a, ErrAddress
	}
	name, key, tail, err := segment(tail)
	if err != nil || tail != "" {
		return a, ErrAddress
	}
	a.Type, a.Name, a.Key = kind, name, key
	return a, nil
}

// segment reads an identifier and an optional [key] from the start of s.
func segment(s string) (name string, key *Key, rest string, err error) {
	name = identifier.FindString(s)
	if name == "" {
		return "", nil, "", ErrAddress
	}
	rest = s[len(name):]
	if !strings.HasPrefix(rest, "[") {
		return name, nil, rest, nil
	}
	end := strings.IndexByte(rest, ']')
	if end < 0 {
		return "", nil, "", ErrAddress
	}
	inner := rest[1:end]
	rest = rest[end+1:]
	switch {
	case len(inner) >= 2 && inner[0] == '"' && inner[len(inner)-1] == '"':
		value := inner[1 : len(inner)-1]
		if !safeKey(value) {
			return "", nil, "", ErrAddress
		}
		return name, &Key{Text: &value}, rest, nil
	case inner != "" && len(inner) <= 9 && strings.Trim(inner, "0123456789") == "" && (inner == "0" || inner[0] != '0'):
		n, _ := strconv.Atoi(inner)
		return name, &Key{Number: &n}, rest, nil
	}
	return "", nil, "", ErrAddress
}

func safeKey(value string) bool {
	if value == "" || strings.Contains(value, "${") || strings.Contains(value, "%{") {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r == ']' {
			return false
		}
	}
	return true
}

func (k *Key) String() string {
	if k == nil {
		return ""
	}
	if k.Number != nil {
		return "[" + strconv.Itoa(*k.Number) + "]"
	}
	return `["` + *k.Text + `"]`
}

func (a Address) String() string {
	var b strings.Builder
	for _, m := range a.Modules {
		b.WriteString("module." + m.Name + m.Key.String() + ".")
	}
	b.WriteString(a.Type + "." + a.Name + a.Key.String())
	return b.String()
}

// Resource is the address without its instance key, as a resource block is declared.
func (a Address) Resource() string { return a.Type + "." + a.Name }

// ModuleKey is the module manifest key ("network.subnets") with instance keys removed.
func (a Address) ModuleKey() string {
	names := make([]string, len(a.Modules))
	for i, m := range a.Modules {
		names[i] = m.Name
	}
	return strings.Join(names, ".")
}

// InRoot reports whether the resource is declared in the root module.
func (a Address) InRoot() bool { return len(a.Modules) == 0 }
