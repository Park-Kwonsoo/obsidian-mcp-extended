package search_test

import (
	"testing"

	"obsidian-mcp/internal/search"
)

func TestParseQuery_SimpleTerm(t *testing.T) {
	e, err := search.ParseQuery("hello")
	if err != nil || e == nil || e.Type != search.NodeTerm || e.Value != "hello" {
		t.Errorf("want TERM{hello}, got %+v err=%v", e, err)
	}
}

func TestParseQuery_ImplicitAnd(t *testing.T) {
	e, _ := search.ParseQuery("alpha beta")
	if e == nil || e.Type != search.NodeAnd {
		t.Fatalf("want AND, got %+v", e)
	}
	if e.Left.Value != "alpha" || e.Right.Value != "beta" {
		t.Errorf("expected alpha,beta operands; got %+v %+v", e.Left, e.Right)
	}
}

func TestParseQuery_ExplicitOperators(t *testing.T) {
	e, _ := search.ParseQuery("a AND b OR c")
	// AND has higher precedence than OR → OR( AND(a,b), c )
	if e.Type != search.NodeOr || e.Left.Type != search.NodeAnd {
		t.Errorf("precedence wrong; got %+v", e)
	}
	if e.Left.Left.Value != "a" || e.Left.Right.Value != "b" || e.Right.Value != "c" {
		t.Errorf("operand mismatch: %+v", e)
	}
}

func TestParseQuery_MinusIsNot(t *testing.T) {
	e, _ := search.ParseQuery("foo -bar")
	// expected: AND( foo, NOT(bar) )
	if e.Type != search.NodeAnd || e.Right.Type != search.NodeNot || e.Right.Operand.Value != "bar" {
		t.Errorf("minus-as-NOT failed: %+v", e)
	}
}

func TestParseQuery_QuotedPhrase(t *testing.T) {
	e, _ := search.ParseQuery(`"getting started"`)
	if e.Type != search.NodeTerm || e.Value != "getting started" {
		t.Errorf("quoted phrase → single TERM; got %+v", e)
	}
}

func TestParseQuery_Grouping(t *testing.T) {
	e, _ := search.ParseQuery("(python OR javascript) AND tutorial")
	if e.Type != search.NodeAnd {
		t.Fatalf("want top-level AND, got %+v", e)
	}
	if e.Left.Type != search.NodeOr {
		t.Errorf("want OR as left operand, got %+v", e.Left)
	}
	if e.Right.Value != "tutorial" {
		t.Errorf("want 'tutorial' right, got %+v", e.Right)
	}
}

func TestParseQuery_FieldSpec(t *testing.T) {
	e, _ := search.ParseQuery("title:setup")
	if e.Type != search.NodeField || e.Field != "title" || e.Value != "setup" {
		t.Errorf("want FIELD{title,setup}, got %+v", e)
	}
}

func TestParseQuery_FieldQuoted(t *testing.T) {
	e, _ := search.ParseQuery(`title:"exact phrase"`)
	if e.Type != search.NodeField || e.Field != "title" || e.Value != "exact phrase" {
		t.Errorf("want FIELD quoted phrase, got %+v", e)
	}
}

func TestParseQuery_EmptyIsNil(t *testing.T) {
	if e, err := search.ParseQuery(""); e != nil || err != nil {
		t.Errorf("empty query → nil,nil; got %+v %v", e, err)
	}
	if e, err := search.ParseQuery("   "); e != nil || err != nil {
		t.Errorf("blank query → nil,nil; got %+v %v", e, err)
	}
}

func TestEvaluate_Term(t *testing.T) {
	e, _ := search.ParseQuery("context")
	if !search.Evaluate(e, "Lorem context ipsum", search.DocMeta{}, false) {
		t.Error("term should match content")
	}
	if !search.Evaluate(e, "", search.DocMeta{Title: "search.Context Notes"}, false) {
		t.Error("term should match title")
	}
	if !search.Evaluate(e, "", search.DocMeta{Tags: []string{"deep-context-ref"}}, false) {
		t.Error("term should match tag substring")
	}
	if search.Evaluate(e, "unrelated body", search.DocMeta{}, false) {
		t.Error("term shouldn't match unrelated content")
	}
}

