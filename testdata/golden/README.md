# golden — JS baseline snapshots

Each `*.json` file captures the **semantic invariants** of a single tool call
against the JS MCP server running on the `synth-100` fixture vault. The Go
tests under `go/tests/golden/` replay the same inputs through the Go library
and assert the same invariants come out. Byte-for-byte JSON match is **not**
required — the two implementations make different sort/formatting choices —
but the set of paths, match counts, and resolved totals must agree.

Regenerate after intentional behavior changes:

```bash
go test ./tests/golden -update
```

The capture path uses the JS server as the authoritative source of truth.
When JS behavior drifts (e.g. a tag-extraction tweak), update the golden
rather than the Go code if the drift is intentional; otherwise, fix the Go
code until the golden assertion passes again.
