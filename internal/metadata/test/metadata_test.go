package metadata_test

import (
	"reflect"
	"testing"

	"obsidian-mcp/internal/metadata"
)

func TestExtractFrontmatterTags_ArrayForm(t *testing.T) {
	c := "---\ntitle: X\ntags: [alpha, beta, gamma]\n---\n\nbody"
	got := metadata.ExtractFrontmatterTags(c)
	want := []string{"alpha", "beta", "gamma"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("array form: want %v, got %v", want, got)
	}
}

func TestExtractFrontmatterTags_QuotedArray(t *testing.T) {
	c := `---
title: X
tags: ["alpha", 'beta']
---

body`
	got := metadata.ExtractFrontmatterTags(c)
	want := []string{"alpha", "beta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("quoted array: want %v, got %v", want, got)
	}
}

func TestExtractFrontmatterTags_YAMLList(t *testing.T) {
	c := "---\ntitle: X\ntags:\n  - alpha\n  - beta\n---\n"
	got := metadata.ExtractFrontmatterTags(c)
	want := []string{"alpha", "beta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("yaml list: want %v, got %v", want, got)
	}
}

func TestExtractFrontmatterTags_SingleScalar(t *testing.T) {
	c := "---\ntitle: X\ntags: alpha\n---\n"
	got := metadata.ExtractFrontmatterTags(c)
	if !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Errorf("single scalar: got %v", got)
	}
}

func TestExtractFrontmatterTags_Missing(t *testing.T) {
	if got := metadata.ExtractFrontmatterTags("# no fm\nbody"); got != nil {
		t.Errorf("no frontmatter → nil; got %v", got)
	}
}

func TestExtractInlineTags_Basic(t *testing.T) {
	c := "Some body with #alpha and #beta-tag.\nAnother line #gamma."
	got := metadata.ExtractInlineTags(c)
	want := []string{"alpha", "beta-tag", "gamma"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestExtractInlineTags_IncludesHexAndSkipsHeadings(t *testing.T) {
	// Real behavior (matches JS + Obsidian CLI): `#fff`/`#e6e6e6` are accepted
	// as tags. Heading markers `##`/`###` don't produce a capture because they
	// have `#` immediately preceding — the negative lookbehind rule kicks in.
	c := "## Heading\nsome #e6e6e6 hex-like value\n#alpha real tag"
	got := metadata.ExtractInlineTags(c)
	want := []string{"e6e6e6", "alpha"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestExtractInlineTags_SkipsCodeBlocks(t *testing.T) {
	c := "body #real\n```\n#fake-in-code\n```\nmore #also-real"
	got := metadata.ExtractInlineTags(c)
	for _, g := range got {
		if g == "fake-in-code" {
			t.Errorf("code block tag leaked: %v", got)
		}
	}
	want := []string{"real", "also-real"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestExtractTags_MergesAndDedupes(t *testing.T) {
	c := "---\ntags: [alpha, beta]\n---\n\nbody #beta #gamma"
	got := metadata.ExtractTags(c)
	want := []string{"alpha", "beta", "gamma"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("merge+dedupe: want %v, got %v", want, got)
	}
}

func TestHasAllTags(t *testing.T) {
	note := []string{"MCP", "Design", "Go"}
	if !metadata.HasAllTags(note, []string{"mcp"}, false) {
		t.Error("case-insensitive single should match")
	}
	if !metadata.HasAllTags(note, []string{"mcp", "design"}, false) {
		t.Error("two-tag intersection should match")
	}
	if metadata.HasAllTags(note, []string{"mcp", "rust"}, false) {
		t.Error("missing tag should fail")
	}
	if metadata.HasAllTags(note, []string{"mcp"}, true) {
		t.Error("case-sensitive 'mcp' vs 'MCP' should miss")
	}
	if !metadata.HasAllTags(note, nil, false) {
		t.Error("empty wanted → true")
	}
}

func TestExtractWikilinks(t *testing.T) {
	c := "See [[Setup]] and [[folder/Notes|my notes]].\nAnother [[Setup]]."
	got := metadata.ExtractWikilinks(c)
	want := []string{"Setup", "folder/Notes"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestIsMoc(t *testing.T) {
	if !metadata.IsMoc([]string{"foo", "MOC"}) {
		t.Error("case-insensitive MOC should be true")
	}
	if metadata.IsMoc([]string{"foo", "bar"}) {
		t.Error("no moc tag → false")
	}
}
