package search

import (
	"os"
	"path/filepath"
	"strings"

	"obsidian-mcp/internal/config"
	"obsidian-mcp/internal/metadata"
	"obsidian-mcp/internal/vault"
)

// TagHit is one note that matched all requested tags.
type TagHit struct {
	Path string   `json:"path"`
	Tags []string `json:"tags"`
}

// TagResults is the shape returned by search-by-tags. Tags queried are echoed
// back so the client can re-display the query without reparsing it.
type TagResults struct {
	Notes []TagHit `json:"notes"`
	Count int      `json:"count"`
}

// SearchByTags walks the vault (or Subdir) and returns notes that contain
// *all* of wanted (AND intersection), matching the JS search-by-tags contract.
// Each requested tag may include a leading `#` — it's stripped here so callers
// can hand through whatever the user typed.
func SearchByTags(v *vault.Vault, wanted []string, subdir string, caseSensitive bool) (TagResults, error) {
	// Normalize wanted tags up-front so we don't do this work inside the loop.
	normalized := make([]string, 0, len(wanted))
	for _, t := range wanted {
		t = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "#"))
		if t != "" {
			normalized = append(normalized, t)
		}
	}
	if len(normalized) == 0 {
		return TagResults{Notes: []TagHit{}}, nil
	}

	files, err := v.ListMarkdown(subdir)
	if err != nil {
		return TagResults{}, err
	}

	out := make([]TagHit, 0, 16)
	for _, rel := range files {
		abs := filepath.Join(v.Root, rel)

		// Bound per-note cost: skip files over the size cap just like search-vault.
		info, err := os.Stat(abs)
		if err != nil || info.Size() > config.MaxFileSize {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		tags := metadata.ExtractTags(string(data))
		if !metadata.HasAllTags(tags, normalized, caseSensitive) {
			continue
		}
		out = append(out, TagHit{Path: rel, Tags: tags})
	}
	return TagResults{Notes: out, Count: len(out)}, nil
}
