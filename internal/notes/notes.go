// Package notes implements the fs-native tools that don't fit cleanly into
// search (discover-mocs, get-note-metadata, read-section, patch-note,
// toggle-checkbox). They all read or mutate a single note and don't depend
// on the Obsidian app.
package notes

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"obsidian-mcp/internal/metadata"
	"obsidian-mcp/internal/search"
	"obsidian-mcp/internal/vault"
)

// ─── get-note-metadata ────────────────────────────────────────────────

// Metadata is the lightweight header view a client uses to decide whether to
// open a note. Previews are truncated to MaxPreviewLen so a batch metadata
// fetch over thousands of notes doesn't blow up response size.
type Metadata struct {
	Path     string   `json:"path"`
	Title    string   `json:"title,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Preview  string   `json:"preview,omitempty"`
	IsMoc    bool     `json:"isMoc"`
	Wikilinks []string `json:"wikilinks,omitempty"`
}

// MaxPreviewLen bounds how much of a note body shows up in a metadata
// response. Generous enough to show one short paragraph, small enough that
// a batch of 1000 notes still fits comfortably in an MCP response.
const MaxPreviewLen = 200

// GetMetadata extracts frontmatter/title/tags/preview for one note. Uses
// vault.Vault so wikilink-style resolution works on bare filenames.
func GetMetadata(v *vault.Vault, path string) (Metadata, error) {
	content, rel, err := v.ReadNote(path)
	if err != nil {
		return Metadata{}, err
	}
	return metadataFromContent(rel, content), nil
}

// GetAllMetadata is the batch form of GetMetadata. Used for dashboards and
// index-building. Skips notes that can't be read rather than aborting; a
// single unreadable file shouldn't hide 999 others.
func GetAllMetadata(v *vault.Vault, subdir string) ([]Metadata, error) {
	files, err := v.ListMarkdown(subdir)
	if err != nil {
		return nil, err
	}
	out := make([]Metadata, 0, len(files))
	for _, rel := range files {
		content, _, err := v.ReadNote(rel)
		if err != nil {
			continue
		}
		out = append(out, metadataFromContent(rel, content))
	}
	return out, nil
}

func metadataFromContent(rel, content string) Metadata {
	tags := metadata.ExtractTags(content)
	return Metadata{
		Path:      rel,
		Title:     firstH1(content),
		Tags:      tags,
		Preview:   preview(content),
		IsMoc:     metadata.IsMoc(tags),
		Wikilinks: metadata.ExtractWikilinks(content),
	}
}

// firstH1 returns the first `# Title` text from content, or "" if absent.
// Thin delegation to search.FirstH1 so the "what counts as the note title"
// rule lives in exactly one place.
func firstH1(content string) string {
	title, _, _ := search.FirstH1(strings.Split(content, "\n"))
	return title
}

// preview returns the first up-to-MaxPreviewLen chars of content with
// frontmatter stripped. Truncated output carries a trailing "…" so clients
// can tell it was clipped.
func preview(content string) string {
	body := content
	if fm := metadata.ExtractFrontmatter(content); fm != "" {
		// Skip past the closing ---.
		if idx := strings.Index(body, "\n---\n"); idx >= 0 {
			body = body[idx+len("\n---\n"):]
		}
	}
	body = strings.TrimSpace(body)
	if len(body) <= MaxPreviewLen {
		return body
	}
	return body[:MaxPreviewLen] + "…"
}

// ─── discover-mocs ───────────────────────────────────────────────────

// MOC is one "Map of Content" — an index note tagged with #moc that links to
// a cluster of related notes. ChildMOCs are the subset of Links that
// themselves are MOC notes, giving clients a hierarchy to render.
type MOC struct {
	Path       string   `json:"path"`
	Title      string   `json:"title,omitempty"`
	Links      []string `json:"links"`
	ChildMOCs  []string `json:"childMocs,omitempty"`
	LinkCount  int      `json:"linkCount"`
}

// DiscoverMOCs walks the vault and returns every note tagged #moc with its
// wikilink targets plus any child-MOC relationships. Optional subdir scopes
// the walk; nameFilter (case-insensitive substring) narrows the result set.
func DiscoverMOCs(v *vault.Vault, subdir, nameFilter string) ([]MOC, error) {
	metas, err := GetAllMetadata(v, subdir)
	if err != nil {
		return nil, err
	}
	// Build path→is-MOC map so we can flag child MOCs in a second pass.
	// Both full relative paths and basenames are candidates for matching
	// because wikilinks typically drop the extension and directory.
	mocPaths := map[string]bool{}
	mocByBase := map[string]string{}
	for _, m := range metas {
		if !m.IsMoc {
			continue
		}
		mocPaths[m.Path] = true
		base := strings.TrimSuffix(pathBase(m.Path), ".md")
		mocByBase[strings.ToLower(base)] = m.Path
	}

	lowerFilter := strings.ToLower(nameFilter)
	out := make([]MOC, 0, len(mocPaths))
	for _, m := range metas {
		if !m.IsMoc {
			continue
		}
		if lowerFilter != "" && !strings.Contains(strings.ToLower(m.Path), lowerFilter) &&
			!strings.Contains(strings.ToLower(m.Title), lowerFilter) {
			continue
		}
		var children []string
		for _, link := range m.Wikilinks {
			if _, ok := mocByBase[strings.ToLower(strings.TrimSuffix(pathBase(link), ".md"))]; ok {
				children = append(children, link)
			}
		}
		out = append(out, MOC{
			Path:      m.Path,
			Title:     m.Title,
			Links:     m.Wikilinks,
			ChildMOCs: children,
			LinkCount: len(m.Wikilinks),
		})
	}
	// Most-linked first — MOCs with the biggest reach are usually the ones
	// worth showing first when an LLM wants to orient itself.
	sort.Slice(out, func(i, j int) bool { return out[i].LinkCount > out[j].LinkCount })
	return out, nil
}

func pathBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// ─── read-section ────────────────────────────────────────────────────

// Section is a named slice of a note identified by heading. Text is the
// section body without the heading line; Heading carries the original
// heading string (useful when the caller passed a partial/approximate
// match and wants to know what was actually returned).
type Section struct {
	Path    string `json:"path"`
	Heading string `json:"heading"`
	Level   int    `json:"level"`
	Text    string `json:"text"`
}

// ErrSectionNotFound — the requested heading text didn't match any section.
var ErrSectionNotFound = errors.New("section not found")

// emojiSuffix strips leading emoji so "## 📝 Logs" matches a query for
// "Logs" — ported from the same workaround the JS server needed after
// the vault adopted emoji-prefixed folders.
var emojiSuffix = regexp.MustCompile(`^[\p{So}\p{Sk}\p{C}\p{Zs}]+`)

// ReadSection returns the section whose heading (any level, #…######)
// contains `heading` case-insensitively. The returned Text spans from the
// line after the heading up to (but not including) the next heading of
// equal-or-shallower level, matching the way Obsidian's outline panel
// scopes sections.
func ReadSection(v *vault.Vault, path, heading string) (Section, error) {
	content, rel, err := v.ReadNote(path)
	if err != nil {
		return Section{}, err
	}
	lines := strings.Split(content, "\n")
	target := strings.ToLower(strings.TrimSpace(heading))

	for i, ln := range lines {
		lvl, text := headingLevel(ln)
		if lvl == 0 {
			continue
		}
		probe := strings.ToLower(emojiSuffix.ReplaceAllString(text, ""))
		if !strings.Contains(probe, target) {
			continue
		}
		// Find the end — next heading of level ≤ lvl.
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if nlvl, _ := headingLevel(lines[j]); nlvl > 0 && nlvl <= lvl {
				end = j
				break
			}
		}
		body := strings.TrimSpace(strings.Join(lines[i+1:end], "\n"))
		return Section{Path: rel, Heading: text, Level: lvl, Text: body}, nil
	}
	return Section{Path: rel}, ErrSectionNotFound
}

