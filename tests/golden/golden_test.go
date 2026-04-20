// Package golden asserts that each Go library function produces the same
// *semantic* result the current JS MCP server produces on the synth-100
// fixture vault. The goldens in ../../testdata/golden/ were captured from
// the JS implementation (see scripts/capture_goldens.py) and committed to
// the repo; a Go implementation change that breaks behavior parity will
// fail here before it reaches a release.
//
// We compare invariants (sets of paths, counts, totals) rather than raw
// JSON, because the two implementations differ in insignificant ways (field
// order, exact whitespace in match.content) that we don't want flagged as
// regressions. If the Go representation changes shape, update the golden
// by running the capture script — don't paper over a semantic drift.
package golden

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"obsidian-mcp/internal/search"
	"obsidian-mcp/internal/vault"
)

var update = flag.Bool("update", false, "rewrite golden files from Go output (use only after intentional behavior changes)")

// defaultFixture is where the P0 bench sandbox places synth-100 by default.
// Tests can override via OBSIDIAN_MCP_TEST_VAULT for CI or custom layouts.
// Absolute rather than relative because the bench sandbox sits outside the
// repo and "../" chains get brittle when the module moves.
const defaultFixture = "/tmp/obsidian-mcp-bench/vaults/synth-100"

// Summary captures the semantic invariants of a tool response. Only the
// fields relevant to each tool are populated; unused fields stay at their
// zero value and are omitted from JSON via `omitempty`.
type Summary struct {
	Tool    string         `json:"tool"`
	Args    map[string]any `json:"args"`
	Paths   []string       `json:"paths,omitempty"`   // list-notes, search-by-tags
	Count   int            `json:"count,omitempty"`   // list-notes
	Total   int            `json:"total,omitempty"`   // list-notes, search-by-title, search-vault
	Titles  []titleTuple   `json:"titles,omitempty"`  // search-by-title
	Matches []matchTuple   `json:"matches,omitempty"` // search-vault (per-file rollup)
}

type titleTuple struct {
	Path  string `json:"path"`
	Title string `json:"title"`
	Line  int    `json:"line"`
}

type matchTuple struct {
	Path       string `json:"path"`
	MatchCount int    `json:"matchCount"`
}

// resolveVault returns the absolute fixture path; skips the test if the
// synth-100 vault was cleaned (e.g. after a reboot — /tmp/ isn't preserved).
func resolveVault(t *testing.T) string {
	t.Helper()
	path := os.Getenv("OBSIDIAN_MCP_TEST_VAULT")
	if path == "" {
		path = defaultFixture
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Skipf("fixture vault missing at %s — run /tmp/obsidian-mcp-bench/scripts/gen_synth_vault.py or set OBSIDIAN_MCP_TEST_VAULT", abs)
	}
	return abs
}

// loadOrUpdate reads the golden file, or writes `got` to it when -update.
// Golden file layout: `../../testdata/golden/<name>.json`.
func loadOrUpdate(t *testing.T, name string, got Summary) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "golden", name+".json")
	if *update {
		b, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated golden: %s", path)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create)", path, err)
	}
	var want Summary
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("decode golden %s: %v", path, err)
	}
	if !equalSummary(want, got) {
		// Render both sides as pretty JSON for diff-friendliness.
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		wantJSON, _ := json.MarshalIndent(want, "", "  ")
		t.Errorf("golden %s mismatch\n--- want ---\n%s\n--- got ---\n%s",
			name, wantJSON, gotJSON)
	}
}

// equalSummary compares two summaries with the set semantics we actually
// care about: path sets (unordered for tags, ordered for list-notes),
// counts, totals, and tuple sets for search-by-title / search-vault.
func equalSummary(a, b Summary) bool {
	if a.Tool != b.Tool || a.Count != b.Count || a.Total != b.Total {
		return false
	}
	if !sliceEqOrdered(a.Paths, b.Paths) {
		return false
	}
	if !titlesEq(a.Titles, b.Titles) {
		return false
	}
	if !matchesEq(a.Matches, b.Matches) {
		return false
	}
	if !reflect.DeepEqual(a.Args, b.Args) {
		return false
	}
	return true
}

func sliceEqOrdered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func titlesEq(a, b []titleTuple) bool {
	if len(a) != len(b) {
		return false
	}
	// Compare as sorted set — JS and Go may emit in different order depending
	// on filesystem walk order.
	ca := append([]titleTuple(nil), a...)
	cb := append([]titleTuple(nil), b...)
	sort.Slice(ca, func(i, j int) bool { return ca[i].Path < ca[j].Path })
	sort.Slice(cb, func(i, j int) bool { return cb[i].Path < cb[j].Path })
	for i := range ca {
		if ca[i] != cb[i] {
			return false
		}
	}
	return true
}

func matchesEq(a, b []matchTuple) bool {
	if len(a) != len(b) {
		return false
	}
	ca := append([]matchTuple(nil), a...)
	cb := append([]matchTuple(nil), b...)
	sort.Slice(ca, func(i, j int) bool { return ca[i].Path < ca[j].Path })
	sort.Slice(cb, func(i, j int) bool { return cb[i].Path < cb[j].Path })
	for i := range ca {
		if ca[i] != cb[i] {
			return false
		}
	}
	return true
}

