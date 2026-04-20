// Package search implements the title, content, and tag-based lookups
// exposed by the search-by-title, search-vault, and search-by-tags MCP tools.
// Each function reads straight from the Vault — there is no intermediate
// index yet; once the indexer daemon lands in P5 these become thin adapters.
package search

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"obsidian-mcp/internal/vault"
)

// TitleHit is one matching note. File is the vault-relative path so clients
// can feed it straight back into read-note without any further resolution.
// The JSON key is `file` (not `path`) to preserve the JS MCP server's public
// response shape — renaming it would silently break every client that parses
// `file` today.
type TitleHit struct {
	File  string `json:"file"`
	Title string `json:"title"`
	Line  int    `json:"line"`
}

// TitleResults is the shape returned by search-by-title. FilesSearched counts
// every .md the search touched (not every .md with an H1) so that "0 results"
// still carries a useful denominator to the client.
type TitleResults struct {
	Results       []TitleHit `json:"results"`
	Count         int        `json:"count"`
	FilesSearched int        `json:"filesSearched"`
}

// inlineTagSuffix matches any run of #tag tokens at the end of a title, which
// Obsidian displays as metadata and title-search should strip before matching
// so "Setup #draft" is findable by query "Setup".
var inlineTagSuffix = regexp.MustCompile(`\s+#\w+(\s+#\w+)*$`)

// FirstH1 walks lines and returns the title (trimmed, inline-tag suffix
// stripped) and 1-based line number of the first `# ` heading. Only H1 —
// deeper headings are ignored on purpose, matching the legacy JS parser.
// One canonical implementation so title/content/metadata tools can't drift
// on what counts as "the title of a note".
func FirstH1(lines []string) (title string, line int, ok bool) {
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		// Match "# foo" but not "## foo". Explicit length+space check is
		// faster than a regex and this path runs once per note.
		if len(t) >= 2 && t[0] == '#' && t[1] == ' ' {
			raw := strings.TrimSpace(t[2:])
			return inlineTagSuffix.ReplaceAllString(raw, ""), i + 1, true
		}
	}
	return "", 0, false
}

// TitleMatches returns true when title contains query, case-sensitive per flag.
// Exported so other packages can reuse the same "contains" semantics (e.g. a
// future advanced search might layer fuzzy matching on top).
func TitleMatches(title, query string, caseSensitive bool) bool {
	if title == "" || query == "" {
		return false
	}
	if caseSensitive {
		return strings.Contains(title, query)
	}
	return strings.Contains(strings.ToLower(title), strings.ToLower(query))
}

// SearchByTitle walks subdir (or the whole vault when subdir is empty) and
// returns notes whose H1 title contains query. Results are sorted by path for
// stable pagination across requests.
func SearchByTitle(v *vault.Vault, query, subdir string, caseSensitive bool) (TitleResults, error) {
	if query == "" {
		// Caller should have gate-kept on required params; returning empty keeps
		// this function pure rather than coupling it to the MCP error layer.
		return TitleResults{Results: []TitleHit{}}, nil
	}

	files, err := v.ListMarkdown(subdir)
	if err != nil {
		return TitleResults{}, err
	}

	out := make([]TitleHit, 0, 16)
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(v.Root, rel))
		if err != nil {
			continue // unreadable files don't abort the whole search
		}
		title, line, ok := FirstH1(strings.Split(string(data), "\n"))
		if !ok {
			continue
		}
		if !TitleMatches(title, query, caseSensitive) {
			continue
		}
		out = append(out, TitleHit{File: rel, Title: title, Line: line})
	}

	return TitleResults{
		Results:       out,
		Count:         len(out),
		FilesSearched: len(files),
	}, nil
}