// headingLevel returns the count of leading `#` followed by a space, or 0
// when line isn't a heading. Heading text (after the #s and trim) is the
// second return.
func headingLevel(line string) (int, string) {
	t := strings.TrimLeft(line, " \t")
	n := 0
	for n < len(t) && t[n] == '#' {
		n++
	}
	if n == 0 || n > 6 {
		return 0, ""
	}
	if n >= len(t) || t[n] != ' ' {
		return 0, ""
	}
	return n, strings.TrimSpace(t[n+1:])
}

// ─── patch-note ──────────────────────────────────────────────────────

// PatchResult summarizes a find/replace operation. Matches is how many
// substitutions were applied. Zero is not an error — callers decide whether
// a no-op patch is surprising enough to surface.
type PatchResult struct {
	Path    string `json:"path"`
	Matches int    `json:"matches"`
}

// PatchNote replaces `oldString` with `newString` inside the named note.
// When replaceAll is false, the first occurrence is replaced; otherwise
// every occurrence. An unmodified file is still written (no-op) because
// the cost is negligible and it keeps the code path symmetric.
func PatchNote(v *vault.Vault, path, oldString, newString string, replaceAll bool) (PatchResult, error) {
	if oldString == "" {
		return PatchResult{}, errors.New("oldString must be non-empty")
	}
	content, rel, err := v.ReadNote(path)
	if err != nil {
		return PatchResult{}, err
	}
	var next string
	var n int
	if replaceAll {
		n = strings.Count(content, oldString)
		next = strings.ReplaceAll(content, oldString, newString)
	} else {
		if idx := strings.Index(content, oldString); idx >= 0 {
			next = content[:idx] + newString + content[idx+len(oldString):]
			n = 1
		} else {
			next = content
		}
	}
	if n == 0 {
		return PatchResult{Path: rel, Matches: 0}, nil
	}
	if _, err := v.WriteNote(rel, next); err != nil {
		return PatchResult{}, fmt.Errorf("write patched note: %w", err)
	}
	return PatchResult{Path: rel, Matches: n}, nil
}

