package security_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"obsidian-mcp/internal/security"
)

func TestResolveVaultPath(t *testing.T) {
	// Use /tmp so filepath.Abs is stable and we avoid cwd drift between runs.
	vault := t.TempDir()
	absVault, _ := filepath.Abs(vault)

	tests := []struct {
		name    string
		target  string
		wantAbs string
		wantErr error
	}{
		{name: "empty target returns vault root", target: "", wantAbs: absVault},
		{name: "simple relative", target: "note.md", wantAbs: filepath.Join(absVault, "note.md")},
		{name: "nested relative", target: "sub/dir/note.md", wantAbs: filepath.Join(absVault, "sub/dir/note.md")},
		{name: "dot-dot escape", target: "../evil.md", wantErr: security.ErrPathOutsideVault},
		{name: "deep dot-dot escape", target: "sub/../../../evil", wantErr: security.ErrPathOutsideVault},
		{name: "absolute inside vault", target: filepath.Join(absVault, "ok.md"), wantAbs: filepath.Join(absVault, "ok.md")},
		{name: "absolute outside vault", target: "/etc/passwd", wantErr: security.ErrAbsolutePathOutside},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := security.ResolveVaultPath(vault, tc.target)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("want err %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.wantAbs {
				t.Fatalf("path mismatch: want %q got %q", tc.wantAbs, got)
			}
		})
	}
}

func TestResolveVaultPath_EmptyBase(t *testing.T) {
	if _, err := security.ResolveVaultPath("", "note.md"); !errors.Is(err, security.ErrBasePathRequired) {
		t.Fatalf("want security.ErrBasePathRequired, got %v", err)
	}
}

func TestRequireMarkdown(t *testing.T) {
	tests := []struct {
		in      string
		wantErr error
	}{
		{"", security.ErrFilePathRequired},
		{"note.md", nil},
		{"NOTE.MD", nil},
		{"folder/note.md", nil},
		{"note.txt", security.ErrNotMarkdown},
		{"note", security.ErrNotMarkdown},
		{".md", nil}, // Obsidian allows leading-dot markdown files in practice
	}
	for _, tc := range tests {
		err := security.RequireMarkdown(tc.in)
		if tc.wantErr != nil {
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("%q: want %v, got %v", tc.in, tc.wantErr, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected err: %v", tc.in, err)
		}
	}
}

func TestRequireParams(t *testing.T) {
	if err := security.RequireParams(map[string]any{"query": "hi"}, []string{"query"}); err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}
	if err := security.RequireParams(map[string]any{}, []string{"query"}); err == nil {
		t.Fatal("missing param accepted")
	} else if !strings.Contains(err.Error(), "query") {
		t.Errorf("error should name the missing param; got %q", err.Error())
	}
	if err := security.RequireParams(map[string]any{"query": ""}, []string{"query"}); err == nil {
		t.Fatal("empty string param accepted (JS treats it as missing)")
	}
	if err := security.RequireParams(nil, []string{"query"}); err == nil {
		t.Fatal("nil params accepted")
	}
}

func TestSanitizeContent(t *testing.T) {
	got := security.SanitizeContent("hello\x00world\x00\x00!")
	if got != "helloworld!" {
		t.Errorf("want 'helloworld!', got %q", got)
	}
	if got := security.SanitizeContent(""); got != "" {
		t.Errorf("empty in → empty out; got %q", got)
	}
}

func TestCheckFileSize(t *testing.T) {
	if err := security.CheckFileSize(100, 1024); err != nil {
		t.Errorf("100 within 1024 should pass: %v", err)
	}
	if err := security.CheckFileSize(2048, 1024); !errors.Is(err, security.ErrFileTooLarge) {
		t.Errorf("2048 exceeds 1024 should err security.ErrFileTooLarge; got %v", err)
	}
	// Zero maxSize falls back to config default (10 MiB)
	if err := security.CheckFileSize(100, 0); err != nil {
		t.Errorf("0 maxSize should fall back to config default: %v", err)
	}
}

func TestFormatFileSize(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0.00 B"},
		{512, "512.00 B"},
		{1024, "1.00 KB"},
		{1536, "1.50 KB"},
		{1024 * 1024, "1.00 MB"},
		{10 * 1024 * 1024, "10.00 MB"},
		{1024 * 1024 * 1024, "1.00 GB"},
	}
	for _, tc := range tests {
		got := security.FormatFileSize(tc.in)
		if got != tc.want {
			t.Errorf("security.FormatFileSize(%d): want %q, got %q", tc.in, tc.want, got)
		}
	}
	if got := security.FormatFileSize(-1); got != "Invalid size" {
		t.Errorf("negative size → 'Invalid size'; got %q", got)
	}
}
