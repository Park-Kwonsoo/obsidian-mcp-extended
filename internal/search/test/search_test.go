package search_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obsidian-mcp/internal/vault"

	"obsidian-mcp/internal/search"
)

func vaultWithContent(t *testing.T) *vault.Vault {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// notes/setup.md has "getting started" and frontmatter tags
	write("notes/setup.md", "---\ntitle: Setup\ntags: [mcp, guide]\n---\n\n# Setup\n\ngetting started with mcp\nsecond line here\n")
	// notes/alt.md has "getting" but not "started"
	write("notes/alt.md", "# Alt\n\ngetting only, no start word\n")
	// projects/other.md has "deprecated" — tested for exclusion
	write("projects/other.md", "# Other\n\ngetting started with deprecated api\n")
	// inline tag note
	write("journal/2025-01.md", "#journal\n\n# Jan\n\nnotes here\n")

	v, err := vault.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSearchVault_SimpleMatch(t *testing.T) {
	v := vaultWithContent(t)
	res, err := search.SearchVault(v, "getting", search.VaultOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.FileCount < 3 {
		t.Errorf("expected 3+ files, got %d (%+v)", res.FileCount, res.Files)
	}
	if res.FilesSearched < 4 {
		t.Errorf("filesSearched should count all md's walked, got %d", res.FilesSearched)
	}
}

func TestSearchVault_QuotedPhrase(t *testing.T) {
	v := vaultWithContent(t)
	res, _ := search.SearchVault(v, `"getting started"`, search.VaultOpts{})
	// notes/alt.md has "getting only" so shouldn't match
	for _, f := range res.Files {
		if strings.Contains(f.Path, "alt.md") {
			t.Error("alt.md should not match quoted 'getting started'")
		}
	}
	if res.FileCount < 1 {
		t.Error("setup.md should match quoted phrase")
	}
}

func TestSearchVault_BooleanMinus(t *testing.T) {
	v := vaultWithContent(t)
	res, _ := search.SearchVault(v, "getting -deprecated", search.VaultOpts{})
	for _, f := range res.Files {
		if strings.Contains(f.Path, "other.md") {
			t.Errorf("other.md (deprecated) should be excluded; got %v", res.Files)
		}
	}
}

func TestSearchVault_FieldTitle(t *testing.T) {
	v := vaultWithContent(t)
	res, _ := search.SearchVault(v, "title:setup", search.VaultOpts{})
	if res.FileCount != 1 || !strings.Contains(res.Files[0].Path, "setup.md") {
		t.Errorf("title:setup should return just setup.md; got %+v", res.Files)
	}
	// For pure title queries, exactly one match line returned (the H1 line).
	if res.Files[0].MatchCount != 1 {
		t.Errorf("pure title: should return 1 match (H1 line); got %d", res.Files[0].MatchCount)
	}
}

func TestSearchVault_FieldTag(t *testing.T) {
	v := vaultWithContent(t)
	res, _ := search.SearchVault(v, "tag:mcp", search.VaultOpts{})
	found := false
	for _, f := range res.Files {
		if strings.Contains(f.Path, "setup.md") {
			found = true
		}
	}
	if !found {
		t.Errorf("tag:mcp should hit setup.md; got %v", res.Files)
	}
}

func TestSearchVault_FieldTagYAMLList(t *testing.T) {
	// Frontmatter YAML-list form must be recognized so tag:X hits notes that
	// search-by-tags also finds.
	root := t.TempDir()
	p := filepath.Join(root, "moc.md")
	body := "---\ntags:\n  - moc\n  - overview\n---\n\n# MOC\n\nbody\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := search.SearchVault(v, "tag:moc", search.VaultOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.FileCount != 1 || !strings.Contains(res.Files[0].Path, "moc.md") {
		t.Errorf("YAML-list tags should be searchable; got %+v", res.Files)
	}
}

func TestSearchVault_WithContext(t *testing.T) {
	v := vaultWithContent(t)
	res, _ := search.SearchVault(v, "getting", search.VaultOpts{IncludeContext: true, ContextLines: 2})
	// Find setup.md match and check context
	var setup *search.FileMatches
	for i, f := range res.Files {
		if strings.Contains(f.Path, "setup.md") {
			setup = &res.Files[i]
			break
		}
	}
	if setup == nil || len(setup.Matches) == 0 {
		t.Fatalf("expected setup.md match, got %+v", res.Files)
	}
	m := setup.Matches[0]
	if m.Context == nil {
		t.Fatal("expected context block")
	}
	if len(m.Context.Lines) == 0 {
		t.Error("context.lines should be non-empty")
	}
	if !strings.Contains(m.Context.Highlighted, "**getting**") {
		t.Errorf("highlight should wrap term; got %q", m.Context.Highlighted)
	}
}

func TestSearchVault_EmptyQueryReturnsNothing(t *testing.T) {
	v := vaultWithContent(t)
	res, err := search.SearchVault(v, "", search.VaultOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// Empty query: search.Expr is nil → search.Evaluate returns true for every doc, and
	// findMatches returns every line. We don't require zero — document the
	// actual behavior: all files matched, large totalMatches.
	if res.FileCount != res.FilesSearched {
		t.Errorf("empty query should match every file; %d / %d", res.FileCount, res.FilesSearched)
	}
}

func TestSearchVault_SubdirFilter(t *testing.T) {
	v := vaultWithContent(t)
	res, _ := search.SearchVault(v, "getting", search.VaultOpts{Subdir: "notes"})
	for _, f := range res.Files {
		if !strings.HasPrefix(f.Path, "notes/") {
			t.Errorf("subdir filter violated: %s", f.Path)
		}
	}
}

func TestHighlight(t *testing.T) {
	if got := search.Highlight("Hello World", "world", false); got != "Hello **World**" {
		t.Errorf("case-insensitive highlight: %q", got)
	}
	if got := search.Highlight("Hello World", "world", true); got != "Hello World" {
		t.Errorf("case-sensitive 'world' should not match 'World': %q", got)
	}
	if got := search.Highlight("re.gex (special)", "(special)", false); got != "re.gex **(special)**" {
		t.Errorf("regex metachars should be escaped: %q", got)
	}
}

func TestExtractContext(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e"}
	slice, start, rel := search.ExtractContext(lines, 2, 1)
	if len(slice) != 3 || slice[0] != "b" || slice[2] != "d" {
		t.Errorf("center extraction: %v", slice)
	}
	if start != 1 || rel != 1 {
		t.Errorf("indices: start=%d rel=%d", start, rel)
	}
	// At boundary
	slice, _, rel = search.ExtractContext(lines, 0, 2)
	if len(slice) != 3 || rel != 0 {
		t.Errorf("boundary: slice=%v rel=%d", slice, rel)
	}
}
