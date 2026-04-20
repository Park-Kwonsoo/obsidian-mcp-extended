package search

import (
	"errors"
	"strings"
	"unicode"
)

// Node types in the parsed search expression. Kept as string constants so
// test output is readable and JSON diffs against JS golden files are direct.
const (
	NodeTerm  = "TERM"  // bare word like `obsidian`
	NodeField = "FIELD" // field:value like `title:setup`
	NodeAnd   = "AND"
	NodeOr    = "OR"
	NodeNot   = "NOT"
)

// Expr is one node of the parsed search-query tree.
//
// Layout choice: a single struct with the unused fields zeroed out beats an
// interface-per-variant here because (a) trees are tiny (≤ a few dozen nodes),
// (b) the consumers switch on Type anyway, and (c) a plain struct serializes
// to JSON cleanly which makes golden-file diffing trivial.
type Expr struct {
	Type    string `json:"type"`
	Value   string `json:"value,omitempty"`   // TERM and FIELD
	Field   string `json:"field,omitempty"`   // FIELD only
	Left    *Expr  `json:"left,omitempty"`    // AND, OR
	Right   *Expr  `json:"right,omitempty"`   // AND, OR
	Operand *Expr  `json:"operand,omitempty"` // NOT
}

// ─── Tokenizer ────────────────────────────────────────────────────────

type tokKind int

const (
	tkTerm tokKind = iota
	tkField
	tkAnd
	tkOr
	tkNot
	tkLParen
	tkRParen
)

type token struct {
	kind  tokKind
	value string
	field string // tkField only
}

// tokenize splits query into tokens, respecting quoted strings, parentheses,
// minus-for-NOT, and field specifiers (title:foo, tag:"quoted value"). It also
// injects implicit AND between adjacent terms so `a b` parses the same as
// `a AND b` — matches the JS tokenizer behavior.
func tokenize(query string) []token {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	r := []rune(q)

	var out []token
	i := 0
	for i < len(r) {
		// Skip whitespace.
		if unicode.IsSpace(r[i]) {
			i++
			continue
		}

		// Quoted string → one TERM.
		if r[i] == '"' {
			start := i + 1
			i++
			for i < len(r) && r[i] != '"' {
				i++
			}
			if i < len(r) {
				out = append(out, token{kind: tkTerm, value: string(r[start:i])})
				i++ // closing quote
			}
			continue
		}

		// Parentheses.
		if r[i] == '(' {
			out = append(out, token{kind: tkLParen})
			i++
			continue
		}
		if r[i] == ')' {
			out = append(out, token{kind: tkRParen})
			i++
			continue
		}

		// Leading `-` as NOT (followed by non-space).
		if r[i] == '-' && i+1 < len(r) && !unicode.IsSpace(r[i+1]) {
			out = append(out, token{kind: tkNot})
			i++
			continue
		}

		// Collect a word.
		start := i
		for i < len(r) && !unicode.IsSpace(r[i]) && r[i] != '"' && r[i] != '(' && r[i] != ')' {
			i++
		}
		word := string(r[start:i])

		switch word {
		case "AND", "&&":
			out = append(out, token{kind: tkAnd})
			continue
		case "OR", "||":
			out = append(out, token{kind: tkOr})
			continue
		case "NOT":
			out = append(out, token{kind: tkNot})
			continue
		}

		// Field specifier — contains ':'.
		if idx := strings.IndexByte(word, ':'); idx > 0 && idx < len(word)-1 {
			field := strings.ToLower(word[:idx])
			value := word[idx+1:]

			// Special case: `field:"quoted value"` where the value starts a quote
			// — keep consuming until the matching close.
			if strings.HasPrefix(value, `"`) && !strings.Contains(value[1:], `"`) {
				var b strings.Builder
				b.WriteString(value[1:])
				for i < len(r) && r[i] != '"' {
					b.WriteRune(r[i])
					i++
				}
				if i < len(r) {
					i++ // closing quote
				}
				out = append(out, token{kind: tkField, field: field, value: b.String()})
				continue
			}
			if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) && len(value) >= 2 {
				value = value[1 : len(value)-1]
			}
			out = append(out, token{kind: tkField, field: field, value: value})
			continue
		}

		// Bare `field:` followed by a space and then a quoted value.
		// Rare in practice but covered by JS; we reproduce for parity.
		if strings.HasSuffix(word, ":") && i < len(r) && r[i] == '"' {
			field := strings.ToLower(strings.TrimSuffix(word, ":"))
			i++ // opening quote
			start := i
			for i < len(r) && r[i] != '"' {
				i++
			}
			if i < len(r) {
				out = append(out, token{kind: tkField, field: field, value: string(r[start:i])})
				i++ // closing quote
			}
			continue
		}

		out = append(out, token{kind: tkTerm, value: word})
	}

	// Insert implicit AND between value-producing tokens.
	var withImplicit []token
	for j, t := range out {
		withImplicit = append(withImplicit, t)
		if j == len(out)-1 {
			continue
		}
		nx := out[j+1]
		curHasValue := t.kind == tkTerm || t.kind == tkField || t.kind == tkRParen
		nextHasValue := nx.kind == tkTerm || nx.kind == tkField || nx.kind == tkLParen || nx.kind == tkNot
		if curHasValue && nextHasValue {
			withImplicit = append(withImplicit, token{kind: tkAnd})
		}
	}
	return withImplicit
}

// ─── Shunting-yard parser ─────────────────────────────────────────────

// ErrBadSyntax signals a user-visible query parse error. Callers should surface
// the original message via the MCP `invalid params` channel.
var ErrBadSyntax = errors.New("invalid search syntax")