// ─── toggle-checkbox ─────────────────────────────────────────────────

// ToggleResult describes the outcome of toggle-checkbox. Found is true when
// the matching task was located and flipped; false means the text didn't
// match any `- [ ]` / `- [x]` line in the note.
type ToggleResult struct {
	Path    string `json:"path"`
	Text    string `json:"text"`
	Checked bool   `json:"checked"`
	Found   bool   `json:"found"`
}

// checkboxLineRe matches an Obsidian-style task line with any indentation.
// The capture is the body text after the brackets so we can match it
// against the caller's query.
var checkboxLineRe = regexp.MustCompile(`^(\s*-\s*\[)([ xX])(\]\s+)(.*)$`)

// ToggleCheckbox sets the checked state of a task line whose body contains
// `text` (trimmed, case-insensitive). Idempotent — toggling to the state
// it's already in is a no-op.
func ToggleCheckbox(v *vault.Vault, path, text string, checked bool) (ToggleResult, error) {
	if text == "" {
		return ToggleResult{}, errors.New("text must be non-empty")
	}
	content, rel, err := v.ReadNote(path)
	if err != nil {
		return ToggleResult{}, err
	}
	lines := strings.Split(content, "\n")
	target := strings.ToLower(strings.TrimSpace(text))
	for i, ln := range lines {
		m := checkboxLineRe.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		body := strings.TrimSpace(m[4])
		if !strings.Contains(strings.ToLower(body), target) {
			continue
		}
		mark := " "
		if checked {
			mark = "x"
		}
		lines[i] = m[1] + mark + m[3] + m[4]
		next := strings.Join(lines, "\n")
		if next != content {
			if _, err := v.WriteNote(rel, next); err != nil {
				return ToggleResult{}, err
			}
		}
		return ToggleResult{Path: rel, Text: body, Checked: checked, Found: true}, nil
	}
	return ToggleResult{Path: rel, Text: text, Found: false}, nil
}
