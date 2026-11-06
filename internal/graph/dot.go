// Package graph parses the DOT output of `terraform graph` / `tofu graph` into a redacted,
// bounded dependency model and answers reachability questions about it. It never runs a
// command; the operation manager does that explicitly.
package graph

import (
	"errors"
	"fmt"
	"strings"
)

// Input bounds. Real graphs of large projects are a few megabytes; anything beyond these is
// refused rather than parsed partially.
const (
	MaxInputBytes = 16 << 20
	MaxDOTNodes   = 20_000
	MaxDOTEdges   = 200_000
	maxIDLength   = 4096
	maxNesting    = 64
)

var (
	// ErrSyntax is returned for input that is not the DOT subset the engines produce.
	ErrSyntax = errors.New("graph output is not valid DOT")
	// ErrTooLarge is returned when the input exceeds the node, edge or byte bounds.
	ErrTooLarge = errors.New("graph output exceeds Hclaro's size limits")
)

// dotNode is one node statement or implicitly declared node. Only label and shape are kept;
// every other attribute is ignored.
type dotNode struct {
	ID      string
	Label   string
	Shape   string
	Cluster string
	cluster int
}

type dotEdge struct{ From, To string }

type dotGraph struct {
	Directed bool
	Nodes    []dotNode
	Edges    []dotEdge
	index    map[string]int
}

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokID
	tokPunct
)

type token struct {
	kind tokenKind
	text string
	// quoted IDs are never keywords.
	quoted bool
	line   int
}

type lexer struct {
	src  string
	pos  int
	line int
}

func (l *lexer) fail(format string, args ...any) error {
	return fmt.Errorf("%w: line %d: %s", ErrSyntax, l.line, fmt.Sprintf(format, args...))
}

func (l *lexer) skip() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\n':
			l.line++
			l.pos++
			// A line starting with '#' is a preprocessor line and is ignored.
			if l.pos < len(l.src) && l.src[l.pos] == '#' {
				for l.pos < len(l.src) && l.src[l.pos] != '\n' {
					l.pos++
				}
			}
		case c == ' ' || c == '\t' || c == '\r':
			l.pos++
		case strings.HasPrefix(l.src[l.pos:], "//"):
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
		case strings.HasPrefix(l.src[l.pos:], "/*"):
			end := strings.Index(l.src[l.pos+2:], "*/")
			if end < 0 {
				l.pos = len(l.src)
				return
			}
			l.line += strings.Count(l.src[l.pos:l.pos+2+end], "\n")
			l.pos += end + 4
		default:
			return
		}
	}
}

func idStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func idPart(c byte) bool { return idStart(c) || c >= '0' && c <= '9' }

