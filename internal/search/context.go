package search

import (
	"regexp"
	"strings"
)

// ContextLine is one line in a context window around a match. Number is
// 1-based (matching how users see line numbers in editors); IsMatch flags the
// line that actually hit the query so clients can render it differently.
type ContextLine struct {
	Number  int    `json:"number"`
	Text    string `json:"text"`
	IsMatch bool   `json:"isMatch"`
}

// Context wraps the lines around a match plus a pre-highlighted rendering of
// the match line itself (with search terms wrapped in `**…**`).
type Context struct {
	Lines       []ContextLine `json:"lines"`
	Highlighted string        `json:"highlighted"`
}

// maxContextLineLength caps individual line text so a context block doesn't
// blow up when a note has a very long minified line. 150 matches the JS cap.
const maxContextLineLength = 150

// ExtractContext returns (contextSize × 2 + 1) lines centered on matchIdx
// (clamped to file boundaries) and the 1-based start index the slice covers.
// Split out so title/tag searches can reuse it for their own context blocks.
func ExtractContext(lines []string, matchIdx, contextSize int) (out []string, startIdx, relIdx int) {
	if len(lines) == 0 {
		return nil, 0, 0
	}
	start := max(matchIdx-contextSize, 0)
	end := min(matchIdx+contextSize, len(lines)-1)
	return lines[start : end+1], start, matchIdx - start
}

// BuildContext composes a Context around a match using a pre-compiled
// highlighter regex. Pre-compilation is a per-search optimization: the same
// query term matches every result line, so recompiling per match was pure
// waste. Pass nil highlighter to skip highlighting (useful for tools that
// don't render the `**…**` convention).
func BuildContext(lines []string, matchIdx, contextSize int, highlighter *regexp.Regexp) Context {
	slice, startIdx, relIdx := ExtractContext(lines, matchIdx, contextSize)

	out := make([]ContextLine, len(slice))
	for i, ln := range slice {
		text := ln
		if len(text) > maxContextLineLength {
			text = text[:maxContextLineLength] + "..."
		}
		out[i] = ContextLine{
			Number:  startIdx + i + 1,
			Text:    text,
			IsMatch: i == relIdx,
		}
	}

	matchLine := ""
	if relIdx < len(slice) {
		matchLine = slice[relIdx]
	}

	return Context{
		Lines:       out,
		Highlighted: HighlightWith(matchLine, highlighter),
	}
}

// CompileHighlighter turns a bare search term into a regex that matches
// every occurrence in a line. Returns nil when term is empty — BuildContext
// and HighlightWith treat nil as "skip highlight". The nil-safe return
// keeps hot-path callers from having to branch on empty terms themselves.
func CompileHighlighter(term string, caseSensitive bool) *regexp.Regexp {
	if term == "" {
		return nil
	}
	pattern := regexp.QuoteMeta(term)
	if !caseSensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	return re
}

// HighlightWith wraps every match of the highlighter regex with `**…**`.
// Returns line unchanged on nil regex or empty line. The `**` convention
// is preserved from the JS server so existing MCP clients render
// identically.
func HighlightWith(line string, highlighter *regexp.Regexp) string {
	if line == "" || highlighter == nil {
		return line
	}
	return highlighter.ReplaceAllStringFunc(line, func(m string) string {
		return "**" + m + "**"
	})
}

// Highlight is a convenience wrapper that compiles and applies in one call.
// Hot-path callers (SearchVault match loop) should use CompileHighlighter +
// HighlightWith instead to avoid recompiling the same regex per match.
func Highlight(line, term string, caseSensitive bool) string {
	return HighlightWith(line, CompileHighlighter(term, caseSensitive))
}

// ExtractSnippet pulls snippetRadius characters each side of matchPos from
// line, ellipsis-prefixing/suffixing if the snippet is clipped. Used where a
// one-line preview is more useful than full context.
func ExtractSnippet(line string, matchPos, snippetRadius int) string {
	if line == "" {
		return ""
	}
	start := max(matchPos-snippetRadius, 0)
	end := min(matchPos+snippetRadius, len(line))
	out := line[start:end]
	var b strings.Builder
	if start > 0 {
		b.WriteString("...")
	}
	b.WriteString(out)
	if end < len(line) {
		b.WriteString("...")
	}
	return b.String()
}
