package api

// The UI's wire types in web/src/types/ are written by hand; this test keeps them in step with
// the Go structs the server encodes. For every mapped TS type it checks, recursively through
// nested objects, that both sides name the same JSON fields and that a field the UI treats as
// always present is never omitted by Go (omitempty/omitzero).

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// unchecked lists TS object types no Go type stands for, with the reason. Prefer naming the
// Go type the handler encodes and mapping it in wireTypes.
var unchecked = map[string]string{
	"ReplaceTarget": "UI selection state; a replace plan sends addresses only",
}

// goOnly lists Go fields, as "TSType.field" (or the path of an inline object), that the
// server may send and the UI deliberately leaves out.
var goOnly = map[string]string{
	"Comparison.result":    "comparisonBody clears it and flattens the result",
	"HistoryEntry.session": "marks the process that wrote a running entry",
}

// tsOnly lists TS fields the UI fills itself, which no mapped Go type sends.
var tsOnly = map[string]string{
	"PlanOptions.code":               "PlansView fromScope copies it from ScopedPlanOptions",
	"PlanOptions.scope":              "PlansView fromScope copies it from ScopedPlanOptions",
	"PlanOptions.executionContext":   "PlansView fromScope copies it from ScopedPlanOptions",
	"PlanOptions.contextFingerprint": "PlansView fromScope copies it from ScopedPlanOptions",
}

func TestWireTypesMatchGo(t *testing.T) {
	decls := readTS(t)
	checked := map[string]bool{}
	for name, value := range wireTypes {
		expr, ok := decls[name]
		if !ok {
			t.Errorf("%s: mapped but not declared in web/src/types", name)
			continue
		}
		checked[name] = true
		c := &comparer{decls: decls, seen: map[string]bool{}, checked: checked}
		var types []reflect.Type
		if variants, ok := value.([]any); ok {
			for _, v := range variants {
				types = append(types, reflect.TypeOf(v))
			}
		} else {
			types = append(types, reflect.TypeOf(value))
		}
		c.object(name, expr, types...)
		for _, e := range c.errs {
			t.Error(e)
		}
	}
	// Every TS object type is checked, directly or as a field of a checked type, or is listed.
	var missing []string
	for name, expr := range decls {
		if _, ok := (&comparer{decls: decls}).fields(expr); ok && !checked[name] && unchecked[name] == "" {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("%s: no Go type checks it; add it to wireTypes, or to unchecked with the handler that builds it", name)
	}
	for name := range unchecked {
		if checked[name] {
			t.Errorf("%s: listed as unchecked but checked through %s", name, "wireTypes")
		}
		if _, ok := decls[name]; !ok {
			t.Errorf("%s: listed as unchecked but not declared", name)
		}
	}
}

var (
	lineComment  = regexp.MustCompile(`(?m)//.*$`)
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	declStart    = regexp.MustCompile(`export type (\w+) = `)
	identifier   = regexp.MustCompile(`^\w+$`)
	pick         = regexp.MustCompile(`^Pick<(\w+),\s*(.+)>$`)
)

// readTS returns every exported type alias in web/src/types as name → type expression.
func readTS(t *testing.T) map[string]string {
	files, err := filepath.Glob("../../web/src/types/*.ts")
	if err != nil || len(files) == 0 {
		t.Fatalf("no TS wire types: %v", err)
	}
	decls := map[string]string{}
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		// Wire types hold no string literal with "//" or "/*", so comments strip safely.
		src := lineComment.ReplaceAllString(blockComment.ReplaceAllString(string(b), ""), "")
		for _, m := range declStart.FindAllStringSubmatchIndex(src, -1) {
			rest := src[m[1]:]
			end := topLevel(rest, ';')
			if len(end) < 2 {
				t.Fatalf("%s: unterminated type %s", file, src[m[2]:m[3]])
			}
			decls[src[m[2]:m[3]]] = strings.TrimSpace(end[0])
		}
	}
	return decls
}

