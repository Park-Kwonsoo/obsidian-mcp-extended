package search_test

import (
	"os"
	"path/filepath"
	"testing"

	"obsidian-mcp/internal/vault"

	"obsidian-mcp/internal/search"
)

func vaultWithTags(t *testing.T) *vault.Vault {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("notes/mcp.md", "---\ntags: [mcp, design]\n---\n\n# MCP Design\n")
	write("notes/design.md", "---\ntags: [design, draft]\n---\n\n# Design Notes\n")
	write("journal/2025.md", "#journal\n\n# Journal 2025\n\nwith #mcp inline")
	write("archive/old.md", "#archived\n\n# Old note")
	v, err := vault.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSearchByTags_SingleTag(t *testing.T) {
	v := vaultWithTags(t)
	res, err := search.SearchByTags(v, []string{"mcp"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 2 { // notes/mcp.md (frontmatter), journal/2025.md (inline)
		t.Errorf("want 2, got %d (%v)", res.Count, res.Notes)
	}
}

func TestSearchByTags_IntersectionIsAnd(t *testing.T) {
	v := vaultWithTags(t)
	res, _ := search.SearchByTags(v, []string{"mcp", "design"}, "", false)
	if res.Count != 1 {
		t.Errorf("intersection: want 1, got %d (%v)", res.Count, res.Notes)
	}
	if res.Notes[0].Path != "notes/mcp.md" {
		t.Errorf("want notes/mcp.md, got %s", res.Notes[0].Path)
	}
}

func TestSearchByTags_StripsLeadingHash(t *testing.T) {
	v := vaultWithTags(t)
	// Should be equivalent to searching "mcp"
	res, _ := search.SearchByTags(v, []string{"#mcp"}, "", false)
	if res.Count == 0 {
		t.Errorf("#-prefixed tag should work the same; got %d", res.Count)
	}
}

func TestSearchByTags_CaseSensitive(t *testing.T) {
	v := vaultWithTags(t)
	// Query "MCP" case-sensitive — frontmatter stores "mcp" (lowercase), so miss.
	res, _ := search.SearchByTags(v, []string{"MCP"}, "", true)
	if res.Count != 0 {
		t.Errorf("case-sensitive 'MCP' should miss 'mcp' tags; got %d", res.Count)
	}
	// And case-insensitive should find them
	res2, _ := search.SearchByTags(v, []string{"MCP"}, "", false)
	if res2.Count != 2 {
		t.Errorf("case-insensitive 'MCP' should find 2; got %d", res2.Count)
	}
}

func TestSearchByTags_EmptyWanted(t *testing.T) {
	v := vaultWithTags(t)
	res, _ := search.SearchByTags(v, nil, "", false)
	if res.Count != 0 {
		t.Errorf("nil wanted → empty result; got %d", res.Count)
	}
	res2, _ := search.SearchByTags(v, []string{"", "  "}, "", false)
	if res2.Count != 0 {
		t.Errorf("whitespace-only wanted → empty result; got %d", res2.Count)
	}
}

func TestSearchByTags_SubdirFilter(t *testing.T) {
	v := vaultWithTags(t)
	res, _ := search.SearchByTags(v, []string{"mcp"}, "notes", false)
	// journal/2025.md is outside `notes/` → excluded
	for _, n := range res.Notes {
		if n.Path != "notes/mcp.md" {
			t.Errorf("subdir filter violated: %s", n.Path)
		}
	}
	if res.Count != 1 {
		t.Errorf("want 1, got %d", res.Count)
	}
}
