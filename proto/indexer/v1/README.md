# indexer.v1

gRPC contract for `obsidian-indexerd`, the long-lived vault index daemon that
will back the Go MCP server in P5.

## Status: design only

No stubs are generated yet. This directory exists to freeze the public shape
of the daemon so that in-process Group A tool handlers written in P1 can
migrate to a daemon client in P5 without rewriting call sites.

When code generation is turned on (target: P5), the directory will gain:

```
indexer_grpc.pb.go   // gRPC service + client stubs
indexer.pb.go        // message types
```

Package alias in go.mod: `indexerpb`.

## Why a separate daemon, not just a bigger server?

At the real-559 vault scale measured in the P0 benchmark, a single-process Go
binary is already 15–115× faster than the current JS server, so the daemon is
**not needed for current-day performance**. The case for splitting it out is
visible only at ≥10 k-note vaults, where naive per-call re-scans dominate:

| Tool on 10 k vault | JS cold | Go single-process cold | Go via daemon (projected) |
|---|---:|---:|---:|
| `list-notes` | 1003.8 ms | 19.4 ms | < 5 ms |
| `search-vault` | 1183.2 ms | 251.8 ms | < 20 ms |
| `search-by-tags` | 1169.7 ms | 1165.9 ms | < 20 ms |

The daemon keeps a warm in-memory index and an `fsnotify` watcher so
`search-vault` on a 10 k vault becomes a map lookup rather than a re-read of
every file.

## How fallback works

The MCP server (`cmd/obsidian-mcp`) routes based on `OBSIDIAN_MCP_DAEMON`:

- **Unset** → call `internal/search`, `internal/vault`, `internal/metadata`
  directly. This is the P1/P2 default.
- **Set** → dial the daemon over the named Unix domain socket and issue the
  corresponding RPC. If the daemon is unreachable, transparently fall back to
  the in-process path and log a health-check failure so the next request
  re-attempts the daemon after a cool-down.

The fallback is important: the daemon is an optimization, not a requirement.
A failed daemon must never break a user who was fine on the single-process
path.
