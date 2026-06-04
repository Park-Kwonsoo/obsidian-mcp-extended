// Package vault exposes the vault root and the primitives every tool needs:
// listing .md files, reading a note (with wikilink-style name resolution),
// and writing atomically. All fs access flows through here so that the
// path-traversal, extension, and size rules live in exactly one place.
package vault

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"obsidian-mcp/internal/config"
	"obsidian-mcp/internal/security"
)

// Vault represents a rooted view into the user's Obsidian directory.
// Every method rejects paths that escape Root — callers can pass untrusted
// relative paths without pre-validating.
type Vault struct {
	Root string // absolute, cleaned
}

// Open verifies root exists and is a directory, then returns a Vault pinned to
// the absolute path. All subsequent operations are relative to Root.
func Open(root string) (*Vault, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve vault root: %w", err)
	}
	// Resolve symlinks in the root. A symlinked vault (e.g. ~/vault -> an
	// iCloud Drive directory) breaks filepath.WalkDir: WalkDir starts with
	// os.Lstat(root), and a symlink reports as a non-directory, so the walk
	// descends into nothing and ListMarkdown/Resolve silently return zero
	// notes. Pinning Root to the real path makes every walk work. If
	// resolution fails (e.g. a not-yet-existing path) keep abs and let the
	// os.Stat below surface the real error.
	if resolved, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat vault root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("vault root not a directory: %s", abs)
	}
	return &Vault{Root: abs}, nil
}

// ErrAmbiguous is returned by Resolve when a bare filename matches more than
// one .md file in the vault. Callers can inspect Candidates to present options.
type ErrAmbiguous struct {
	Name       string
	Candidates []string // relative paths, sorted
}

func (e *ErrAmbiguous) Error() string {
	return fmt.Sprintf("ambiguous note name %q: %d candidates", e.Name, len(e.Candidates))
}

// ErrNotFound is returned by Resolve when neither an exact path nor a
// filename search turns up a match.
var ErrNotFound = errors.New("note not found")

// Resolve accepts either (a) an exact relative path inside the vault, or (b) a
// bare filename — matching Obsidian's wikilink semantics — and returns the
// absolute path to the resolved file. The .md extension is optional on input.
//
// Order of attempts:
//  1. If the path contains a separator or ends in .md and the file exists at
//     that exact location, use it. Exact path wins.
//  2. Otherwise walk the vault looking for files whose basename matches
//     "<name>.md" (case-insensitive). 0 hits → ErrNotFound, ≥2 → ErrAmbiguous.
//
// This mirrors src/backends/resolver.js and is the single place wikilink
// resolution lives in the Go port.
func (v *Vault) Resolve(userPath string) (string, error) {
	if userPath == "" {
		return "", errors.New("path is required")
	}

	// Step 1: exact relative path.
	candidate, err := security.ResolveVaultPath(v.Root, userPath)
	if err == nil {
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}

	// Step 2: bare-filename wikilink resolution. Only kick in if the input has
	// no separator — a multi-segment path that failed Step 1 is definitively
	// not found rather than "try again by basename".
	if strings.ContainsRune(userPath, filepath.Separator) {
		return "", ErrNotFound
	}

	target := strings.ToLower(strings.TrimSuffix(userPath, ".md")) + ".md"
	var hits []string
	err = filepath.WalkDir(v.Root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if skipDir(d.Name(), p, v.Root) {
				return fs.SkipDir
			}
			return nil
		}
		if strings.EqualFold(d.Name(), target) {
			rel, _ := filepath.Rel(v.Root, p)
			hits = append(hits, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	switch len(hits) {
	case 0:
		return "", ErrNotFound
	case 1:
		return filepath.Join(v.Root, hits[0]), nil
	default:
		sort.Strings(hits)
		return "", &ErrAmbiguous{Name: userPath, Candidates: hits}
	}
}

// ListMarkdown walks subdir (relative to Root, or "" for whole vault) and
// returns every .md file as a sorted vault-relative path.
// Hidden directories, .obsidian, and .trash are skipped.
func (v *Vault) ListMarkdown(subdir string) ([]string, error) {
	root, err := security.ResolveVaultPath(v.Root, subdir)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", root)
	}

	var out []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if skipDir(d.Name(), p, v.Root) {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".md") {
			rel, _ := filepath.Rel(v.Root, p)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// ReadNote reads a note by exact relative path or bare filename, validates its
// .md extension and size, and returns its raw content plus the vault-relative
// path it actually resolved to. Returning rel lets callers (and MCP clients)
// see which file a bare filename / wikilink resolved to — critical so a
// subsequent write or delete targets the same note rather than re-triggering
// wikilink resolution against a different file.
func (v *Vault) ReadNote(userPath string) (content string, rel string, err error) {
	abs, err := v.Resolve(userPath)
	if err != nil {
		return "", "", err
	}
	if err := security.RequireMarkdown(abs); err != nil {
		return "", "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", "", err
	}
	if err := security.CheckFileSize(info.Size(), config.MaxFileSize); err != nil {
		return "", "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", "", err
	}
	rel, _ = filepath.Rel(v.Root, abs)
	return string(data), rel, nil
}

// WriteNote creates or overwrites a note. Uses tmp+rename for atomicity so a
// process kill mid-write can't leave half-written content. Rejects non-.md
// paths and traversal.
func (v *Vault) WriteNote(userPath, content string) (string, error) {
	if err := security.RequireMarkdown(userPath); err != nil {
		return "", err
	}
	abs, err := security.ResolveVaultPath(v.Root, userPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}

	clean := security.SanitizeContent(content)

	tmp, err := os.CreateTemp(filepath.Dir(abs), ".omcp-tmp-*.md")
	if err != nil {
		return "", err
	}
	if _, err := tmp.WriteString(clean); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), abs); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	rel, _ := filepath.Rel(v.Root, abs)
	return rel, nil
}

// DeleteNote removes a note. Like Write, rejects anything non-.md so a broken
// caller can't wipe out .obsidian/ config by passing an unexpected path.
func (v *Vault) DeleteNote(userPath string) error {
	if err := security.RequireMarkdown(userPath); err != nil {
		return err
	}
	abs, err := security.ResolveVaultPath(v.Root, userPath)
	if err != nil {
		return err
	}
	return os.Remove(abs)
}

// skipDir decides whether WalkDir should recurse into a directory. Matches JS
// behavior: skip any dotfile dir plus explicit .obsidian and .trash. The root
// itself is never skipped even if its own name starts with a dot.
func skipDir(name, path, root string) bool {
	if path == root {
		return false
	}
	if name == ".obsidian" || name == ".trash" {
		return true
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	return false
}
