# obsidian-mcp

A Go-native Model Context Protocol (MCP) server for Obsidian. Talks to MCP
clients (Claude Desktop, Claude Code, …) over stdio JSON-RPC, reads vault
files directly from disk, and shells out to the `obsidian` CLI only for tools
whose semantics depend on a running Obsidian app (backlinks, daily notes,
templates).

The server is a single ~8 MB static binary with no runtime dependencies
beyond an accessible vault directory.

## Why

Most Obsidian MCP servers rely on the Obsidian REST API plugin, which needs
the Obsidian app running with a configured plugin for every request. This
server operates on the vault files directly, so it also works with
[obsidian.nvim](https://github.com/obsidian-nvim/obsidian.nvim) or any
editor that treats the vault as a plain markdown tree.

## Install

```bash
git clone <repo-url> obsidian-mcp
cd obsidian-mcp
make install   # builds and installs to $HOME/.local/bin/obsidian-mcp
```

Or manually:

```bash
go build -o $HOME/.local/bin/obsidian-mcp ./cmd/obsidian-mcp
```

Make sure `$HOME/.local/bin` is on your `PATH`.

## Register with Claude

```bash
claude mcp add obsidian -s user -- obsidian-mcp \
  --vault ~/Documents/ObsidianVault \
  --obsidian-cli "$(which obsidian)"
```

Verify:

```bash
claude mcp list    # expect "obsidian" in the output
```

### Startup flags

| Flag | Required | Purpose |
|------|----------|---------|
| `--vault` | yes | Absolute path to the Obsidian vault. The first positional argument is also accepted. |
| `--obsidian-cli` | no | Explicit path to the `obsidian` CLI binary. Defaults to a `$PATH` lookup. Only consulted by Group B tools (P2+). |

## Tools (P1)

These Group A tools are implemented directly in Go — no Obsidian CLI
required.

| Tool | What it does |
|------|--------------|
| `list-notes` | List every `.md` in the vault, or a subdirectory, with pagination. |
| `read-note` | Return a note's full content. Accepts an exact vault-relative path *or* a bare filename (Obsidian wikilink-style resolution). |
| `write-note` | Atomically create or overwrite a note (tmp-file + rename). |
| `delete-note` | Remove a note after path-traversal validation. |
| `search-by-title` | Find notes whose H1 title contains a substring. |
| `search-vault` | Full-text search with boolean `AND`/`OR`/`NOT`, quoted phrases, field scopes (`title:`, `tag:`, `content:`), and grouping with parentheses. Optional context snippets. |
| `search-by-tags` | Find notes that contain all requested tags (intersection). Reads both YAML frontmatter (`tags: [...]` or YAML list) and inline `#tag` markers. |

Group B tools that wrap the Obsidian CLI (`get-backlinks`, `get-orphans`,
`get-deadends`, `daily-note`, `daily-append`, `move-note`, `list-templates`,
`read-template`, `list-tasks`) are scheduled for P2. The remaining
fs-native tools (`discover-mocs`, `read-section`, `patch-note`,
`toggle-checkbox`, `get-note-metadata`) are scheduled for P3–P4. An
optional long-lived indexer daemon with a gRPC control plane is planned for
P5 — the proto is already frozen under `proto/indexer/v1/`.

## Performance

Methodology: `/usr/bin/time -l`-wrapped subprocess, 15 runs + 3 warmup each,
cold-start per invocation. Fixture: 552-note copy of a real vault
(`parkkwonsoo`). See `proto/indexer/v1/README.md` for the daemon rationale
and scaling curve past 10 k notes.

### Latency (real-559 vault, mean wall-clock)

| Tool | JS reference (prior) | Go-native | Speedup |
|------|---------------------:|----------:|--------:|
| `list-notes` | 1088.5 ms | **9.5 ms** | **115×** |
| `read-note` | 779.3 ms | **7.7 ms** | **101×** |
| `search-by-title` | 867.6 ms | **28.3 ms** | **31×** |
| `search-vault` | 1285.8 ms | **26 ms** | **50×** |
| `search-by-tags` | 1046.9 ms | **70 ms** | **15×** |

### Memory (RSS max, median across runs)

| Backend | Memory |
|---------|-------:|
| Go-native binary | **7.8 MB** |
| Obsidian CLI subprocess | 112.3 MB |
| JS reference server | 112.7 MB |

## Backend selection policy

For every Group A tool, the backend was chosen empirically by benchmark:

- **Go-native wins by ≥ 2×** → compiled in as the only path, no CLI fallback.
- **CLI wins by ≥ 2×** → CLI first with Go-native fallback on CLI failure.
- **Inconclusive (within 2×)** → Go-native by default to minimize runtime dependencies.

All seven Group A tools cleared the Go-native threshold by 4.5–66× margin,
so the released binary carries no Group A CLI path at all.

### Pinned: `write-note` and `delete-note`

These two are **always go-native**, even without a head-to-head benchmark:

1. **Fork overhead dominates.** A single file write/delete is a sub-millisecond filesystem op. Spawning `obsidian` costs ~100 ms for Electron startup and IPC handshake — 100–1000× the underlying work. CLI cannot win in any realistic scenario.
2. **Benchmark safety.** CLI mutating operations can only be exercised against an Obsidian-registered vault, which on development machines is the user's real iCloud-synced vault. Running write/delete benches there would mutate live notes. Verdict recorded as *go-native by construction*.

### Group B (CLI-only, kept as subprocess)

Tools whose semantics depend on Obsidian itself — link auto-update during
rename/move, daily-note path resolution, template rendering — continue to
shell out to the `obsidian` CLI. No fs equivalent exists.

`rename-note` has been dropped: `move-note` is a full superset (Obsidian
CLI's `rename` is semantically a `move` within the same directory).

## Layout

```
.
├── cmd/obsidian-mcp/        # stdio MCP server entry point
├── internal/
│   ├── config/              # runtime-tunable limits
│   ├── security/            # path traversal, markdown ext, sanitize, size
│   ├── vault/               # fs walk + wikilink resolver + read/write/delete
│   ├── metadata/            # frontmatter tags, inline #tag, wikilinks, MOC
│   └── search/              # boolean AST, context snippets, title/content/tag
├── proto/indexer/v1/        # frozen gRPC contract for the P5 daemon
├── testdata/golden/         # JS-captured regression baselines
└── tests/golden/            # Go-vs-JS parity tests
```

## Development

```bash
make install      # build + drop binary into ~/.local/bin/obsidian-mcp
make test         # unit tests + golden regression
make fmt          # gofmt + goimports
go test ./...     # raw test entry point
```

Golden regression runs against a synthesized 100-note fixture
(`scripts/gen_synth_vault.py` in the bench workspace). To regenerate after
an intentional behavior change:

```bash
go test ./tests/golden -update
```

## Status

Phase 1 complete: Group A tools implemented with 72+ unit tests and 6
JS-parity golden tests passing. P2 (Group B CLI wrappers) is next.
