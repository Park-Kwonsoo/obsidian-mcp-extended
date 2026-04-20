// Package indexer implements the gRPC service that backs the
// obsidian-indexerd daemon. Each RPC funnels into the same internal
// packages the MCP server uses in-process (vault, search, metadata,
// notes), so the daemon does not duplicate search logic — it hosts it.
//
// The daemon's long-running state (warm fsnotify watcher, cached file
// lists, eventually a proper index) lives in Server; the service
// implementation just reads from it and delegates the real work.
package indexer

import (
	"context"
	"errors"
	"sync"

	"obsidian-mcp/internal/metadata"
	"obsidian-mcp/internal/search"
	"obsidian-mcp/internal/vault"
	pb "obsidian-mcp/proto/indexer/v1"
)

// Server implements pb.IndexerServiceServer. It holds a Vault per
// absolute vault_path so different MCP clients can address different
// vaults through the same daemon. A future warm index will attach here.
type Server struct {
	pb.UnimplementedIndexerServiceServer

	mu     sync.Mutex
	vaults map[string]*vault.Vault
}

// NewServer returns a bare daemon ready to accept RPCs. Vaults are
// opened lazily on first use of each vault_path; callers don't need to
// register vaults in advance.
func NewServer() *Server {
	return &Server{vaults: map[string]*vault.Vault{}}
}

// getVault opens and caches a vault by absolute path. The cache means a
// hot vault avoids repeated dir-exists probes per RPC.
func (s *Server) getVault(root string) (*vault.Vault, error) {
	if root == "" {
		return nil, errors.New("vault_path required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.vaults[root]; ok {
		return v, nil
	}
	v, err := vault.Open(root)
	if err != nil {
		return nil, err
	}
	s.vaults[root] = v
	return v, nil
}

// ─── ListNotes ───────────────────────────────────────────────────────

func (s *Server) ListNotes(_ context.Context, req *pb.ListNotesRequest) (*pb.ListNotesResponse, error) {
	v, err := s.getVault(req.GetVaultPath())
	if err != nil {
		return nil, err
	}
	all, err := v.ListMarkdown(req.GetSubdir())
	if err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 100
	}
	offset := int(req.GetOffset())
	start, end, pg := paginate(len(all), limit, offset)
	return &pb.ListNotesResponse{
		Notes:      all[start:end],
		Pagination: pg,
	}, nil
}

// ─── ReadNote ────────────────────────────────────────────────────────

func (s *Server) ReadNote(_ context.Context, req *pb.ReadNoteRequest) (*pb.ReadNoteResponse, error) {
	v, err := s.getVault(req.GetVaultPath())
	if err != nil {
		return nil, err
	}
	content, rel, err := v.ReadNote(req.GetPath())
	if err != nil {
		var amb *vault.ErrAmbiguous
		if errors.As(err, &amb) {
			return &pb.ReadNoteResponse{AmbiguousCandidates: amb.Candidates}, nil
		}
		return nil, err
	}
	return &pb.ReadNoteResponse{ResolvedPath: rel, Content: content}, nil
}

// ─── WriteNote ───────────────────────────────────────────────────────

func (s *Server) WriteNote(_ context.Context, req *pb.WriteNoteRequest) (*pb.WriteNoteResponse, error) {
	v, err := s.getVault(req.GetVaultPath())
	if err != nil {
		return nil, err
	}
	rel, err := v.WriteNote(req.GetPath(), req.GetContent())
	if err != nil {
		return nil, err
	}
	return &pb.WriteNoteResponse{
		ResolvedPath: rel,
		BytesWritten: int64(len(req.GetContent())),
	}, nil
}

// ─── DeleteNote ──────────────────────────────────────────────────────

func (s *Server) DeleteNote(_ context.Context, req *pb.DeleteNoteRequest) (*pb.DeleteNoteResponse, error) {
	v, err := s.getVault(req.GetVaultPath())
	if err != nil {
		return nil, err
	}
	if err := v.DeleteNote(req.GetPath()); err != nil {
		return nil, err
	}
	return &pb.DeleteNoteResponse{DeletedPath: req.GetPath()}, nil
}

// ─── SearchByTitle ───────────────────────────────────────────────────

