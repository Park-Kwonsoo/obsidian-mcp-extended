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

func TestListTemplatesUsesCLIContentWhenPresent(t *testing.T) {
	vault := buildTemplateVault(t)
	if err := os.WriteFile(filepath.Join(vault, "Templates", "Planning Template.md"), []byte("from file"), 0o644); err != nil {
		t.Fatal(err)
	}

	cli := writeFakeObsidianCLI(t, vault, "From CLI\nOther CLI")
	exec := &Executor{Binary: cli, VaultPath: vault, Timeout: time.Second}

	got, err := exec.ListTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListTemplates returned error: %v", err)
	}
	want := []string{"From CLI", "Other CLI"}
	if strings.Join(got.Templates, "\n") != strings.Join(want, "\n") {
		t.Fatalf("templates mismatch\nwant: %#v\n got: %#v", want, got.Templates)
	}
}

func TestReadTemplateFallsBackToTemplateFileOnCLIVaultNotFound(t *testing.T) {
	vault := buildTemplateVault(t)
	want := "from file"
	if err := os.WriteFile(filepath.Join(vault, "Templates", "Planning Template.md"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	cli := writeFakeObsidianCLI(t, vault, "Vault not found.")
	exec := &Executor{Binary: cli, VaultPath: vault, Timeout: time.Second}

	got, err := exec.ReadTemplate(context.Background(), "Planning Template", true)
	if err != nil {
		t.Fatalf("ReadTemplate returned error: %v", err)
	}
	if got.Content != want {
		t.Fatalf("content mismatch\nwant: %q\n got: %q", want, got.Content)
	}
}

func TestReadTemplateFallsBackToTemplateFileWhenCLIUnavailable(t *testing.T) {
	vault := buildTemplateVault(t)
	want := "from file"
	if err := os.WriteFile(filepath.Join(vault, "Templates", "Planning Template.md"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	exec := &Executor{Binary: filepath.Join(t.TempDir(), "missing-obsidian"), VaultPath: vault, Timeout: time.Second}

	got, err := exec.ReadTemplate(context.Background(), "Planning Template", true)
	if err != nil {
		t.Fatalf("ReadTemplate returned error: %v", err)
	}
	if got.Content != want {
		t.Fatalf("content mismatch\nwant: %q\n got: %q", want, got.Content)
	}
}

func TestReadTemplateFallsBackToTemplateFileOnCLITimeout(t *testing.T) {
	vault := buildTemplateVault(t)
	want := "from file"
	if err := os.WriteFile(filepath.Join(vault, "Templates", "Planning Template.md"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	cli := writeSlowTemplateCLI(t, vault)
	exec := &Executor{Binary: cli, VaultPath: vault, Timeout: 500 * time.Millisecond}

	got, err := exec.ReadTemplate(context.Background(), "Planning Template", true)
	if err != nil {
		t.Fatalf("ReadTemplate returned error: %v", err)
	}
	if got.Content != want {
		t.Fatalf("content mismatch\nwant: %q\n got: %q", want, got.Content)
	}
}

func TestListTemplatesFallsBackToConfiguredTemplateFolderOnCLIVaultNotFound(t *testing.T) {
	vault := buildTemplateVault(t)
	for _, name := range []string{
		"Planning Template.md",
		"Auto/Auto Link Tickets.md",
		"README.txt",
	} {
		path := filepath.Join(vault, "Templates", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cli := writeFakeObsidianCLI(t, vault, "Vault not found.")
	exec := &Executor{Binary: cli, VaultPath: vault, Timeout: time.Second}

	got, err := exec.ListTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListTemplates returned error: %v", err)
	}
	want := []string{"Auto/Auto Link Tickets", "Planning Template"}
	if strings.Join(got.Templates, "\n") != strings.Join(want, "\n") {
		t.Fatalf("templates mismatch\nwant: %#v\n got: %#v", want, got.Templates)
	}
	if got.Count != len(want) {
		t.Fatalf("count mismatch\nwant: %d\n got: %d", len(want), got.Count)
	}
}

func TestListTemplatesFallsBackToConfiguredTemplateFolderWhenCLIUnavailable(t *testing.T) {
	vault := buildTemplateVault(t)
	if err := os.WriteFile(filepath.Join(vault, "Templates", "Planning Template.md"), []byte("from file"), 0o644); err != nil {
		t.Fatal(err)
	}

	exec := &Executor{Binary: filepath.Join(t.TempDir(), "missing-obsidian"), VaultPath: vault, Timeout: time.Second}

	got, err := exec.ListTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListTemplates returned error: %v", err)
	}
	want := []string{"Planning Template"}
	if strings.Join(got.Templates, "\n") != strings.Join(want, "\n") {
		t.Fatalf("templates mismatch\nwant: %#v\n got: %#v", want, got.Templates)
	}
}

func TestListTemplatesFallsBackToConfiguredTemplateFolderOnCLITimeout(t *testing.T) {
	vault := buildTemplateVault(t)
	if err := os.WriteFile(filepath.Join(vault, "Templates", "Planning Template.md"), []byte("from file"), 0o644); err != nil {
		t.Fatal(err)
	}

	cli := writeSlowTemplateCLI(t, vault)
	exec := &Executor{Binary: cli, VaultPath: vault, Timeout: 500 * time.Millisecond}

	got, err := exec.ListTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListTemplates returned error: %v", err)
	}
	want := []string{"Planning Template"}
	if strings.Join(got.Templates, "\n") != strings.Join(want, "\n") {
		t.Fatalf("templates mismatch\nwant: %#v\n got: %#v", want, got.Templates)
	}
}

func buildTemplateVault(t *testing.T) string {
	t.Helper()
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
	return vault
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

func writeSlowTemplateCLI(t *testing.T, vault string) string {
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
sleep 1
`
	path := filepath.Join(t.TempDir(), "obsidian")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
