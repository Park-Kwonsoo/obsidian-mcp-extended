// MCP handlers for the fs-native tools ported in P3: discover-mocs,
// get-note-metadata, read-section, patch-note, toggle-checkbox. All pure
// Go, no Obsidian-app dependency.
package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"obsidian-mcp/internal/notes"
	"obsidian-mcp/internal/vault"
)

// ─── Argument types ──────────────────────────────────────────────────

type getMetadataArgs struct {
	Path   string `json:"path,omitempty"   jsonschema:"optional — vault-relative path or bare filename. Omit to fetch every note's metadata (batch)."`
	Subdir string `json:"subdir,omitempty" jsonschema:"for batch mode: restrict to a subdirectory"`
}

type discoverMocsArgs struct {
	Subdir string `json:"subdir,omitempty" jsonschema:"optional subdirectory to scan"`
	Filter string `json:"filter,omitempty" jsonschema:"case-insensitive substring filter on MOC path or title"`
}

type readSectionArgs struct {
	Path    string `json:"path"    jsonschema:"note path or bare filename"`
	Heading string `json:"heading" jsonschema:"heading text to find (case-insensitive, substring match)"`
}

type patchNoteArgs struct {
	Path       string `json:"path"                  jsonschema:"note path or bare filename"`
	OldString  string `json:"oldString"             jsonschema:"text to find"`
	NewString  string `json:"newString"             jsonschema:"replacement text"`
	ReplaceAll bool   `json:"replaceAll,omitempty"  jsonschema:"replace every occurrence (default false = first only)"`
}

type toggleCheckboxArgs struct {
	Path    string `json:"path"    jsonschema:"note path or bare filename"`
	Text    string `json:"text"    jsonschema:"case-insensitive substring of the task body"`
	Section string `json:"section,omitempty" jsonschema:"optional heading — restrict search to this section only"`
	Checked bool   `json:"checked" jsonschema:"true to mark done, false to uncheck"`
}

// ─── Registrations ───────────────────────────────────────────────────

func registerNoteTools(server *mcp.Server, v *vault.Vault) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get-note-metadata",
		Description: "Get metadata (frontmatter, title, tags, preview, wikilinks, MOC flag) for one note OR every note in the vault / a subdirectory. Single-note mode when path is set.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, a getMetadataArgs) (*mcp.CallToolResult, any, error) {
		if a.Path != "" {
			meta, err := notes.GetMetadata(v, a.Path)
			if err != nil {
				return nil, nil, err
			}
			return toolResult(fmt.Sprintf("metadata for %s", meta.Path), meta)
		}
		all, err := notes.GetAllMetadata(v, a.Subdir)
		if err != nil {
			return nil, nil, err
		}
		return toolResult(fmt.Sprintf("metadata for %d notes", len(all)),
			map[string]any{"notes": all, "count": len(all)})
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "discover-mocs",
		Description: "List every Map of Content (note tagged #moc) with its linked notes and child-MOC relationships. Start here when exploring an unfamiliar vault — typically 10x faster than blind keyword search.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, a discoverMocsArgs) (*mcp.CallToolResult, any, error) {
		mocs, err := notes.DiscoverMOCs(v, a.Subdir, a.Filter)
		if err != nil {
			return nil, nil, err
		}
		return toolResult(fmt.Sprintf("found %d MOCs", len(mocs)),
			map[string]any{"mocs": mocs, "count": len(mocs)})
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read-section",
		Description: "Read a specific section of a note, delimited by heading. Returns the text between the matching heading and the next heading of equal-or-shallower level.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, a readSectionArgs) (*mcp.CallToolResult, any, error) {
		sec, err := notes.ReadSection(v, a.Path, a.Heading)
		if err != nil {
			if errors.Is(err, notes.ErrSectionNotFound) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("section %q not found in %s", a.Heading, a.Path)}},
					IsError: true,
				}, nil, nil
			}
			return nil, nil, err
		}
		return toolResult(fmt.Sprintf("%s § %s", sec.Path, sec.Heading), sec)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "patch-note",
		Description: "Find-and-replace inside a single note. Set replaceAll=true to replace every occurrence; default is first only. Returns the count of substitutions.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, a patchNoteArgs) (*mcp.CallToolResult, any, error) {
		res, err := notes.PatchNote(v, a.Path, a.OldString, a.NewString, a.ReplaceAll)
		if err != nil {
			return nil, nil, err
		}
		return toolResult(fmt.Sprintf("%d replacement(s) in %s", res.Matches, res.Path), res)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "toggle-checkbox",
		Description: "Toggle the checked state of a task line (`- [ ]` / `- [x]`) inside a note. Matches by case-insensitive substring of the task body. Use section to restrict the search to a specific heading block when the same task text appears in multiple sections.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, a toggleCheckboxArgs) (*mcp.CallToolResult, any, error) {
		res, err := notes.ToggleCheckbox(v, a.Path, a.Text, a.Section, a.Checked)
		if err != nil {
			return nil, nil, err
		}
		if !res.Found {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("no task matching %q in %s", a.Text, res.Path)}},
				IsError: true,
			}, nil, nil
		}
		mark := "unchecked"
		if res.Checked {
			mark = "checked"
		}
		return toolResult(fmt.Sprintf("%s task in %s (%s)", mark, res.Path, res.Text), res)
	})
}
