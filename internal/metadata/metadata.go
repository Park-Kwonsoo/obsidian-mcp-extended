// Package metadata extracts tags, wikilinks, and other note-level facts from
// raw markdown content. The logic is deliberately string-based (not a proper
// YAML/markdown parser) because Obsidian notes don't guarantee conformance
// and the JS server relies on the same permissive matching — we match that
// behavior byte-for-byte to keep tag/backlink search results stable.
package metadata

import (
	"regexp"
	"strings"
)

// ─── Frontmatter ──────────────────────────────────────────────────────

// frontmatterRe grabs the YAML block at the very top of a note. Captures
// group 1 is the frontmatter body without the surrounding `---` lines.
var frontmatterRe = regexp.MustCompile(`(?s)\A---\n(.*?)\n---`)

// ExtractFrontmatter returns the text between the leading `---` and the next
// `---`, or "" if the note doesn't open with a frontmatter block.
func ExtractFrontmatter(content string) string {
	m := frontmatterRe.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	return m[1]
}

// ─── Tags ─────────────────────────────────────────────────────────────

// Three matchers cover the shapes Obsidian accepts for frontmatter tags:
//
//	tags: [foo, bar]          (inline array)
//	tags:\n  - foo\n  - bar   (YAML list)
//	tags: foo                 (single scalar)
//
// Tried in that order; first hit wins. Kept as package-level vars so they're
// compiled once at init rather than per-call (extractTags runs on every note
// during search).
var (
	fmArrayRe  = regexp.MustCompile(`tags:\s*\[(.*?)\]`)
	fmYAMLRe   = regexp.MustCompile(`(?m)tags:\s*\n((?:\s*-\s*.+\n?)+)`)
	fmSingleRe = regexp.MustCompile(`(?m)^tags:\s*(\S.*)$`)

	// Match `#tag` not preceded by another `#` or a word char — avoids hash-
	// anchored URLs and heading markers (`## Section`). Tags allow letters,
	// digits, `_`, `-`, `+`, `.`, `/`. Trailing dots are stripped in post.
	//
	// NOTE: we intentionally do *not* reject CSS-color-like tokens (`#fff`,
	// `#e6e6e6`). The JS reference parser treats them as tags, the Obsidian
	// CLI treats them as tags, and user notes often use `#0x…`-style codes as
	// legitimate categorization. A "no-hex" filter would silently drop real
	// tags and diverge from the reference shape the MCP client sees today.
	inlineTagRe = regexp.MustCompile(`(?:^|[^#\w])#([a-zA-Z0-9][a-zA-Z0-9_+./\-]*)`)

	// Strip fenced code blocks before scanning for inline tags — otherwise a
	// JSON sample with `"#fff"` or a CSS snippet with `#header` floods results.
	codeBlockRe = regexp.MustCompile("(?s)```.*?```")
)

// removeCodeBlocks strips ```...``` fenced blocks. Inline-code backticks are
// left alone; the JS version matches only fenced blocks too.
func removeCodeBlocks(content string) string {
	return codeBlockRe.ReplaceAllString(content, "")
}

// ExtractFrontmatterTags parses `tags:` out of the frontmatter. Returns nil
// (not empty slice) when there's no frontmatter at all.
func ExtractFrontmatterTags(content string) []string {
	fm := ExtractFrontmatter(content)
	if fm == "" {
		return nil
	}

	if m := fmArrayRe.FindStringSubmatch(fm); m != nil {
		return cleanTagList(strings.Split(m[1], ","))
	}
	if m := fmYAMLRe.FindStringSubmatch(fm); m != nil {
		var out []string
		for _, line := range strings.Split(m[1], "\n") {
			t := strings.TrimSpace(line)
			if t == "" {
				continue
			}
			t = strings.TrimPrefix(t, "-")
			t = strings.TrimSpace(t)
			t = strings.Trim(t, `"'`)
			if t != "" {
				out = append(out, t)
			}
		}
		return out
	}
	if m := fmSingleRe.FindStringSubmatch(fm); m != nil {
		t := strings.TrimSpace(m[1])
		t = strings.Trim(t, `"'`)
		if t != "" {
			return []string{t}
		}
	}
	return nil
}

// ExtractInlineTags returns every `#tag` in the note body. Dedupe is the
// caller's job — ExtractTags does that at the merge layer.
func ExtractInlineTags(content string) []string {
	stripped := removeCodeBlocks(content)
	var out []string
	for _, m := range inlineTagRe.FindAllStringSubmatch(stripped, -1) {
		tag := strings.TrimRight(m[1], ".")
		if tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// ExtractTags merges frontmatter + inline tags, dedupes preserving first-seen
// order, and returns the unified set. This is the one function search-by-tags
// calls per note.
func ExtractTags(content string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(t string) {
		if t == "" {
			return
		}
		if _, ok := seen[t]; ok {
			return
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	for _, t := range ExtractFrontmatterTags(content) {
		add(t)
	}
	for _, t := range ExtractInlineTags(content) {
		add(t)
	}
	return out
}

// HasAllTags returns true when noteTags contains every entry in wanted. The
// AND semantics are intentional — search-by-tags has always been intersection,
// not union.
func HasAllTags(noteTags, wanted []string, caseSensitive bool) bool {
	if len(wanted) == 0 {
		return true
	}
	normalize := func(s string) string {
		if caseSensitive {
			return s
		}
		return strings.ToLower(s)
	}
	have := make(map[string]struct{}, len(noteTags))
	for _, t := range noteTags {
		have[normalize(t)] = struct{}{}
	}
	for _, w := range wanted {
		if _, ok := have[normalize(w)]; !ok {
			return false
		}
	}
	return true
}

// ─── Wikilinks ────────────────────────────────────────────────────────

// `[[Target]]`, `[[Target|alias]]`, `[[folder/Target]]`. Alias text is dropped
// on the way out — discover-mocs and backlinks care about the target, not the
// display text.
var wikilinkRe = regexp.MustCompile(`\[\[([^\]|]+)(?:\|[^\]]+)?\]\]`)

// ExtractWikilinks returns the unique set of target names in note order.
func ExtractWikilinks(content string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, m := range wikilinkRe.FindAllStringSubmatch(content, -1) {
		target := strings.TrimSpace(m[1])
		if target == "" {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		out = append(out, target)
	}
	return out
}

// IsMoc flags a note as a Map of Content when any of its tags (frontmatter or
// inline) is `moc` case-insensitive. Matches links.js behavior.
func IsMoc(tags []string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, "moc") {
			return true
		}
	}
	return false
}

// cleanTagList is the inline-array path's splitter: trims, strips quotes,
// drops empties. Pulled out so ExtractFrontmatterTags reads linearly.
func cleanTagList(parts []string) []string {
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
