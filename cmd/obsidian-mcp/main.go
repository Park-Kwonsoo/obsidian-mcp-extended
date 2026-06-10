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
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"obsidian-mcp/internal/config"
	"obsidian-mcp/internal/obscli"
	"obsidian-mcp/internal/search"
	"obsidian-mcp/internal/vault"
	pb "obsidian-mcp/proto/indexer/v1"
)

// version is stamped at build time via -ldflags "-X main.version=..." from
// the Makefile. "dev" is the fallback when built without ldflags.
var version = "dev"

// Config holds the startup flags. Kept as a struct so the same parser is
// reusable from tests.
type Config struct {
	Vault       string
	ObsidianCLI string // reserved for P2+ Group B tools; validated at startup
	Transport   string
	HTTPHost    string
	HTTPPort    int
	HTTPPath    string
}

func parseConfig() Config {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	vaultFlag := fs.String("vault", "", "path to the Obsidian vault (required)")
	cliFlag := fs.String("obsidian-cli", "", "path to the obsidian CLI binary (default: $PATH lookup)")
	transportFlag := fs.String("transport", "stdio", "transport to serve MCP over: stdio or streamable-http")
	httpHostFlag := fs.String("http-host", "127.0.0.1", "host for streamable-http transport")
	httpPortFlag := fs.Int("http-port", 47610, "port for streamable-http transport")
	httpPathFlag := fs.String("http-path", "/mcp", "path for streamable-http transport")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s --vault <path> [--obsidian-cli <path>] [--transport stdio|streamable-http]\n", os.Args[0])
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
	transport := strings.ToLower(*transportFlag)
	if transport == "http" {
		transport = "streamable-http"
	}
	if transport != "stdio" && transport != "streamable-http" {
		log.Fatalf("unsupported transport %q; use stdio or streamable-http", *transportFlag)
	}

	httpPath := *httpPathFlag
	if httpPath == "" {
		httpPath = "/mcp"
	}
	if !strings.HasPrefix(httpPath, "/") {
		httpPath = "/" + httpPath
	}

	cfg := Config{
		Vault:       abs,
		ObsidianCLI: *cliFlag,
		Transport:   transport,
		HTTPHost:    *httpHostFlag,
		HTTPPort:    *httpPortFlag,
		HTTPPath:    httpPath,
	}
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

func handleListNotes(v *vault.Vault, daemon pb.IndexerServiceClient) func(context.Context, *mcp.CallToolRequest, listNotesArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args listNotesArgs) (*mcp.CallToolResult, any, error) {
		// Daemon path first. `list-notes` is the top beneficiary of the
		// warm cache — on a 10k-note vault it's the difference between a
		// full directory walk and a map lookup.
		if daemon != nil {
			if notes, pg, err := daemonListNotes(ctx, daemon, v.Root, args); err == nil {
				return toolResult(
					fmt.Sprintf("Found %d notes (returned %d)", pg.Total, pg.Returned),
					map[string]any{"notes": notes, "count": pg.Returned, "pagination": pg},
				)
			}
			// Fall through to in-process on any daemon error.
		}
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

// daemonListNotes is the gRPC path for list-notes. Returns the notes slice
// and the same pagination shape the in-process handler produces so the
// caller's response formatting is identical either way.
func daemonListNotes(ctx context.Context, c pb.IndexerServiceClient, vaultRoot string, args listNotesArgs) ([]string, pagination, error) {
	resp, err := c.ListNotes(ctx, &pb.ListNotesRequest{
		VaultPath: vaultRoot,
		Subdir:    args.Directory,
		Limit:     int32(args.Limit),
		Offset:    int32(args.Offset),
	})
	if err != nil {
		return nil, pagination{}, err
	}
	pg := resp.GetPagination()
	return resp.GetNotes(), pagination{
		Total:    int(pg.GetTotal()),
		Returned: int(pg.GetReturned()),
		Limit:    int(pg.GetLimit()),
		Offset:   int(pg.GetOffset()),
		HasMore:  pg.GetHasMore(),
	}, nil
}

// readNoteResponse is the success-path rendering used by both the daemon
// and in-process code paths. Ambiguous name resolution is handled in the
// handler itself since it needs to reach for mcp.CallToolResult.
func readNoteResponse(content, rel string) (*mcp.CallToolResult, any, error) {
	return toolResult(content, map[string]any{"path": rel, "content": content})
}

func handleReadNote(v *vault.Vault, daemon pb.IndexerServiceClient) func(context.Context, *mcp.CallToolRequest, readNoteArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args readNoteArgs) (*mcp.CallToolResult, any, error) {
		if daemon != nil {
			content, rel, err := daemonReadNote(ctx, daemon, v.Root, args.Path)
			if err == nil {
				return readNoteResponse(content, rel)
			}
			// Ambiguity is a user-facing diagnostic, not a reason to retry
			// locally (the daemon and local paths share the same vault).
			var amb *ambiguousReadErr
			if errors.As(err, &amb) {
				msg, _ := json.Marshal(map[string]any{"ambiguous": true, "candidates": amb.candidates})
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: string(msg)}},
					IsError: true,
				}, nil, nil
			}
			// Fall through to in-process on other errors (daemon crash, etc).
		}
		content, rel, err := v.ReadNote(args.Path)
		if err != nil {
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
		return readNoteResponse(content, rel)
	}
}

