package search_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"obsidian-mcp/internal/vault"

	"obsidian-mcp/internal/search"
)

func writeFile(t *testing.T, p, c string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
		t.Fatal(err)
	}
}

func vaultWithTitles(t *testing.T) *vault.Vault {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "alpha.md"), "# Alpha Setup\n\nbody\n")
	writeFile(t, filepath.Join(root, "beta.md"), "not markdown heading\n# Beta Notes #draft\n")
	writeFile(t, filepath.Join(root, "gamma.md"), "## only h2\n# real title after\n")
	writeFile(t, filepath.Join(root, "notitle.md"), "no headings here\n")
	v, err := vault.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSearchByTitle_Basic(t *testing.T) {
	v := vaultWithTitles(t)
	res, err := search.SearchByTitle(v, "alpha", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 1 || res.Results[0].File != "alpha.md" {
		t.Errorf("want 1 match on alpha.md, got %+v", res)
	}
	if res.Results[0].Line != 1 {
		t.Errorf("line number should be 1, got %d", res.Results[0].Line)
	}
}

func TestSearchByTitle_StripsInlineTagSuffix(t *testing.T) {
	v := vaultWithTitles(t)
	// "# Beta Notes #draft" → title "Beta Notes"; querying "notes" matches.
	res, err := search.SearchByTitle(v, "notes", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 1 {
		t.Fatalf("want 1, got %d (%+v)", res.Count, res.Results)
	}
	if res.Results[0].Title != "Beta Notes" {
		t.Errorf("title should be stripped of '#draft'; got %q", res.Results[0].Title)
	}
}

func TestSearchByTitle_IgnoresH2AndUsesFirstH1(t *testing.T) {
	v := vaultWithTitles(t)
	res, err := search.SearchByTitle(v, "real", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 1 {
		t.Fatalf("want 1, got %d", res.Count)
	}
	// "## only h2" at line 1 should have been skipped; "# real title after" is line 2.
	if res.Results[0].Line != 2 {
		t.Errorf("expected line 2 (H1 after H2), got %d", res.Results[0].Line)
	}
}

func TestSearchByTitle_NoMatchWhenNoH1(t *testing.T) {
	v := vaultWithTitles(t)
	res, _ := search.SearchByTitle(v, "headings", "", false)
	// "notitle.md" body contains the word but has no H1 — should not match.
	for _, r := range res.Results {
		if r.File == "notitle.md" {
			t.Errorf("notitle.md with no H1 should not match, got %+v", r)
		}
	}
}

func TestSearchByTitle_CaseSensitive(t *testing.T) {
	v := vaultWithTitles(t)
	// lower-case query with caseSensitive=true against "Alpha Setup" → no match
	res, err := search.SearchByTitle(v, "alpha", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 0 {
		t.Errorf("case-sensitive 'alpha' should miss 'Alpha'; got %d hits", res.Count)
	}
	res2, _ := search.SearchByTitle(v, "Alpha", "", true)
	if res2.Count != 1 {
		t.Errorf("case-sensitive 'Alpha' should match; got %d", res2.Count)
	}
}

// TestTitleHit_JSONShape pins the public response shape for search-by-title
// results. The JS MCP server emitted `{file, title, line}`, so any rename to
// `{path, title, line}` (or similar) silently breaks clients that parse by
// field name. Guarding the serialized form catches that class of regression
// without needing an end-to-end MCP replay.
func TestTitleHit_JSONShape(t *testing.T) {
	b, err := json.Marshal(search.TitleHit{File: "notes/x.md", Title: "X", Line: 3})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"file", "title", "line"} {
		if _, ok := m[key]; !ok {
			t.Errorf("search.TitleHit JSON missing expected key %q; payload=%s", key, b)
		}
	}
	if _, bad := m["path"]; bad {
		t.Errorf("search.TitleHit must not emit legacy %q key — use %q for the vault-relative path; payload=%s", "path", "file", b)
	}
}

func TestSearchByTitle_EmptyQueryReturnsEmpty(t *testing.T) {
	v := vaultWithTitles(t)
	res, err := search.SearchByTitle(v, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 0 {
		t.Errorf("empty query should give 0 results, got %d", res.Count)
	}
}