func (l *lexer) next() (token, error) {
	l.skip()
	if l.pos >= len(l.src) {
		return token{kind: tokEOF, line: l.line}, nil
	}
	line := l.line
	c := l.src[l.pos]
	switch {
	case strings.HasPrefix(l.src[l.pos:], "->") || strings.HasPrefix(l.src[l.pos:], "--"):
		l.pos += 2
		return token{kind: tokPunct, text: l.src[l.pos-2 : l.pos], line: line}, nil
	case strings.ContainsRune("{}[];,=:", rune(c)):
		l.pos++
		return token{kind: tokPunct, text: string(c), line: line}, nil
	case c == '"':
		s, err := l.quoted()
		if err != nil {
			return token{}, err
		}
		// "a" + "b" concatenates quoted strings.
		for {
			save, saveLine := l.pos, l.line
			l.skip()
			if l.pos < len(l.src) && l.src[l.pos] == '+' {
				l.pos++
				l.skip()
				if l.pos < len(l.src) && l.src[l.pos] == '"' {
					more, err := l.quoted()
					if err != nil {
						return token{}, err
					}
					s += more
					if len(s) > maxIDLength {
						return token{}, l.fail("identifier too long")
					}
					continue
				}
				return token{}, l.fail("'+' must join quoted strings")
			}
			l.pos, l.line = save, saveLine
			break
		}
		return token{kind: tokID, text: s, quoted: true, line: line}, nil
	case c == '<':
		depth, start := 0, l.pos
		for l.pos < len(l.src) {
			switch l.src[l.pos] {
			case '<':
				depth++
			case '>':
				depth--
			case '\n':
				l.line++
			}
			l.pos++
			if depth == 0 {
				if l.pos-start > maxIDLength {
					return token{}, l.fail("identifier too long")
				}
				return token{kind: tokID, text: l.src[start+1 : l.pos-1], quoted: true, line: line}, nil
			}
		}
		return token{}, l.fail("unterminated HTML string")
	case c == '-' || c == '.' || c >= '0' && c <= '9':
		start := l.pos
		if c == '-' {
			l.pos++
		}
		for l.pos < len(l.src) && (l.src[l.pos] == '.' || l.src[l.pos] >= '0' && l.src[l.pos] <= '9') {
			l.pos++
		}
		if !strings.ContainsAny(l.src[start:l.pos], "0123456789") {
			return token{}, l.fail("unexpected character %q", c)
		}
		return token{kind: tokID, text: l.src[start:l.pos], line: line}, nil
	case idStart(c):
		start := l.pos
		for l.pos < len(l.src) && idPart(l.src[l.pos]) {
			l.pos++
		}
		if l.pos-start > maxIDLength {
			return token{}, l.fail("identifier too long")
		}
		return token{kind: tokID, text: l.src[start:l.pos], line: line}, nil
	}
	return token{}, l.fail("unexpected character %q", c)
}

// quoted reads a double-quoted string. \" is DOT's escape; \\ is also unescaped because
// the engines write node IDs Go-quoted. A backslash before a newline continues the line.
// Other backslashes are kept as written.
func (l *lexer) quoted() (string, error) {
	var b strings.Builder
	l.pos++
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '"':
			l.pos++
			return b.String(), nil
		case c == '\\' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '"':
			b.WriteByte('"')
			l.pos += 2
		case c == '\\' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '\\':
			b.WriteByte('\\')
			l.pos += 2
		case c == '\\' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '\n':
			l.pos += 2
			l.line++
		case c == '\\' && l.pos+2 < len(l.src) && l.src[l.pos+1] == '\r' && l.src[l.pos+2] == '\n':
			l.pos += 3
			l.line++
		default:
			if c == '\n' {
				l.line++
			}
			b.WriteByte(c)
			l.pos++
		}
		if b.Len() > maxIDLength {
			return "", l.fail("identifier too long")
		}
	}
	return "", l.fail("unterminated string")
}

type parser struct {
	lex   *lexer
	tok   token
	g     *dotGraph
	depth int
	// stack holds indices into labels of the enclosing cluster subgraphs; a cluster's label
	// is its label attribute, or its name without the "cluster" prefix.
	stack  []int
	labels []string
}

func (p *parser) advance() error {
	t, err := p.lex.next()
	if err != nil {
		return err
	}
	p.tok = t
	return nil
}

func (p *parser) keyword(word string) bool {
	return p.tok.kind == tokID && !p.tok.quoted && strings.EqualFold(p.tok.text, word)
}

func (p *parser) punct(s string) bool { return p.tok.kind == tokPunct && p.tok.text == s }

func (p *parser) expect(s string) error {
	if !p.punct(s) {
		return p.lex.fail("expected %q", s)
	}
	return p.advance()
}

