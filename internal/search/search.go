package search

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"obsidian-mcp/internal/config"
	"obsidian-mcp/internal/vault"
)

// Match is one matched line in a note.
type Match struct {
	Line    int      `json:"line"`
	Content string   `json:"content"`
	Context *Context `json:"context,omitempty"`
}

// FileMatches groups all matches for one note.
type FileMatches struct {
	Path       string  `json:"path"`
	MatchCount int     `json:"matchCount"`
	Matches    []Match `json:"matches"`
}

// VaultResults is the response shape for search-vault, mirroring what the JS
// server emits so existing MCP clients don't need to change.
type VaultResults struct {
	Files         []FileMatches `json:"files"`
	TotalMatches  int           `json:"totalMatches"`
	FileCount     int           `json:"fileCount"`
	FilesSearched int           `json:"filesSearched"`
}

// VaultOpts controls search behavior. Zero values give the JS defaults:
// case-insensitive, context disabled, context=2 lines on each side if enabled.
type VaultOpts struct {
	CaseSensitive  bool
	IncludeContext bool
	ContextLines   int
	Subdir         string // "" = whole vault
}

// SearchVault runs a search-vault query against every .md under vault Subdir.
// The query can be a plain substring, a boolean expression (AND / OR / NOT),
// or a field-scoped term (title:x, tag:y, content:z). Match detection uses
// the expression evaluator for correctness; match *reporting* (which lines to
// return) uses the set of positive terms from the expression so output stays
// intuitive — users see the lines that contain what they asked for, not
// entire documents.
func SearchVault(v *vault.Vault, query string, opts VaultOpts) (VaultResults, error) {
	expr, err := ParseQuery(query)
	if err != nil {
		return VaultResults{}, err
	}

	files, err := v.ListMarkdown(opts.Subdir)
	if err != nil {
		return VaultResults{}, err
	}

	// Deliberately no coercion of ContextLines here. The MCP handler resolves
	// "user didn't pass contextLines" → default 2 before calling us, so a value
	// of 0 on this struct means "caller explicitly wants match-only output" and
	// must pass through untouched. Flattening 0 → default here would silently
	// violate the documented 0-10 range.

	var all []FileMatches
	totalMatches := 0

	for _, rel := range files {
		abs := filepath.Join(v.Root, rel)

		info, err := os.Stat(abs)
		if err != nil || info.Size() > config.MaxFileSize {
			continue // skip unreadable or too-large notes, same as JS
		}

		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		content := string(data)

		// Extract title + tags lazily — only needed if the expression
		// touches those fields, but cheap enough to always compute.
		title, tags := extractDocMeta(content)
		if expr != nil && !Evaluate(expr, content, DocMeta{Title: title, Tags: tags}, opts.CaseSensitive) {
			continue
		}

		matches := findMatches(content, expr, query, opts)
		if len(matches) == 0 {
			continue
		}

		all = append(all, FileMatches{
			Path:       rel,
			MatchCount: len(matches),
			Matches:    matches,
		})
		totalMatches += len(matches)
	}

	return VaultResults{
		Files:         all,
		TotalMatches:  totalMatches,
		FileCount:     len(all),
		FilesSearched: len(files),
	}, nil
}