func TestEvaluate_FieldTitle(t *testing.T) {
	e, _ := search.ParseQuery("title:setup")
	if !search.Evaluate(e, "no match in body", search.DocMeta{Title: "Setup guide"}, false) {
		t.Error("title: should match in title")
	}
	if search.Evaluate(e, "setup body", search.DocMeta{Title: "other"}, false) {
		t.Error("title: shouldn't match just body")
	}
}

func TestEvaluate_CaseSensitive(t *testing.T) {
	// title:Foo — exact case required when caseSensitive=true.
	e, _ := search.ParseQuery("title:Foo")
	if !search.Evaluate(e, "", search.DocMeta{Title: "Foo Bar"}, true) {
		t.Error("case-sensitive title:Foo should match 'Foo Bar'")
	}
	if search.Evaluate(e, "", search.DocMeta{Title: "foo bar"}, true) {
		t.Error("case-sensitive title:Foo should NOT match 'foo bar'")
	}
	// Same query, case-insensitive: both should match.
	if !search.Evaluate(e, "", search.DocMeta{Title: "foo bar"}, false) {
		t.Error("case-insensitive title:Foo should match 'foo bar'")
	}

	// tag:Bar — casing preserved under caseSensitive=true.
	tagExpr, _ := search.ParseQuery("tag:Bar")
	if !search.Evaluate(tagExpr, "", search.DocMeta{Tags: []string{"Bar"}}, true) {
		t.Error("case-sensitive tag:Bar should match tag 'Bar'")
	}
	if search.Evaluate(tagExpr, "", search.DocMeta{Tags: []string{"bar"}}, true) {
		t.Error("case-sensitive tag:Bar should NOT match tag 'bar'")
	}

	// NOT title:Foo — negated form must also honor caseSensitive.
	notExpr, _ := search.ParseQuery("NOT title:Foo")
	if search.Evaluate(notExpr, "", search.DocMeta{Title: "Foo Bar"}, true) {
		t.Error("NOT title:Foo should exclude 'Foo Bar' under caseSensitive=true")
	}
	if !search.Evaluate(notExpr, "", search.DocMeta{Title: "foo bar"}, true) {
		t.Error("NOT title:Foo should include 'foo bar' under caseSensitive=true (different case)")
	}
}

func TestEvaluate_FieldTag(t *testing.T) {
	e, _ := search.ParseQuery("tag:mcp")
	if !search.Evaluate(e, "", search.DocMeta{Tags: []string{"mcp", "design"}}, false) {
		t.Error("tag: exact match failed")
	}
	if !search.Evaluate(e, "", search.DocMeta{Tags: []string{"mcp-server"}}, false) {
		t.Error("tag: substring match failed")
	}
}

func TestEvaluate_BooleanAndOrNot(t *testing.T) {
	e, _ := search.ParseQuery("(alpha OR beta) AND -gamma")
	if !search.Evaluate(e, "alpha content", search.DocMeta{}, false) {
		t.Error("alpha AND !gamma should be true")
	}
	if search.Evaluate(e, "alpha gamma", search.DocMeta{}, false) {
		t.Error("gamma should exclude")
	}
	if !search.Evaluate(e, "beta content", search.DocMeta{}, false) {
		t.Error("beta leg of OR should match")
	}
	if search.Evaluate(e, "neither", search.DocMeta{}, false) {
		t.Error("no positive term → no match")
	}
}

func TestPositiveTerms(t *testing.T) {
	e, _ := search.ParseQuery("a OR b -c")
	terms := search.PositiveTerms(e)
	// positives: a, b (c is inside NOT → excluded)
	got := map[string]bool{}
	for _, t := range terms {
		got[t] = true
	}
	if !got["a"] || !got["b"] || got["c"] {
		t.Errorf("positives wrong: %v", terms)
	}
}