// parseDOT parses one graph. Only the first graph in the input is read; anything after it
// must be whitespace or comments.
func parseDOT(src []byte) (*dotGraph, error) {
	if len(src) > MaxInputBytes {
		return nil, ErrTooLarge
	}
	// The leading newline lets a '#' preprocessor line on line 1 be skipped like any other.
	p := &parser{lex: &lexer{src: "\n" + string(src), line: 0}, g: &dotGraph{index: map[string]int{}}}
	if err := p.advance(); err != nil {
		return nil, err
	}
	if p.keyword("strict") {
		if err := p.advance(); err != nil {
			return nil, err
		}
	}
	switch {
	case p.keyword("digraph"):
		p.g.Directed = true
	case p.keyword("graph"):
	default:
		return nil, p.lex.fail("expected digraph")
	}
	if err := p.advance(); err != nil {
		return nil, err
	}
	if p.tok.kind == tokID {
		if err := p.advance(); err != nil {
			return nil, err
		}
	}
	if err := p.expect("{"); err != nil {
		return nil, err
	}
	if _, err := p.statements(); err != nil {
		return nil, err
	}
	if err := p.expect("}"); err != nil {
		return nil, err
	}
	if p.tok.kind != tokEOF {
		return nil, p.lex.fail("unexpected content after the graph")
	}
	for i := range p.g.Nodes {
		if c := p.g.Nodes[i].cluster; c > 0 {
			p.g.Nodes[i].Cluster = p.labels[c-1]
		}
	}
	return p.g, nil
}

// statements parses a statement list up to (not including) the closing brace and returns
// the IDs of nodes mentioned in it, for edges whose operand is a subgraph.
func (p *parser) statements() ([]string, error) {
	var members []string
	for !p.punct("}") {
		if p.tok.kind == tokEOF {
			return nil, p.lex.fail("unexpected end of input")
		}
		ids, err := p.statement()
		if err != nil {
			return nil, err
		}
		members = append(members, ids...)
		if p.punct(";") || p.punct(",") {
			if err := p.advance(); err != nil {
				return nil, err
			}
		}
	}
	return members, nil
}

func (p *parser) statement() ([]string, error) {
	switch {
	case p.keyword("graph") || p.keyword("node") || p.keyword("edge"):
		kind := strings.ToLower(p.tok.text)
		if err := p.advance(); err != nil {
			return nil, err
		}
		attrs, err := p.attributes()
		if err != nil {
			return nil, err
		}
		if kind == "graph" {
			p.labelCluster(attrs)
		}
		return nil, nil
	case p.keyword("subgraph") || p.punct("{"):
		ids, err := p.subgraph()
		if err != nil {
			return nil, err
		}
		return p.edgeTail(ids)
	case p.tok.kind == tokID:
		id := p.tok.text
		if err := p.advance(); err != nil {
			return nil, err
		}
		if p.punct("=") {
			if err := p.advance(); err != nil {
				return nil, err
			}
			if p.tok.kind != tokID {
				return nil, p.lex.fail("expected a value after '='")
			}
			value := p.tok.text
			if err := p.advance(); err != nil {
				return nil, err
			}
			if strings.EqualFold(id, "label") {
				p.labelCluster(map[string]string{"label": value})
			}
			return nil, nil
		}
		if err := p.port(); err != nil {
			return nil, err
		}
		if p.punct("->") || p.punct("--") {
			return p.edgeTail([]string{id})
		}
		attrs, err := p.attributes()
		if err != nil {
			return nil, err
		}
		if err := p.declare(id, attrs); err != nil {
			return nil, err
		}
		return []string{id}, nil
	}
	return nil, p.lex.fail("unexpected %q", p.tok.text)
}

// port skips an optional :port[:compass] suffix of a node ID.
func (p *parser) port() error {
	for i := 0; i < 2 && p.punct(":"); i++ {
		if err := p.advance(); err != nil {
			return err
		}
		if p.tok.kind != tokID {
			return p.lex.fail("expected a port name")
		}
		if err := p.advance(); err != nil {
			return err
		}
	}
	return nil
}

// labelCluster records the label of the innermost cluster subgraph.
func (p *parser) labelCluster(attrs map[string]string) {
	if label, ok := attrs["label"]; ok && len(p.stack) > 0 {
		p.labels[p.stack[len(p.stack)-1]] = label
	}
}

