package vault_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obsidian-mcp/internal/vault"
)

// writeFile is a tiny helper: create parent dir, write content. Fatal on error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildVault sets up a fixture vault with:
//   notes/a.md        "# A"
//   notes/b.md        "# B"
//   projects/a.md     "# project A"   (duplicate basename — triggers ambiguity)
//   deep/nested/c.md  "# C"
//   .obsidian/app.json (should be skipped by walk)
//   .trash/old.md     (should be skipped)
func buildVault(t *testing.T) *vault.Vault {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes/a.md"), "# A")
	writeFile(t, filepath.Join(root, "notes/b.md"), "# B")
	writeFile(t, filepath.Join(root, "projects/a.md"), "# project A")
	writeFile(t, filepath.Join(root, "deep/nested/c.md"), "# C")
	writeFile(t, filepath.Join(root, ".obsidian/app.json"), "{}")
	writeFile(t, filepath.Join(root, ".trash/old.md"), "gone")
	v, err := vault.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestOpen_RejectsMissing(t *testing.T) {
	if _, err := vault.Open("/nonexistent/path/zzz"); err == nil {
		t.Fatal("missing path should fail")
	}
}

func TestOpen_RejectsFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x.txt")
	writeFile(t, f, "hi")
	if _, err := vault.Open(f); err == nil {
		t.Fatal("file (not dir) should fail")
	}
}

func TestListMarkdown_RootAndSubdir(t *testing.T) {
	v := buildVault(t)

	root, err := v.ListMarkdown("")
	if err != nil {
		t.Fatal(err)
	}
	// 4 user notes; .obsidian and .trash excluded
	want := []string{"deep/nested/c.md", "notes/a.md", "notes/b.md", "projects/a.md"}
	if got := root; !eqSlice(got, want) {
		t.Errorf("root listing mismatch\nwant %v\ngot  %v", want, got)
	}

	sub, err := v.ListMarkdown("notes")
	if err != nil {
		t.Fatal(err)
	}
	if !eqSlice(sub, []string{"notes/a.md", "notes/b.md"}) {
		t.Errorf("subdir listing mismatch: %v", sub)
	}
}

func TestListMarkdown_RejectsTraversal(t *testing.T) {
	v := buildVault(t)
	if _, err := v.ListMarkdown("../escape"); err == nil {
		t.Fatal("traversal should be rejected")
	}
}

func TestResolve_ExactPath(t *testing.T) {
	v := buildVault(t)
	got, err := v.Resolve("notes/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "/notes/a.md") {
		t.Errorf("want ending /notes/a.md, got %q", got)
	}
}

func TestResolve_WikilinkUnique(t *testing.T) {
	v := buildVault(t)
	// only one "b.md" exists — should resolve without ambiguity
	got, err := v.Resolve("b")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "/notes/b.md") {
		t.Errorf("want .../notes/b.md, got %q", got)
	}
	// ".md" suffix on input should also work
	got2, err := v.Resolve("b.md")
	if err != nil {
		t.Fatal(err)
	}
	if got != got2 {
		t.Errorf("with and without .md should match: %q vs %q", got, got2)
	}
}

func TestResolve_WikilinkAmbiguous(t *testing.T) {
	v := buildVault(t)
	_, err := v.Resolve("a") // both notes/a.md and projects/a.md exist
	var amb *vault.ErrAmbiguous
	if !errors.As(err, &amb) {
		t.Fatalf("want vault.ErrAmbiguous, got %v", err)
	}
	if len(amb.Candidates) != 2 {
		t.Errorf("want 2 candidates, got %d: %v", len(amb.Candidates), amb.Candidates)
	}
}

func TestResolve_NotFound(t *testing.T) {
	v := buildVault(t)
	if _, err := v.Resolve("absent"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("want vault.ErrNotFound, got %v", err)
	}
	// Multi-segment path that doesn't exist should be NotFound, not
	// wikilink-resolve by basename.
	if _, err := v.Resolve("foo/bar/absent.md"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("multi-seg non-existent → vault.ErrNotFound, got %v", err)
	}
}

func TestReadNote(t *testing.T) {
	v := buildVault(t)
	content, rel, err := v.ReadNote("notes/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if content != "# A" {
		t.Errorf("unexpected content: %q", content)
	}
	if rel != "notes/a.md" {
		t.Errorf("exact path should echo back as rel; got %q", rel)
	}
}

func TestReadNote_WikilinkResolution(t *testing.T) {
	v := buildVault(t)
	content, rel, err := v.ReadNote("c") // deep/nested/c.md
	if err != nil {
		t.Fatal(err)
	}
	if content != "# C" {
		t.Errorf("unexpected content: %q", content)
	}
	// Bare filename must resolve to the full vault-relative path so clients
	// don't lose track of which file they actually read.
	if rel != filepath.Join("deep", "nested", "c.md") {
		t.Errorf("wikilink resolve should return full rel path; got %q", rel)
	}
}

func TestReadNote_RejectsNonMarkdown(t *testing.T) {
	v := buildVault(t)
	root := v.Root
	writeFile(t, filepath.Join(root, "binary.bin"), "data")
	if _, _, err := v.ReadNote("binary.bin"); err == nil {
		t.Fatal("non-.md read should be rejected")
	}
}

func TestWriteNote_AtomicAndRoundtrip(t *testing.T) {
	v := buildVault(t)
	rel, err := v.WriteNote("notes/new.md", "fresh content")
	if err != nil {
		t.Fatal(err)
	}
	if rel != "notes/new.md" {
		t.Errorf("unexpected rel path: %q", rel)
	}
	got, _, err := v.ReadNote("notes/new.md")
	if err != nil {
		t.Fatal(err)
	}
	if got != "fresh content" {
		t.Errorf("roundtrip mismatch: %q", got)
	}
}

func TestWriteNote_CreatesDirs(t *testing.T) {
	v := buildVault(t)
	_, err := v.WriteNote("newly/made/dir/note.md", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(v.Root, "newly/made/dir/note.md")); err != nil {
		t.Errorf("expected created: %v", err)
	}
}

func TestWriteNote_RejectsNonMarkdown(t *testing.T) {
	v := buildVault(t)
	if _, err := v.WriteNote("x.txt", "hi"); err == nil {
		t.Fatal("txt write should be rejected")
	}
}

func TestWriteNote_SanitizesNulls(t *testing.T) {
	v := buildVault(t)
	_, err := v.WriteNote("nul.md", "hello\x00world")
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := v.ReadNote("nul.md")
	if got != "helloworld" {
		t.Errorf("null bytes should be stripped; got %q", got)
	}
}

func TestWriteNote_RejectsTraversal(t *testing.T) {
	v := buildVault(t)
	if _, err := v.WriteNote("../evil.md", "x"); err == nil {
		t.Fatal("traversal write should be rejected")
	}
}

func TestDeleteNote(t *testing.T) {
	v := buildVault(t)
	if err := v.DeleteNote("notes/a.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(v.Root, "notes/a.md")); !os.IsNotExist(err) {
		t.Errorf("file should be gone; stat err: %v", err)
	}
}

func TestDeleteNote_RejectsNonMarkdown(t *testing.T) {
	v := buildVault(t)
	if err := v.DeleteNote("x.txt"); err == nil {
		t.Fatal("txt delete should be rejected")
	}
}

func eqSlice(a, b []string) bool {
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
