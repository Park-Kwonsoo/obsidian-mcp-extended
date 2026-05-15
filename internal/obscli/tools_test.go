package obscli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadTemplateFallsBackToTemplateFileOnEmptyCLIOutput(t *testing.T) {
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(vault, "Templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, ".obsidian", "templates.json"), []byte(`{"folder":"Templates"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	want := "---\n# Plan\n\n- [ ] keep moving\n"
	if err := os.WriteFile(filepath.Join(vault, "Templates", "Planning Template.md"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	cli := writeFakeObsidianCLI(t, vault, "")
	exec := &Executor{Binary: cli, VaultPath: vault, Timeout: time.Second}

	got, err := exec.ReadTemplate(context.Background(), "Planning Template", true)
	if err != nil {
		t.Fatalf("ReadTemplate returned error: %v", err)
	}
	if got.Content != want {
		t.Fatalf("content mismatch\nwant: %q\n got: %q", want, got.Content)
	}
}

func TestReadTemplateUsesCLIContentWhenPresent(t *testing.T) {
	vault := t.TempDir()
	cliContent := "from cli"
	cli := writeFakeObsidianCLI(t, vault, cliContent)
	exec := &Executor{Binary: cli, VaultPath: vault, Timeout: time.Second}

	got, err := exec.ReadTemplate(context.Background(), "Planning Template", false)
	if err != nil {
		t.Fatalf("ReadTemplate returned error: %v", err)
	}
	if got.Content != cliContent {
		t.Fatalf("content mismatch\nwant: %q\n got: %q", cliContent, got.Content)
	}
}

func writeFakeObsidianCLI(t *testing.T, vault, templateOutput string) string {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "version" ]; then
  echo "test"
  exit 0
fi
if [ "$1" = "vaults" ]; then
  echo "TestVault  ` + vault + `"
  exit 0
fi
printf '%s' "` + strings.ReplaceAll(templateOutput, `"`, `\"`) + `"
`
	path := filepath.Join(t.TempDir(), "obsidian")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
