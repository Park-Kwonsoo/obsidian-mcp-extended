// obsidian-mcp is the MCP stdio server for an Obsidian vault. External
// transport is JSON-RPC 2.0 over stdio (the MCP spec); handlers dispatch
// directly into the internal packages for filesystem-backed tools.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"obsidian-mcp/internal/config"
	"obsidian-mcp/internal/obscli"
	"obsidian-mcp/internal/search"
	"obsidian-mcp/internal/vault"
)

// version is stamped at build time via -ldflags "-X main.version=..." from
// the Makefile. "dev" is the fallback when built without ldflags.
var version = "dev"

// Config holds the startup flags. Kept as a struct so the same parser is
// reusable from tests.
type Config struct {
	Vault       string
	ObsidianCLI string // reserved for P2+ Group B tools; validated at startup
}

func parseConfig() Config {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	vaultFlag := fs.String("vault", "", "path to the Obsidian vault (required)")
	cliFlag := fs.String("obsidian-cli", "", "path to the obsidian CLI binary (default: $PATH lookup)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s --vault <path> [--obsidian-cli <path>]\n", os.Args[0])
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])
	if *vaultFlag == "" && fs.NArg() > 0 {
		*vaultFlag = fs.Arg(0)
	}
	if *vaultFlag == "" {
		fs.Usage()
		os.Exit(2)
	}

	abs, err := filepath.Abs(*vaultFlag)
	if err != nil {
		log.Fatalf("resolve vault: %v", err)
	}
	cfg := Config{Vault: abs, ObsidianCLI: *cliFlag}
	if cfg.ObsidianCLI != "" {
		if info, err := os.Stat(cfg.ObsidianCLI); err != nil || info.IsDir() {
			log.Fatalf("obsidian-cli not a file: %s", cfg.ObsidianCLI)
		}
	}
	return cfg
}

// ─── Tool argument types ─────────────────────────────────────────────
// Struct tags drive the JSON schema the SDK advertises to MCP clients.
// `jsonschema` descriptions matter — they show up in Claude Desktop's tool picker.

type listNotesArgs struct {
	Directory string `json:"directory,omitempty" jsonschema:"optional subdirectory relative to vault root"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max notes to return (default 100)"`
	Offset    int    `json:"offset,omitempty" jsonschema:"pagination offset (default 0)"`
}

type readNoteArgs struct {
	Path string `json:"path" jsonschema:"exact vault-relative path OR bare filename for wikilink-style resolution"`
}

type writeNoteArgs struct {
	Path    string `json:"path"    jsonschema:"vault-relative path for the note (must end in .md)"`
	Content string `json:"content" jsonschema:"full note body; overwrites existing content atomically"`
}

type deleteNoteArgs struct {
	Path string `json:"path" jsonschema:"vault-relative path of the note to delete (must end in .md)"`
}

type searchByTitleArgs struct {
	Query         string `json:"query" jsonschema:"substring to match against H1 title (# Title)"`
	CaseSensitive bool   `json:"caseSensitive,omitempty"`
	Path          string `json:"path,omitempty" jsonschema:"optional subdirectory to limit the search"`
	Limit         int    `json:"limit,omitempty"`
	Offset        int    `json:"offset,omitempty"`
}

type searchVaultArgs struct {
	Query         string `json:"query" jsonschema:"search query; supports AND OR NOT, quoted phrases, field: specifiers, grouping"`
	CaseSensitive bool   `json:"caseSensitive,omitempty"`
	Path          string `json:"path,omitempty"`
	// Pointer so "field omitted" (→ JS-compat default of true) is distinguishable
	// from explicit `false`. A bare bool would collapse both cases to false via
	// Go's zero value and silently drop context snippets for clients that relied
	// on the JS server's implicit default.
	IncludeContext *bool `json:"includeContext,omitempty" jsonschema:"include surrounding lines for each match (default true)"`
	// Same pointer trick for contextLines so an explicit `contextLines: 0`
	// (match-only output) stays distinguishable from "field omitted" — absent
	// stays nil, 0 becomes a non-nil pointer to zero.
	ContextLines *int `json:"contextLines,omitempty" jsonschema:"lines of context before/after each match (0-10, default 2)"`
	Limit        int  `json:"limit,omitempty"`
	Offset       int  `json:"offset,omitempty"`
}

