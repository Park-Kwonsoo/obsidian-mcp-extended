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
	"time"

	"obsidian-mcp/internal/metadata"
	"obsidian-mcp/internal/search"
	"obsidian-mcp/internal/vault"
	pb "obsidian-mcp/proto/indexer/v1"
)

// Server implements pb.IndexerServiceServer. It holds one per-vault
// record keyed by absolute path; each record owns the vault handle, a
// filesystem watcher, and a cached note listing the watcher invalidates.
// Multiple MCP clients pointing at different vaults route through the
// same daemon.
type Server struct {
	pb.UnimplementedIndexerServiceServer

	mu     sync.Mutex
	vaults map[string]*vaultRecord
}

// vaultRecord bundles the warm state for one vault. Every field beyond
// `v` is protected by rmu so a list-notes RPC and an incoming fsnotify
// event can't race each other.
type vaultRecord struct {
	v       *vault.Vault
	watcher *Watcher

	rmu        sync.RWMutex
	listCache  []string // nil until the first ListNotes populates it
}

// NewServer returns a bare daemon ready to accept RPCs. Vaults are
// opened lazily on first use of each vault_path; callers don't need to
// register vaults in advance.
func NewServer() *Server {
	return &Server{vaults: map[string]*vaultRecord{}}
}

// getVault opens (on first use) and returns the vaultRecord for root.
// The watcher is started the same time as the vault handle so cached
// state becomes invalid the instant the user touches a file — no stale
// list-notes responses after a write.
func (s *Server) getVault(root string) (*vaultRecord, error) {
	if root == "" {
		return nil, errors.New("vault_path required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.vaults[root]; ok {
		return rec, nil
	}
	v, err := vault.Open(root)
	if err != nil {
		return nil, err
	}
	rec := &vaultRecord{v: v}
	w, err := newWatcher(v.Root, rec.invalidate)
	if err == nil {
		if err := w.Start(); err == nil {
			rec.watcher = w
		}
	}
	// Watcher failure isn't fatal — the cache just behaves as a
	// no-op cache (every RPC re-reads). Record the vault either way.
	s.vaults[root] = rec
	return rec, nil
}

// invalidate is the watcher's OnChange callback. It drops any per-vault
// caches; the next RPC that needs them rebuilds from disk.
func (r *vaultRecord) invalidate() {
	r.rmu.Lock()
	r.listCache = nil
	r.rmu.Unlock()
}

// listMarkdown returns the cached note list, rebuilding on a miss.
// Called by the ListNotes RPC — giving it warm-cache semantics without
// changing the wire. subdir support intentionally bypasses the cache
// (a subdir filter is rare enough not to justify multiple cache slots).
func (r *vaultRecord) listMarkdown(subdir string) ([]string, error) {
	if subdir != "" {
		return r.v.ListMarkdown(subdir)
	}
	r.rmu.RLock()
	cached := r.listCache
	r.rmu.RUnlock()
	if cached != nil {
		return cached, nil
	}
	fresh, err := r.v.ListMarkdown("")
	if err != nil {
		return nil, err
	}
	r.rmu.Lock()
	r.listCache = fresh
	r.rmu.Unlock()
	return fresh, nil
}

// ─── ListNotes ───────────────────────────────────────────────────────

func (s *Server) ListNotes(_ context.Context, req *pb.ListNotesRequest) (*pb.ListNotesResponse, error) {
	rec, err := s.getVault(req.GetVaultPath())
	if err != nil {
		return nil, err
	}
	all, err := rec.listMarkdown(req.GetSubdir())
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
	rec, err := s.getVault(req.GetVaultPath())
	if err != nil {
		return nil, err
	}
	content, rel, err := rec.v.ReadNote(req.GetPath())
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
	rec, err := s.getVault(req.GetVaultPath())
	if err != nil {
		return nil, err
	}
	rel, err := rec.v.WriteNote(req.GetPath(), req.GetContent())
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
	rec, err := s.getVault(req.GetVaultPath())
	if err != nil {
		return nil, err
	}
	if err := rec.v.DeleteNote(req.GetPath()); err != nil {
		return nil, err
	}
	return &pb.DeleteNoteResponse{DeletedPath: req.GetPath()}, nil
}

// ─── SearchByTitle ───────────────────────────────────────────────────

func (s *Server) SearchByTitle(_ context.Context, req *pb.SearchByTitleRequest) (*pb.SearchByTitleResponse, error) {
	rec, err := s.getVault(req.GetVaultPath())
	if err != nil {
		return nil, err
	}
	res, err := search.SearchByTitle(rec.v, req.GetQuery(), req.GetSubdir(), req.GetCaseSensitive())
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
	rec, err := s.getVault(req.GetVaultPath())
	if err != nil {
		return nil, err
	}
	res, err := search.SearchVault(rec.v, req.GetQuery(), search.VaultOpts{
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
	rec, err := s.getVault(req.GetVaultPath())
	if err != nil {
		return nil, err
	}
	res, err := search.SearchByTags(rec.v, req.GetTags(), req.GetSubdir(), req.GetCaseSensitive())
	if err != nil {
		return nil, err
	}
	notes := make([]*pb.TagHit, len(res.Notes))
	for i, n := range res.Notes {
		notes[i] = &pb.TagHit{Path: n.Path, Tags: n.Tags}
	}
	return &pb.SearchByTagsResponse{Notes: notes, Count: int32(res.Count)}, nil
}

// ─── SubscribeFileChanges ────────────────────────────────────────────

// SubscribeFileChanges streams per-file mutation events for one vault.
// If req.InitialSnapshot is set the daemon first replays a CREATED event
// per existing .md (so a fresh client reaches steady state without an
// extra ListNotes round-trip) and then tails the live watcher until the
// client cancels or the server shuts down.
//
// Requires the vault's watcher to have started successfully; if the
// watcher failed to come up (rare — usually a kqueue/inotify exhaustion)
// we return an error so the client can fall back rather than hang on an
// event stream that will never produce anything.
func (s *Server) SubscribeFileChanges(req *pb.SubscribeFileChangesRequest, stream pb.IndexerService_SubscribeFileChangesServer) error {
	rec, err := s.getVault(req.GetVaultPath())
	if err != nil {
		return err
	}
	if rec.watcher == nil {
		return errors.New("file-change watcher unavailable for vault")
	}

	ctx := stream.Context()

	if req.GetInitialSnapshot() {
		notes, err := rec.listMarkdown("")
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		for _, p := range notes {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := stream.Send(&pb.SubscribeFileChangesResponse{
				Event: &pb.FileChangeEvent{
					Kind:        pb.ChangeKind_CHANGE_KIND_CREATED,
					Path:        p,
					TimestampMs: now,
				},
			}); err != nil {
				return err
			}
		}
	}

	// Subscribe after the snapshot so steady-state begins exactly where
	// the snapshot ended. A small race remains (changes during the
	// snapshot that neither land in the snapshot list nor arrive on the
	// subscription); clients that care can ListNotes again after the
	// first live event settles. The alternative — subscribing first and
	// buffering — traps the daemon into holding arbitrarily many events
	// while the snapshot streams.
	ch := rec.watcher.Subscribe(0)
	defer rec.watcher.Unsubscribe(ch)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-ch:
			if !ok {
				return nil // watcher closed (daemon shutdown)
			}
			if err := stream.Send(&pb.SubscribeFileChangesResponse{
				Event: &pb.FileChangeEvent{
					Kind:        toPBChangeKind(ev.Kind),
					Path:        ev.Path,
					TimestampMs: ev.Timestamp.UnixMilli(),
				},
			}); err != nil {
				return err
			}
		}
	}
}

// toPBChangeKind translates the watcher's internal enum into the wire
// enum. Kept as a function rather than a map so an unknown source value
// maps to CHANGE_KIND_UNSPECIFIED explicitly.
func toPBChangeKind(k ChangeKind) pb.ChangeKind {
	switch k {
	case ChangeCreated:
		return pb.ChangeKind_CHANGE_KIND_CREATED
	case ChangeModified:
		return pb.ChangeKind_CHANGE_KIND_MODIFIED
	case ChangeDeleted:
		return pb.ChangeKind_CHANGE_KIND_DELETED
	case ChangeRenamed:
		return pb.ChangeKind_CHANGE_KIND_RENAMED
	}
	return pb.ChangeKind_CHANGE_KIND_UNSPECIFIED
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
