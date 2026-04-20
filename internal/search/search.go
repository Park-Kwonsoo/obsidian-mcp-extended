package search

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"obsidian-mcp/internal/config"
	"obsidian-mcp/internal/metadata"
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
		_, line, ok := FirstH1(lines)
		if !ok {
			return nil
		}
		idx := line - 1
		m := Match{Line: line, Content: strings.TrimSpace(lines[idx])}
		if opts.IncludeContext {
			ctx := BuildContext(lines, idx, opts.ContextLines,
				CompileHighlighter(expr.Value, opts.CaseSensitive))
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

	// Pre-compile one highlighter per positive term. Without this cache a
	// document with N matching lines recompiled the same regex N times —
	// see simplify review for the efficiency trace. Nil entry is fine;
	// HighlightWith is nil-safe.
	var highlighters map[string]*regexp.Regexp
	if opts.IncludeContext {
		highlighters = make(map[string]*regexp.Regexp, len(terms))
		for _, t := range terms {
			highlighters[t] = CompileHighlighter(t, opts.CaseSensitive)
		}
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
			ctx := BuildContext(lines, i, opts.ContextLines, highlighters[hitTerm])
			m.Context = &ctx
		}
		out = append(out, m)
	}
	return out
}

// extractDocMeta returns the first H1 title and the full set of tags for a
// note. Delegates to FirstH1 and metadata.ExtractFrontmatterTags rather than
// parsing inline — what used to be a hand-rolled YAML walker here drifted
// out of parity with the search-by-tags parser twice in a row, so now the
// two tools call the same code.
func extractDocMeta(content string) (title string, tags []string) {
	lines := strings.Split(content, "\n")
	title, _, _ = FirstH1(lines)
	return title, metadata.ExtractFrontmatterTags(content)
}