type searchByTagsArgs struct {
	Tags          []string `json:"tags" jsonschema:"one or more tags; intersection (AND) semantics — leading # is optional"`
	CaseSensitive bool     `json:"caseSensitive,omitempty"`
	Path          string   `json:"path,omitempty" jsonschema:"optional subdirectory relative to vault root"`
	// Directory is a backward-compat alias for Path. The original JS
	// search-by-tags tool accepted `directory` (inconsistent with the other
	// search tools' `path`); existing clients still send that field. Dropping
	// it would silently widen their tag filter to the whole vault, so we keep
	// accepting both and prefer Path when the caller sets it explicitly.
	Directory string `json:"directory,omitempty" jsonschema:"deprecated alias for path — prefer path in new clients"`
}

// ─── Helpers ──────────────────────────────────────────────────────────

// toolResult packages arbitrary structured output alongside a short human-
// readable text summary. MCP clients that can't parse structuredContent fall
// back to the text block; those that can still get the rich payload.
func toolResult(text string, data any) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}, data, nil
}

// paginate slices a sorted string list and packages a small pagination struct
// matching the shape the JS server returns.
type pagination struct {
	Total    int  `json:"total"`
	Returned int  `json:"returned"`
	Limit    int  `json:"limit"`
	Offset   int  `json:"offset"`
	HasMore  bool `json:"hasMore"`
}

func paginate(total, limit, offset int) (int, int, pagination) {
	// Enforce the JS server's hard cap: an arbitrarily large `limit` must not
	// be able to blow up a response payload. A missing/non-positive limit
	// falls back to the same cap (matching the JS default of 100).
	if limit <= 0 || limit > config.MaxSearchResults {
		limit = config.MaxSearchResults
	}
	if offset < 0 {
		offset = 0
	}
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return start, end, pagination{
		Total:    total,
		Returned: end - start,
		Limit:    limit,
		Offset:   offset,
		HasMore:  end < total,
	}
}

// ─── Tool handlers ────────────────────────────────────────────────────

func handleListNotes(v *vault.Vault) func(context.Context, *mcp.CallToolRequest, listNotesArgs) (*mcp.CallToolResult, any, error) {
	return func(_ context.Context, _ *mcp.CallToolRequest, args listNotesArgs) (*mcp.CallToolResult, any, error) {
		all, err := v.ListMarkdown(args.Directory)
		if err != nil {
			return nil, nil, err
		}
		s, e, pg := paginate(len(all), args.Limit, args.Offset)
		slice := all[s:e]
		return toolResult(
			fmt.Sprintf("Found %d notes (returned %d)", pg.Total, pg.Returned),
			map[string]any{"notes": slice, "count": pg.Returned, "pagination": pg},
		)
	}
}

func handleReadNote(v *vault.Vault) func(context.Context, *mcp.CallToolRequest, readNoteArgs) (*mcp.CallToolResult, any, error) {
	return func(_ context.Context, _ *mcp.CallToolRequest, args readNoteArgs) (*mcp.CallToolResult, any, error) {
		content, rel, err := v.ReadNote(args.Path)
		if err != nil {
			// Ambiguous resolution is a user-facing case: tell them which candidates matched.
			var amb *vault.ErrAmbiguous
			if errors.As(err, &amb) {
				msg, _ := json.Marshal(map[string]any{"ambiguous": true, "candidates": amb.Candidates})
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: string(msg)}},
					IsError: true,
				}, nil, nil
			}
			return nil, nil, err
		}
		// Return the resolved vault-relative path, not the raw user input —
		// otherwise a bare filename that resolves deep in the tree would leak
		// back as `note.md` and mislead clients into writing to the wrong spot.
		return toolResult(content, map[string]any{"path": rel, "content": content})
	}
}

