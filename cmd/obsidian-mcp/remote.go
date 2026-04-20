// Remote-path helpers for the Group A tool handlers. Each function does
// one RPC to the indexer daemon and reshapes the response into the form
// the in-process library produces, so the handler above can branch on
// "daemon set" vs "daemon nil" without caring about protobuf types.
//
// Every helper returns an error the handler should treat as "fall back
// to the in-process implementation". Daemons crash; sockets get unlinked;
// versions drift — an MCP session that survives all three of those is
// how we keep the user from noticing when we ship.
package main

import (
	"context"

	"obsidian-mcp/internal/search"
	pb "obsidian-mcp/proto/indexer/v1"
)

// ─── read-note ───────────────────────────────────────────────────────

// daemonReadNote returns the same (content, resolvedPath) pair that
// vault.ReadNote produces. Ambiguous-name resolution bubbles up via the
// dedicated return so the caller can shape the MCP error response the
// same way the in-process path does.
type ambiguousReadErr struct{ candidates []string }

func (a *ambiguousReadErr) Error() string { return "ambiguous note name" }

func daemonReadNote(ctx context.Context, c pb.IndexerServiceClient, vaultRoot, path string) (content, resolvedPath string, err error) {
	resp, err := c.ReadNote(ctx, &pb.ReadNoteRequest{VaultPath: vaultRoot, Path: path})
	if err != nil {
		return "", "", err
	}
	if len(resp.GetAmbiguousCandidates()) > 0 {
		return "", "", &ambiguousReadErr{candidates: resp.GetAmbiguousCandidates()}
	}
	return resp.GetContent(), resp.GetResolvedPath(), nil
}

// ─── write-note / delete-note ───────────────────────────────────────

func daemonWriteNote(ctx context.Context, c pb.IndexerServiceClient, vaultRoot, path, content string) (string, error) {
	resp, err := c.WriteNote(ctx, &pb.WriteNoteRequest{VaultPath: vaultRoot, Path: path, Content: content})
	if err != nil {
		return "", err
	}
	return resp.GetResolvedPath(), nil
}

func daemonDeleteNote(ctx context.Context, c pb.IndexerServiceClient, vaultRoot, path string) error {
	_, err := c.DeleteNote(ctx, &pb.DeleteNoteRequest{VaultPath: vaultRoot, Path: path})
	return err
}

// ─── search-by-title ────────────────────────────────────────────────

func daemonSearchByTitle(ctx context.Context, c pb.IndexerServiceClient, vaultRoot, query, subdir string, caseSensitive bool, limit, offset int) (search.TitleResults, pagination, error) {
	resp, err := c.SearchByTitle(ctx, &pb.SearchByTitleRequest{
		VaultPath:     vaultRoot,
		Query:         query,
		Subdir:        subdir,
		CaseSensitive: caseSensitive,
		Limit:         int32(limit),
		Offset:        int32(offset),
	})
	if err != nil {
		return search.TitleResults{}, pagination{}, err
	}
	hits := make([]search.TitleHit, 0, len(resp.GetResults()))
	for _, h := range resp.GetResults() {
		// Daemon emits `path`; library struct keeps the JS field name
		// `File` for wire compatibility. Map once here.
		hits = append(hits, search.TitleHit{File: h.GetPath(), Title: h.GetTitle(), Line: int(h.GetLine())})
	}
	pg := resp.GetPagination()
	return search.TitleResults{
			Results:       hits,
			Count:         len(hits),
			FilesSearched: int(resp.GetFilesSearched()),
		}, pagination{
			Total:    int(pg.GetTotal()),
			Returned: int(pg.GetReturned()),
			Limit:    int(pg.GetLimit()),
			Offset:   int(pg.GetOffset()),
			HasMore:  pg.GetHasMore(),
		}, nil
}

// ─── search-vault ───────────────────────────────────────────────────

func daemonSearchVault(ctx context.Context, c pb.IndexerServiceClient, vaultRoot, query, subdir string, caseSensitive, includeContext bool, contextLines int) (search.VaultResults, error) {
	resp, err := c.SearchVault(ctx, &pb.SearchVaultRequest{
		VaultPath:      vaultRoot,
		Query:          query,
		Subdir:         subdir,
		CaseSensitive:  caseSensitive,
		IncludeContext: includeContext,
		ContextLines:   int32(contextLines),
	})
	if err != nil {
		return search.VaultResults{}, err
	}
	out := search.VaultResults{
		TotalMatches:  int(resp.GetTotalMatches()),
		FileCount:     int(resp.GetFileCount()),
		FilesSearched: int(resp.GetFilesSearched()),
	}
	out.Files = make([]search.FileMatches, 0, len(resp.GetFiles()))
	for _, f := range resp.GetFiles() {
		fm := search.FileMatches{
			Path:       f.GetPath(),
			MatchCount: int(f.GetMatchCount()),
			Matches:    make([]search.Match, 0, len(f.GetMatches())),
		}
		for _, m := range f.GetMatches() {
			sm := search.Match{Line: int(m.GetLine()), Content: m.GetContent()}
			if pc := m.GetContext(); pc != nil {
				lines := make([]search.ContextLine, 0, len(pc.GetLines()))
				for _, pl := range pc.GetLines() {
					lines = append(lines, search.ContextLine{
						Number:  int(pl.GetNumber()),
						Text:    pl.GetText(),
						IsMatch: pl.GetIsMatch(),
					})
				}
				sm.Context = &search.Context{Lines: lines, Highlighted: pc.GetHighlighted()}
			}
			fm.Matches = append(fm.Matches, sm)
		}
		out.Files = append(out.Files, fm)
	}
	return out, nil
}

// ─── search-by-tags ─────────────────────────────────────────────────

func daemonSearchByTags(ctx context.Context, c pb.IndexerServiceClient, vaultRoot string, tags []string, subdir string, caseSensitive bool) (search.TagResults, error) {
	resp, err := c.SearchByTags(ctx, &pb.SearchByTagsRequest{
		VaultPath:     vaultRoot,
		Tags:          tags,
		Subdir:        subdir,
		CaseSensitive: caseSensitive,
	})
	if err != nil {
		return search.TagResults{}, err
	}
	notes := make([]search.TagHit, 0, len(resp.GetNotes()))
	for _, n := range resp.GetNotes() {
		notes = append(notes, search.TagHit{Path: n.GetPath(), Tags: n.GetTags()})
	}
	return search.TagResults{Notes: notes, Count: int(resp.GetCount())}, nil
}