// ─── Tests ────────────────────────────────────────────────────────────

func TestGolden_ListNotes(t *testing.T) {
	v, _ := vault.Open(resolveVault(t))
	notes, err := v.ListMarkdown("")
	if err != nil {
		t.Fatal(err)
	}
	got := Summary{
		Tool:  "list-notes",
		Args:  map[string]any{"directory": "", "limit": float64(100000), "offset": float64(0)},
		Paths: notes,
		Count: len(notes),
		Total: len(notes),
	}
	loadOrUpdate(t, "list-notes_whole-vault", got)
}

func TestGolden_ListNotes_Subdir(t *testing.T) {
	v, _ := vault.Open(resolveVault(t))
	notes, err := v.ListMarkdown("_mocs")
	if err != nil {
		t.Fatal(err)
	}
	got := Summary{
		Tool:  "list-notes",
		Args:  map[string]any{"directory": "_mocs", "limit": float64(100000), "offset": float64(0)},
		Paths: notes,
		Count: len(notes),
		Total: len(notes),
	}
	loadOrUpdate(t, "list-notes_mocs-only", got)
}

func TestGolden_SearchByTitle(t *testing.T) {
	v, _ := vault.Open(resolveVault(t))
	res, err := search.SearchByTitle(v, "Section", "", false)
	if err != nil {
		t.Fatal(err)
	}
	titles := make([]titleTuple, len(res.Results))
	for i, r := range res.Results {
		titles[i] = titleTuple{Path: r.File, Title: r.Title, Line: r.Line}
	}
	got := Summary{
		Tool:   "search-by-title",
		Args:   map[string]any{"query": "Section", "caseSensitive": false},
		Titles: titles,
		Total:  res.Count,
	}
	loadOrUpdate(t, "search-by-title_Section", got)
}

func TestGolden_SearchVault_SimpleToken(t *testing.T) {
	// Uses a narrower query so the total stays below the JS server's
	// maxSearchResults=100 cap — otherwise we'd be comparing a post-paginated
	// JS result against an un-capped Go result and flagging a policy
	// difference as a semantic regression. Policy parity (applying the cap in
	// Go) is a P2 decision; this test targets search *correctness*.
	v, _ := vault.Open(resolveVault(t))
	// The quoted phrase only appears in `_mocs/*.md` (the synth generator
	// writes it verbatim into MOC notes). Total matches stay far below the
	// JS 100-match cap so the two implementations' file sets align.
	const narrow = `"Linked Notes"`
	res, err := search.SearchVault(v, narrow, search.VaultOpts{})
	if err != nil {
		t.Fatal(err)
	}
	got := summarySearchVault("search-vault", map[string]any{"query": narrow}, res)
	loadOrUpdate(t, "search-vault_simple-token", got)
}

func TestGolden_SearchVault_BooleanMinus(t *testing.T) {
	v, _ := vault.Open(resolveVault(t))
	res, err := search.SearchVault(v, "context -deploy", search.VaultOpts{})
	if err != nil {
		t.Fatal(err)
	}
	got := summarySearchVault("search-vault", map[string]any{"query": "context -deploy"}, res)
	loadOrUpdate(t, "search-vault_boolean-minus", got)
}

func TestGolden_SearchByTags_Single(t *testing.T) {
	v, _ := vault.Open(resolveVault(t))
	res, err := search.SearchByTags(v, []string{"moc"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, len(res.Notes))
	for i, n := range res.Notes {
		paths[i] = n.Path
	}
	sort.Strings(paths) // set comparison — tag search order isn't semantically meaningful
	got := Summary{
		Tool:  "search-by-tags",
		Args:  map[string]any{"tags": []any{"moc"}, "caseSensitive": false},
		Paths: paths,
		Count: res.Count,
	}
	loadOrUpdate(t, "search-by-tags_moc", got)
}

// summarySearchVault reduces a VaultResults to the *set of files* matched.
// We deliberately drop per-file match counts and vault-wide totals from the
// assertion because JS caps results at maxSearchResults=100 inside its
// paginateSearchResults helper while the Go library returns un-capped
// results; applying that cap in Go is a P2 policy decision, not a
// correctness concern. The set of notes that match a query is the
// semantically meaningful invariant at this stage.
func summarySearchVault(tool string, args map[string]any, res search.VaultResults) Summary {
	paths := make([]string, 0, len(res.Files))
	for _, f := range res.Files {
		paths = append(paths, f.Path)
	}
	sort.Strings(paths)
	return Summary{
		Tool:  tool,
		Args:  args,
		Paths: paths,
		Count: len(paths),
	}
}

// lowercaseName is a small safety net: the CI will refuse to pick up golden
// names that collide case-insensitively (e.g. macOS fs is case-insensitive
// by default so `Foo.json` and `foo.json` would shadow each other).
func init() {
	entries, err := os.ReadDir(filepath.Join("..", "..", "testdata", "golden"))
	if err != nil {
		return
	}
	seen := map[string]string{}
	for _, e := range entries {
		n := strings.ToLower(e.Name())
		if prev, ok := seen[n]; ok && prev != e.Name() {
			panic("case-colliding golden files: " + prev + " vs " + e.Name())
		}
		seen[n] = e.Name()
	}
}