func handleWriteNote(v *vault.Vault) func(context.Context, *mcp.CallToolRequest, writeNoteArgs) (*mcp.CallToolResult, any, error) {
	return func(_ context.Context, _ *mcp.CallToolRequest, args writeNoteArgs) (*mcp.CallToolResult, any, error) {
		rel, err := v.WriteNote(args.Path, args.Content)
		if err != nil {
			return nil, nil, err
		}
		return toolResult(
			fmt.Sprintf("Wrote %d bytes to %s", len(args.Content), rel),
			map[string]any{"path": rel, "bytes": len(args.Content), "success": true},
		)
	}
}

func handleDeleteNote(v *vault.Vault) func(context.Context, *mcp.CallToolRequest, deleteNoteArgs) (*mcp.CallToolResult, any, error) {
	return func(_ context.Context, _ *mcp.CallToolRequest, args deleteNoteArgs) (*mcp.CallToolResult, any, error) {
		if err := v.DeleteNote(args.Path); err != nil {
			return nil, nil, err
		}
		return toolResult("Deleted "+args.Path, map[string]any{"path": args.Path, "success": true})
	}
}

func handleSearchByTitle(v *vault.Vault) func(context.Context, *mcp.CallToolRequest, searchByTitleArgs) (*mcp.CallToolResult, any, error) {
	return func(_ context.Context, _ *mcp.CallToolRequest, args searchByTitleArgs) (*mcp.CallToolResult, any, error) {
		res, err := search.SearchByTitle(v, args.Query, args.Path, args.CaseSensitive)
		if err != nil {
			return nil, nil, err
		}
		s, e, pg := paginate(res.Count, args.Limit, args.Offset)
		res.Results = res.Results[s:e]
		res.Count = pg.Returned
		return toolResult(
			fmt.Sprintf("Found %d titles (searched %d files)", pg.Total, res.FilesSearched),
			map[string]any{
				"results":       res.Results,
				"count":         res.Count,
				"filesSearched": res.FilesSearched,
				"pagination":    pg,
			},
		)
	}
}

func handleSearchVault(v *vault.Vault) func(context.Context, *mcp.CallToolRequest, searchVaultArgs) (*mcp.CallToolResult, any, error) {
	return func(_ context.Context, _ *mcp.CallToolRequest, args searchVaultArgs) (*mcp.CallToolResult, any, error) {
		// Resolve includeContext and contextLines defaults here (not in the
		// library) so explicit falsy values (`includeContext:false`,
		// `contextLines:0`) are preserved end-to-end while omitted fields pick
		// up the JS-compatible defaults (includeContext=true, contextLines=2).
		includeCtx := true
		if args.IncludeContext != nil {
			includeCtx = *args.IncludeContext
		}
		ctxLines := 2
		if args.ContextLines != nil {
			ctxLines = *args.ContextLines
		}
		res, err := search.SearchVault(v, args.Query, search.VaultOpts{
			CaseSensitive:  args.CaseSensitive,
			IncludeContext: includeCtx,
			ContextLines:   ctxLines,
			Subdir:         args.Path,
		})
		if err != nil {
			return nil, nil, err
		}
		// Paginate on the flattened match stream, not on the file list. The
		// JS server advertised `limit`/`offset` as cursors over individual
		// matches, so clients that already consume this tool expect a page
		// of N matches — not N files, each potentially carrying many matches.
		// Rebuilding the file structure from the paginated match slice lets
		// a single file span page boundaries without duplicating or skipping.
		s, e, pg := paginate(res.TotalMatches, args.Limit, args.Offset)
		pageFiles := sliceMatchRange(res.Files, s, e)
		return toolResult(
			fmt.Sprintf("Found %d matches in %d files for %q (returned %d matches across %d files)", res.TotalMatches, res.FileCount, args.Query, pg.Returned, len(pageFiles)),
			map[string]any{
				"files":         pageFiles,
				"totalMatches":  res.TotalMatches,
				"fileCount":     len(pageFiles),
				"filesSearched": res.FilesSearched,
				"pagination":    pg,
			},
		)
	}
}

