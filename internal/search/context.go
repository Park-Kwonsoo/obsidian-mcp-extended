package search

import (
	"regexp"
	"strings"
)

// ContextLine is one line in a context window around a match. Number is
// 1-based (matching how users see line numbers in editors); IsMatch flags the
// line that actually hit the query so clients can render it differently.
type ContextLine struct {
	Number int    `json:"number"`
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

// BuildContext composes a Context around a match. Lines longer than
// maxContextLineLength are truncated with "..." — the full file content is
// still available via read-note, so this is purely a display concern.
func BuildContext(lines []string, matchIdx, contextSize int, highlightTerm string) Context {
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
		Highlighted: Highlight(matchLine, highlightTerm, false),
	}
}

// Highlight wraps every occurrence of term in line with `**…**` for markdown
// bold. Returns line unchanged when either side is empty. The `**` convention
// is preserved from the JS server so existing MCP clients render identically.
func Highlight(line, term string, caseSensitive bool) string {
	if line == "" || term == "" {
		return line
	}
	pattern := regexp.QuoteMeta(term)
	if !caseSensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return line
	}
	return re.ReplaceAllStringFunc(line, func(match string) string {
		return "**" + match + "**"
	})
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