func handleWriteNote(v *vault.Vault, daemon pb.IndexerServiceClient) func(context.Context, *mcp.CallToolRequest, writeNoteArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args writeNoteArgs) (*mcp.CallToolResult, any, error) {
		var rel string
		var err error
		if daemon != nil {
			if rel, err = daemonWriteNote(ctx, daemon, v.Root, args.Path, args.Content); err == nil {
				return toolResult(
					fmt.Sprintf("Wrote %d bytes to %s", len(args.Content), rel),
					map[string]any{"path": rel, "bytes": len(args.Content), "success": true},
				)
			}
		}
		if rel, err = v.WriteNote(args.Path, args.Content); err != nil {
			return nil, nil, err
		}
		return toolResult(
			fmt.Sprintf("Wrote %d bytes to %s", len(args.Content), rel),
			map[string]any{"path": rel, "bytes": len(args.Content), "success": true},
		)
	}
}

func handleDeleteNote(v *vault.Vault, daemon pb.IndexerServiceClient) func(context.Context, *mcp.CallToolRequest, deleteNoteArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args deleteNoteArgs) (*mcp.CallToolResult, any, error) {
		if daemon != nil {
			if err := daemonDeleteNote(ctx, daemon, v.Root, args.Path); err == nil {
				return toolResult("Deleted "+args.Path, map[string]any{"path": args.Path, "success": true})
			}
		}
		if err := v.DeleteNote(args.Path); err != nil {
			return nil, nil, err
		}
		return toolResult("Deleted "+args.Path, map[string]any{"path": args.Path, "success": true})
	}
}

func handleSearchByTitle(v *vault.Vault, daemon pb.IndexerServiceClient) func(context.Context, *mcp.CallToolRequest, searchByTitleArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args searchByTitleArgs) (*mcp.CallToolResult, any, error) {
		if daemon != nil {
			if res, pg, err := daemonSearchByTitle(ctx, daemon, v.Root, args.Query, args.Path, args.CaseSensitive, args.Limit, args.Offset); err == nil {
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

func handleSearchVault(v *vault.Vault, daemon pb.IndexerServiceClient) func(context.Context, *mcp.CallToolRequest, searchVaultArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args searchVaultArgs) (*mcp.CallToolResult, any, error) {
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
		var res search.VaultResults
		var err error
		if daemon != nil {
			res, err = daemonSearchVault(ctx, daemon, v.Root, args.Query, args.Path, args.CaseSensitive, includeCtx, ctxLines)
		}
		if daemon == nil || err != nil {
			res, err = search.SearchVault(v, args.Query, search.VaultOpts{
				CaseSensitive:  args.CaseSensitive,
				IncludeContext: includeCtx,
				ContextLines:   ctxLines,
				Subdir:         args.Path,
			})
		}
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

func handleSearchByTags(v *vault.Vault, daemon pb.IndexerServiceClient) func(context.Context, *mcp.CallToolRequest, searchByTagsArgs) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args searchByTagsArgs) (*mcp.CallToolResult, any, error) {
		// Prefer `path` (current contract) but fall back to the legacy
		// `directory` field so existing clients keep scoping the search.
		subdir := args.Path
		if subdir == "" {
			subdir = args.Directory
		}
		if daemon != nil {
			if res, err := daemonSearchByTags(ctx, daemon, v.Root, args.Tags, subdir, args.CaseSensitive); err == nil {
				return toolResult(fmt.Sprintf("Found %d notes matching tags %v", res.Count, args.Tags), res)
			}
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

	daemon := dialDaemon()
	_ = daemon // silence unused warning when tests compile main without routing

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list-notes",
		Description: "List markdown files in the vault or a subdirectory.",
	}, handleListNotes(v, daemon))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read-note",
		Description: "Read a note's full content. Accepts an exact vault-relative path OR a bare filename (Obsidian wikilink resolution).",
	}, handleReadNote(v, daemon))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "write-note",
		Description: "Create or overwrite a note atomically. Path must end in .md.",
	}, handleWriteNote(v, daemon))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete-note",
		Description: "Delete a note from the vault. Path must end in .md.",
	}, handleDeleteNote(v, daemon))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search-by-title",
		Description: "Find notes whose H1 title (# Title) contains the given substring.",
	}, handleSearchByTitle(v, daemon))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search-vault",
		Description: "Full-text search across notes. Supports boolean (AND OR NOT), quoted phrases, field scopes (title:, tag:, content:), grouping with parentheses, and context snippets.",
	}, handleSearchVault(v, daemon))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search-by-tags",
		Description: "Find notes that contain ALL requested tags (frontmatter or inline). Leading # is optional.",
	}, handleSearchByTags(v, daemon))

	// fs-native tools that don't fit the search package: metadata lookup,
	// MOC discovery, section read, patch, checkbox toggle.
	registerNoteTools(server, v)

	// Template tools have a filesystem fallback, so they stay available even
	// when Obsidian is not running. The rest of the CLI-backed tools register
	// only when `obsidian` is reachable.
	ctx := context.Background()
	cliExec := &obscli.Executor{Binary: cfg.ObsidianCLI, VaultPath: cfg.Vault}
	registerTemplateTools(server, cliExec)
	if cliExec.Detect(ctx) {
		registerCLITools(server, cliExec)
	}

	switch cfg.Transport {
	case "stdio":
		if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
			log.Fatalf("server error: %v", err)
		}
	case "streamable-http":
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
			return server
		}, nil)
		mux := http.NewServeMux()
		mux.HandleFunc(cfg.HTTPPath, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.Header.Get("Mcp-Session-Id") == "" {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				_, _ = w.Write([]byte("ok\n"))
				return
			}
			handler.ServeHTTP(w, r)
		})
		addr := fmt.Sprintf("%s:%d", cfg.HTTPHost, cfg.HTTPPort)
		log.Printf("obsidian-mcp listening on http://%s%s", addr, cfg.HTTPPath)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Fatalf("server error: %v", err)
		}
	}
}