// findMatches picks which lines to report for a document that Evaluate already
// said matched. The strategy tracks JS search.js:
//
//   - empty expression → every line (query was blank)
//   - pure title:X     → only the H1 line
//   - pure tag:X       → a synthetic one-line "matches tag" summary
//   - everything else  → every line that contains any positive term
func findMatches(content string, expr *Expr, _ string, opts VaultOpts) []Match {
	lines := strings.Split(content, "\n")

	if expr == nil {
		out := make([]Match, len(lines))
		for i, ln := range lines {
			out[i] = Match{Line: i + 1, Content: strings.TrimSpace(ln)}
		}
		return out
	}

	// Pure field: single-node expression of that kind, no AND/OR/NOT.
	if expr.Type == NodeField && expr.Field == "title" {
		idx := findH1Index(lines)
		if idx < 0 {
			return nil
		}
		m := Match{Line: idx + 1, Content: strings.TrimSpace(lines[idx])}
		if opts.IncludeContext {
			ctx := BuildContext(lines, idx, opts.ContextLines, expr.Value)
			m.Context = &ctx
		}
		return []Match{m}
	}
	if expr.Type == NodeField && expr.Field == "tag" {
		return []Match{{Line: 1, Content: "[Document matches tag: " + expr.Value + "]"}}
	}

	terms := PositiveTerms(expr)
	if len(terms) == 0 {
		return nil
	}
	termsLower := make([]string, len(terms))
	for i, t := range terms {
		termsLower[i] = strings.ToLower(t)
	}

	out := make([]Match, 0, 8)
	for i, ln := range lines {
		probe := ln
		if !opts.CaseSensitive {
			probe = strings.ToLower(probe)
		}
		hitTerm := ""
		for j, t := range terms {
			candidate := t
			if !opts.CaseSensitive {
				candidate = termsLower[j]
			}
			if strings.Contains(probe, candidate) {
				hitTerm = t
				break
			}
		}
		if hitTerm == "" {
			continue
		}
		m := Match{Line: i + 1, Content: strings.TrimSpace(ln)}
		if opts.IncludeContext {
			ctx := BuildContext(lines, i, opts.ContextLines, hitTerm)
			m.Context = &ctx
		}
		out = append(out, m)
	}
	return out
}

// findH1Index returns the 0-based index of the first `# heading` line, or -1.
func findH1Index(lines []string) int {
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if len(t) >= 2 && t[0] == '#' && t[1] == ' ' {
			return i
		}
	}
	return -1
}

// extractDocMeta is a minimal-cost title+tag extractor used by SearchVault.
// It only scans what's needed and stops once both are resolved — cheap enough
// to run on every document during a content search. The fuller parser used
// by search-by-tags lives in internal/metadata.
//
// Streaming over lines avoids allocating a whole []string when we only need
// the first heading and any line beginning with `tags:`. Both `tags: [a, b]`
// and the YAML-list form (`tags:` followed by `  - a` / `  - b`) are
// recognized so tag-scoped search-vault queries match search-by-tags.
func extractDocMeta(content string) (title string, tags []string) {
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	inFrontmatter := false
	inTagsList := false
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		trimmed := strings.TrimSpace(line)

		if lineNo == 1 && trimmed == "---" {
			inFrontmatter = true
			continue
		}
		if inFrontmatter {
			if trimmed == "---" {
				inFrontmatter = false
				inTagsList = false
				continue
			}
			if inTagsList {
				// YAML list items under `tags:` start with `-`.
				if strings.HasPrefix(trimmed, "-") {
					item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
					item = strings.Trim(item, `"'`)
					if item != "" {
						tags = append(tags, item)
					}
					continue
				}
				// Any non-list line ends the tags block; fall through so
				// this line can itself start a new `tags:` section or be
				// ignored like the rest of the frontmatter.
				inTagsList = false
			}
			if strings.HasPrefix(trimmed, "tags:") {
				rest := strings.TrimSpace(trimmed[len("tags:"):])
				if rest == "" {
					// Bare `tags:` — list items follow on subsequent lines.
					inTagsList = true
				} else {
					tags = append(tags, parseInlineTagList(rest)...)
				}
			}
			continue
		}

		if title == "" && strings.HasPrefix(trimmed, "# ") {
			title = strings.TrimSpace(trimmed[2:])
		}
	}
	return title, tags
}

// parseInlineTagList parses `[foo, bar]` or `foo` style into a slice. For `- foo`
// YAML list form we defer to the richer parser in internal/metadata; this
// inline form is sufficient for the fast-path search meta lookup.
func parseInlineTagList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		s = s[1 : len(s)-1]
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(part)
		p = strings.Trim(p, `"'`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