// topLevel splits s at sep outside brackets, braces, parentheses and angle brackets.
func topLevel(s string, sep byte) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{', '[', '(', '<':
			depth++
		case '}', ']', ')', '>':
			depth--
		case sep:
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

type tsField struct {
	optional bool
	typ      string
}

type comparer struct {
	decls   map[string]string
	seen    map[string]bool
	checked map[string]bool
	errs    []string
}

// fields returns the members of an object type expression: an object literal, a named object
// type, an intersection of those, or one of those unioned with null/undefined.
func (c *comparer) fields(expr string) (map[string]tsField, bool) {
	expr = nonNull(expr)
	if parts := topLevel(expr, '|'); len(parts) > 1 {
		return nil, false
	}
	out := map[string]tsField{}
	for _, part := range topLevel(expr, '&') {
		part = strings.TrimSpace(part)
		var members map[string]tsField
		switch {
		case strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}"):
			members = map[string]tsField{}
			for _, m := range topLevel(part[1:len(part)-1], ';') {
				for _, m := range topLevel(m, ',') {
					if m = strings.TrimSpace(m); m == "" {
						continue
					}
					kv := topLevel(m, ':')
					name := strings.TrimSpace(kv[0])
					members[strings.TrimSuffix(name, "?")] = tsField{optional: strings.HasSuffix(name, "?"), typ: strings.TrimSpace(strings.Join(kv[1:], ":"))}
				}
			}
		case pick.MatchString(part):
			m := pick.FindStringSubmatch(part)
			all, ok := c.fields(m[1])
			if !ok {
				return nil, false
			}
			members = map[string]tsField{}
			for _, key := range topLevel(m[2], '|') {
				key = strings.Trim(strings.TrimSpace(key), "'")
				members[key] = all[key]
			}
		case identifier.MatchString(part) && c.decls[part] != "":
			var ok bool
			if members, ok = c.fields(c.decls[part]); !ok {
				return nil, false
			}
		default:
			return nil, false
		}
		for k, v := range members {
			out[k] = v
		}
	}
	return out, true
}

func nonNull(expr string) string {
	var keep []string
	for _, p := range topLevel(expr, '|') {
		if p = strings.TrimSpace(p); p != "null" && p != "undefined" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, " | ")
}

// element strips arrays and records from a field type, leaving what each element holds.
func element(typ string) string {
	typ = nonNull(typ)
	for {
		switch {
		case strings.HasSuffix(typ, "[]"):
			typ = strings.TrimSuffix(typ, "[]")
		case strings.HasPrefix(typ, "(") && strings.HasSuffix(typ, ")"):
			typ = nonNull(typ[1 : len(typ)-1])
		case strings.HasPrefix(typ, "Record<string,") && strings.HasSuffix(typ, ">"):
			typ = strings.TrimSpace(typ[len("Record<string,") : len(typ)-1])
		default:
			return typ
		}
	}
}

func goElement(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	return t
}

type goField struct {
	optional bool
	typ      reflect.Type
}

func goFields(t reflect.Type, out map[string]goField) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" || (!f.IsExported() && !f.Anonymous) {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" && goElement(f.Type).Kind() == reflect.Struct {
			goFields(goElement(f.Type), out)
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = goField{optional: strings.Contains(","+opts+",", ",omitempty,") || strings.Contains(","+opts+",", ",omitzero,"), typ: f.Type}
	}
}

// object compares the TS object expression at path with the Go types it is decoded from. A TS
// type may stand for several Go types (for example launch and Hub options): it then names
// the fields of all of them, and a field is required only if every Go type always sends it.
func (c *comparer) object(path, expr string, types ...reflect.Type) {
	var names []string
	for _, t := range types {
		names = append(names, t.String())
	}
	goNames := strings.Join(names, ", ")
	if identifier.MatchString(expr) {
		if c.seen[expr+"="+goNames] {
			return
		}
		c.seen[expr+"="+goNames] = true
	}
	ts, ok := c.fields(expr)
	if !ok {
		c.errs = append(c.errs, path+": "+expr+" is not an object type")
		return
	}
	// Named object types, alone or inside an intersection, are checked too.
	for _, part := range topLevel(nonNull(expr), '&') {
		if part = strings.TrimSpace(part); identifier.MatchString(part) {
			c.checked[part] = true
		}
	}
	g := map[string]goField{}
	for i, t := range types {
		if t = goElement(t); t.Kind() != reflect.Struct {
			c.errs = append(c.errs, path+": Go "+t.String()+" is not a struct")
			return
		}
		one := map[string]goField{}
		goFields(goElement(t), one)
		for name, f := range one {
			if prev, ok := g[name]; ok {
				f.optional = f.optional || prev.optional
				f.typ = prev.typ
			} else if i > 0 {
				f.optional = true
			}
			g[name] = f
		}
		for name, f := range g {
			if _, ok := one[name]; !ok {
				f.optional = true
				g[name] = f
			}
		}
	}
	owner := path
	if identifier.MatchString(expr) {
		owner = expr
	}
	for _, name := range sortedKeys(ts) {
		f := ts[name]
		gf, ok := g[name]
		if !ok && tsOnly[owner+"."+name] != "" {
			continue
		}
		if !ok {
			c.errs = append(c.errs, path+"."+name+": not sent by Go "+goNames)
			continue
		}
		if !f.optional && gf.optional {
			c.errs = append(c.errs, path+"."+name+": required in TS but omitted by Go "+goNames+" (omitempty or not in every variant)")
		}
		el := element(f.typ)
		if _, isObject := c.fields(el); isObject && goElement(gf.typ).Kind() == reflect.Struct {
			c.object(path+"."+name, el, gf.typ)
		}
	}
	for _, name := range sortedKeys(g) {
		if _, ok := ts[name]; !ok && goOnly[owner+"."+name] == "" {
			c.errs = append(c.errs, path+"."+name+": sent by Go "+goNames+" but missing in TS")
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// A refusal sent without a reason shows the UI's message for its code; without one the UI
// would claim the server is unreachable. Every UI message names a code Go still sends.
func TestEveryErrorCodeHasAUIMessage(t *testing.T) {
	src, err := os.ReadFile("../../web/src/api.ts")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)const messages: Record<string, string> = \{(.*?)\n\};`).FindSubmatch(src)
	if block == nil {
		t.Fatal("messages map not found in web/src/api.ts")
	}
	messages := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s+([a-z_]+):`).FindAllSubmatch(block[1], -1) {
		messages[string(m[1])] = true
	}
	var goSource strings.Builder
	files, _ := filepath.Glob("../*/*.go")
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		goSource.Write(b)
		if !strings.HasPrefix(file, "../api/") {
			continue
		}
		for _, m := range regexp.MustCompile(`errorJSON\(w, [^,]+, "([a-z_]+)"\)`).FindAllSubmatch(b, -1) {
			if !messages[string(m[1])] {
				t.Errorf("%s: errorJSON code %q has no message in web/src/api.ts", file, m[1])
			}
		}
	}
	for code := range messages {
		if !strings.Contains(goSource.String(), `"`+code+`"`) {
			t.Errorf("web/src/api.ts has a message for %q, which no Go code sends", code)
		}
	}
}