func (s *Server) SearchByTitle(_ context.Context, req *pb.SearchByTitleRequest) (*pb.SearchByTitleResponse, error) {
	v, err := s.getVault(req.GetVaultPath())
	if err != nil {
		return nil, err
	}
	res, err := search.SearchByTitle(v, req.GetQuery(), req.GetSubdir(), req.GetCaseSensitive())
	if err != nil {
		return nil, err
	}

	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 100
	}
	offset := int(req.GetOffset())
	start, end, pg := paginate(res.Count, limit, offset)
	page := res.Results[start:end]

	hits := make([]*pb.TitleHit, len(page))
	for i, h := range page {
		// search.TitleHit keeps the JS field name `File`; the proto
		// canonicalized it to `path` so the daemon returns the same key
		// across all RPCs.
		hits[i] = &pb.TitleHit{Path: h.File, Title: h.Title, Line: int32(h.Line)}
	}
	return &pb.SearchByTitleResponse{
		Results:       hits,
		Pagination:    pg,
		FilesSearched: int32(res.FilesSearched),
	}, nil
}

// ─── SearchVault ─────────────────────────────────────────────────────

func (s *Server) SearchVault(_ context.Context, req *pb.SearchVaultRequest) (*pb.SearchVaultResponse, error) {
	v, err := s.getVault(req.GetVaultPath())
	if err != nil {
		return nil, err
	}
	res, err := search.SearchVault(v, req.GetQuery(), search.VaultOpts{
		CaseSensitive:  req.GetCaseSensitive(),
		IncludeContext: req.GetIncludeContext(),
		ContextLines:   int(req.GetContextLines()),
		Subdir:         req.GetSubdir(),
	})
	if err != nil {
		return nil, err
	}

	// Flatten into pagination-aware pb.FileMatches.
	files := make([]*pb.FileMatches, 0, len(res.Files))
	for _, f := range res.Files {
		matches := make([]*pb.Match, len(f.Matches))
		for i, m := range f.Matches {
			pm := &pb.Match{Line: int32(m.Line), Content: m.Content}
			if m.Context != nil {
				lines := make([]*pb.ContextLine, len(m.Context.Lines))
				for j, ln := range m.Context.Lines {
					lines[j] = &pb.ContextLine{
						Number:  int32(ln.Number),
						Text:    ln.Text,
						IsMatch: ln.IsMatch,
					}
				}
				pm.Context = &pb.MatchContext{
					Lines:       lines,
					Highlighted: m.Context.Highlighted,
				}
			}
			matches[i] = pm
		}
		files = append(files, &pb.FileMatches{
			Path:       f.Path,
			MatchCount: int32(f.MatchCount),
			Matches:    matches,
		})
	}

	return &pb.SearchVaultResponse{
		Files:         files,
		TotalMatches:  int32(res.TotalMatches),
		FileCount:     int32(res.FileCount),
		FilesSearched: int32(res.FilesSearched),
	}, nil
}

// ─── SearchByTags ────────────────────────────────────────────────────

func (s *Server) SearchByTags(_ context.Context, req *pb.SearchByTagsRequest) (*pb.SearchByTagsResponse, error) {
	v, err := s.getVault(req.GetVaultPath())
	if err != nil {
		return nil, err
	}
	res, err := search.SearchByTags(v, req.GetTags(), req.GetSubdir(), req.GetCaseSensitive())
	if err != nil {
		return nil, err
	}
	notes := make([]*pb.TagHit, len(res.Notes))
	for i, n := range res.Notes {
		notes[i] = &pb.TagHit{Path: n.Path, Tags: n.Tags}
	}
	return &pb.SearchByTagsResponse{Notes: notes, Count: int32(res.Count)}, nil
}

// ─── paginate ────────────────────────────────────────────────────────

// paginate is the same clamp logic the MCP handlers use, duplicated here
// so the daemon package has no reverse dependency on cmd/.
func paginate(total, limit, offset int) (int, int, *pb.Pagination) {
	if limit <= 0 {
		limit = 100
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
	return start, end, &pb.Pagination{
		Total:    int32(total),
		Returned: int32(end - start),
		Limit:    int32(limit),
		Offset:   int32(offset),
		HasMore:  end < total,
	}
}

// metadata package kept imported for the upcoming warm-index work —
// drops the unused-import warning in the meantime.
var _ = metadata.IsMoc