var precedence = map[tokKind]int{tkNot: 3, tkAnd: 2, tkOr: 1}

// ParseQuery turns a search string into an Expr tree. Empty / blank input
// returns (nil, nil) so callers can treat that case the same as "match all".
func ParseQuery(query string) (*Expr, error) {
	toks := tokenize(query)
	if len(toks) == 0 {
		return nil, nil
	}

	// Shunting-yard → RPN queue.
	var output []token
	var opstack []token
	for _, t := range toks {
		switch t.kind {
		case tkTerm, tkField:
			output = append(output, t)
		case tkNot:
			opstack = append(opstack, t)
		case tkAnd, tkOr:
			for len(opstack) > 0 {
				top := opstack[len(opstack)-1]
				if top.kind == tkLParen {
					break
				}
				if precedence[top.kind] < precedence[t.kind] {
					break
				}
				output = append(output, top)
				opstack = opstack[:len(opstack)-1]
			}
			opstack = append(opstack, t)
		case tkLParen:
			opstack = append(opstack, t)
		case tkRParen:
			for len(opstack) > 0 && opstack[len(opstack)-1].kind != tkLParen {
				output = append(output, opstack[len(opstack)-1])
				opstack = opstack[:len(opstack)-1]
			}
			if len(opstack) > 0 && opstack[len(opstack)-1].kind == tkLParen {
				opstack = opstack[:len(opstack)-1]
			}
		}
	}
	for len(opstack) > 0 {
		output = append(output, opstack[len(opstack)-1])
		opstack = opstack[:len(opstack)-1]
	}

	// RPN → tree.
	var stack []*Expr
	push := func(e *Expr) { stack = append(stack, e) }
	pop := func() (*Expr, bool) {
		if len(stack) == 0 {
			return nil, false
		}
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		return v, true
	}

	for _, t := range output {
		switch t.kind {
		case tkTerm:
			push(&Expr{Type: NodeTerm, Value: t.value})
		case tkField:
			push(&Expr{Type: NodeField, Field: t.field, Value: t.value})
		case tkNot:
			op, ok := pop()
			if !ok {
				return nil, ErrBadSyntax
			}
			push(&Expr{Type: NodeNot, Operand: op})
		case tkAnd, tkOr:
			right, ok1 := pop()
			left, ok2 := pop()
			if !ok1 || !ok2 {
				return nil, ErrBadSyntax
			}
			typ := NodeAnd
			if t.kind == tkOr {
				typ = NodeOr
			}
			push(&Expr{Type: typ, Left: left, Right: right})
		}
	}

	if len(stack) != 1 {
		return nil, ErrBadSyntax
	}
	return stack[0], nil
}

// ─── Evaluator ────────────────────────────────────────────────────────

// DocMeta is the metadata an Evaluate call consults for field-scoped searches
// (title:, tag:). Extracted at call-site so we don't re-parse the document.
type DocMeta struct {
	Title string
	Tags  []string
}

// Evaluate returns true when the document satisfies the expression.
// A nil expression matches everything — that's how empty queries behave.
//
// When caseSensitive is false (the default search mode), content, title,
// tags, and every query term are folded to lowercase before substring
// comparison. When true, casing is preserved so `title:Foo` only matches
// titles containing the literal `Foo`.
func Evaluate(e *Expr, content string, meta DocMeta, caseSensitive bool) bool {
	if e == nil {
		return true
	}
	if caseSensitive {
		return evalRec(e, content, meta.Title, meta.Tags, true)
	}
	contentLower := strings.ToLower(content)
	titleLower := strings.ToLower(meta.Title)
	tagsLower := make([]string, len(meta.Tags))
	for i, t := range meta.Tags {
		tagsLower[i] = strings.ToLower(t)
	}
	return evalRec(e, contentLower, titleLower, tagsLower, false)
}

func evalRec(e *Expr, content, title string, tags []string, caseSensitive bool) bool {
	switch e.Type {
	case NodeTerm:
		t := e.Value
		if !caseSensitive {
			t = strings.ToLower(t)
		}
		if strings.Contains(content, t) || strings.Contains(title, t) {
			return true
		}
		for _, tag := range tags {
			if strings.Contains(tag, t) {
				return true
			}
		}
		return false
	case NodeField:
		v := e.Value
		if !caseSensitive {
			v = strings.ToLower(v)
		}
		switch e.Field {
		case "title":
			return strings.Contains(title, v)
		case "content":
			return strings.Contains(content, v)
		case "tag":
			for _, tag := range tags {
				if strings.Contains(tag, v) {
					return true
				}
			}
		}
		return false
	case NodeAnd:
		return evalRec(e.Left, content, title, tags, caseSensitive) && evalRec(e.Right, content, title, tags, caseSensitive)
	case NodeOr:
		return evalRec(e.Left, content, title, tags, caseSensitive) || evalRec(e.Right, content, title, tags, caseSensitive)
	case NodeNot:
		return !evalRec(e.Operand, content, title, tags, caseSensitive)
	}
	return false
}

// PositiveTerms walks the expression and returns every term whose match
// should be *shown* to the user — TERM and FIELD nodes; NOT subtrees excluded.
// Used by the content search to decide which lines to highlight/emit.
func PositiveTerms(e *Expr) []string {
	if e == nil {
		return nil
	}
	switch e.Type {
	case NodeTerm, NodeField:
		return []string{e.Value}
	case NodeAnd, NodeOr:
		return append(PositiveTerms(e.Left), PositiveTerms(e.Right)...)
	case NodeNot:
		return nil
	}
	return nil
}