// sliceMatchRange returns files with only the matches that fall in the
// flattened-match range [start, end). Preserves the JS pagination contract
// where limit/offset step across individual matches; a file whose matches
// straddle a page boundary appears in both pages with partial slices.
func sliceMatchRange(files []search.FileMatches, start, end int) []search.FileMatches {
	if start >= end {
		return nil
	}
	out := make([]search.FileMatches, 0, len(files))
	idx := 0
	for _, f := range files {
		fileEnd := idx + len(f.Matches)
		if fileEnd <= start {
			idx = fileEnd
			continue
		}
		if idx >= end {
			break
		}
		lo := max(0, start-idx)
		hi := min(len(f.Matches), end-idx)
		page := f.Matches[lo:hi]
		out = append(out, search.FileMatches{
			Path:       f.Path,
			MatchCount: len(page),
			Matches:    page,
		})
		idx = fileEnd
	}
	return out
}

func handleSearchByTags(v *vault.Vault) func(context.Context, *mcp.CallToolRequest, searchByTagsArgs) (*mcp.CallToolResult, any, error) {
	return func(_ context.Context, _ *mcp.CallToolRequest, args searchByTagsArgs) (*mcp.CallToolResult, any, error) {
		// Prefer `path` (current contract) but fall back to the legacy
		// `directory` field so existing clients keep scoping the search.
		subdir := args.Path
		if subdir == "" {
			subdir = args.Directory
		}
		res, err := search.SearchByTags(v, args.Tags, subdir, args.CaseSensitive)
		if err != nil {
			return nil, nil, err
		}
		return toolResult(
			fmt.Sprintf("Found %d notes matching tags %v", res.Count, args.Tags),
			res,
		)
	}
}

// ─── main ─────────────────────────────────────────────────────────────

func main() {
	cfg := parseConfig()
	v, err := vault.Open(cfg.Vault)
	if err != nil {
		log.Fatalf("open vault: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "obsidian-mcp",
		Version: version,
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list-notes",
		Description: "List markdown files in the vault or a subdirectory.",
	}, handleListNotes(v))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read-note",
		Description: "Read a note's full content. Accepts an exact vault-relative path OR a bare filename (Obsidian wikilink resolution).",
	}, handleReadNote(v))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "write-note",
		Description: "Create or overwrite a note atomically. Path must end in .md.",
	}, handleWriteNote(v))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete-note",
		Description: "Delete a note from the vault. Path must end in .md.",
	}, handleDeleteNote(v))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search-by-title",
		Description: "Find notes whose H1 title (# Title) contains the given substring.",
	}, handleSearchByTitle(v))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search-vault",
		Description: "Full-text search across notes. Supports boolean (AND OR NOT), quoted phrases, field scopes (title:, tag:, content:), grouping with parentheses, and context snippets.",
	}, handleSearchVault(v))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search-by-tags",
		Description: "Find notes that contain ALL requested tags (frontmatter or inline). Leading # is optional.",
	}, handleSearchByTags(v))

	// Group B tools (CLI-backed) register only when `obsidian` is reachable.
	// That keeps the tools/list surface honest: if the CLI isn't there, the
	// tools that need it never get advertised.
	ctx := context.Background()
	cliExec := &obscli.Executor{Binary: cfg.ObsidianCLI, VaultPath: cfg.Vault}
	if cliExec.Detect(ctx) {
		registerCLITools(server, cliExec)
	}

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