func (p *parser) subgraph() ([]string, error) {
	p.depth++
	if p.depth > maxNesting {
		return nil, p.lex.fail("subgraphs nested too deeply")
	}
	defer func() { p.depth-- }()
	name := ""
	if p.keyword("subgraph") {
		if err := p.advance(); err != nil {
			return nil, err
		}
		if p.tok.kind == tokID {
			name = p.tok.text
			if err := p.advance(); err != nil {
				return nil, err
			}
		}
	}
	if err := p.expect("{"); err != nil {
		return nil, err
	}
	cluster := strings.HasPrefix(name, "cluster")
	if cluster {
		p.labels = append(p.labels, strings.TrimPrefix(strings.TrimPrefix(name, "cluster"), "_"))
		p.stack = append(p.stack, len(p.labels)-1)
	}
	ids, err := p.statements()
	if cluster {
		p.stack = p.stack[:len(p.stack)-1]
	}
	if err != nil {
		return nil, err
	}
	if err := p.expect("}"); err != nil {
		return nil, err
	}
	return ids, nil
}

// edgeTail parses "-> operand" repetitions after the first operand, adds every edge and
// returns the IDs of every operand.
func (p *parser) edgeTail(from []string) ([]string, error) {
	operands := [][]string{from}
	for p.punct("->") || p.punct("--") {
		if err := p.advance(); err != nil {
			return nil, err
		}
		switch {
		case p.keyword("subgraph") || p.punct("{"):
			ids, err := p.subgraph()
			if err != nil {
				return nil, err
			}
			operands = append(operands, ids)
		case p.tok.kind == tokID:
			id := p.tok.text
			if err := p.advance(); err != nil {
				return nil, err
			}
			if err := p.port(); err != nil {
				return nil, err
			}
			operands = append(operands, []string{id})
		default:
			return nil, p.lex.fail("expected an edge target")
		}
	}
	if len(operands) == 1 {
		return from, nil
	}
	if _, err := p.attributes(); err != nil {
		return nil, err
	}
	var all []string
	for _, ids := range operands {
		for _, id := range ids {
			if err := p.declare(id, nil); err != nil {
				return nil, err
			}
		}
	}
	for i := 1; i < len(operands); i++ {
		for _, a := range operands[i-1] {
			for _, b := range operands[i] {
				if len(p.g.Edges) >= MaxDOTEdges {
					return nil, ErrTooLarge
				}
				p.g.Edges = append(p.g.Edges, dotEdge{a, b})
				if !p.g.Directed {
					p.g.Edges = append(p.g.Edges, dotEdge{b, a})
				}
			}
		}
	}
	return all, nil
}

// attributes parses zero or more [k=v, ...] lists.
func (p *parser) attributes() (map[string]string, error) {
	attrs := map[string]string{}
	for p.punct("[") {
		if err := p.advance(); err != nil {
			return nil, err
		}
		for !p.punct("]") {
			if p.tok.kind != tokID {
				return nil, p.lex.fail("expected an attribute name")
			}
			key := strings.ToLower(p.tok.text)
			if err := p.advance(); err != nil {
				return nil, err
			}
			value := "true"
			if p.punct("=") {
				if err := p.advance(); err != nil {
					return nil, err
				}
				if p.tok.kind != tokID {
					return nil, p.lex.fail("expected an attribute value")
				}
				value = p.tok.text
				if err := p.advance(); err != nil {
					return nil, err
				}
			}
			attrs[key] = value
			if p.punct(",") || p.punct(";") {
				if err := p.advance(); err != nil {
					return nil, err
				}
			}
		}
		if err := p.advance(); err != nil {
			return nil, err
		}
	}
	return attrs, nil
}

func (p *parser) declare(id string, attrs map[string]string) error {
	i, ok := p.g.index[id]
	if !ok {
		if len(p.g.Nodes) >= MaxDOTNodes {
			return ErrTooLarge
		}
		cluster := 0
		if len(p.stack) > 0 {
			cluster = p.stack[len(p.stack)-1] + 1
		}
		p.g.index[id] = len(p.g.Nodes)
		p.g.Nodes = append(p.g.Nodes, dotNode{ID: id, cluster: cluster})
		i = len(p.g.Nodes) - 1
	}
	if v, ok := attrs["label"]; ok {
		p.g.Nodes[i].Label = v
	}
	if v, ok := attrs["shape"]; ok {
		p.g.Nodes[i].Shape = v
	}
	return nil
}
