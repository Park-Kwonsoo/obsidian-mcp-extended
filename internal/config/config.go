// Package config holds runtime-tunable limits and defaults ported from src/config.js.
// Values are package-level vars (not consts) so a top-level main.go can override
// them from env or flags before any handler runs.
package config

import "time"

var (
	// MaxFileSize caps any single note read/write. Notes above this are treated
	// as "skip" in search and "too large" in read. Matches JS default (10 MiB).
	MaxFileSize int64 = 10 * 1024 * 1024

	// MaxSearchResults is the hard cap on matches returned per tool call. The JS
	// server reduced this from 1000 → 100 after observing context-window blow-ups
	// when tools dumped the full result set into chat; preserve that ceiling.
	MaxSearchResults = 100

	// MaxConcurrentReads bounds goroutines that do file I/O for a single tool call.
	MaxConcurrentReads = 10

	// FileOpTimeout applies to read/write/stat per individual file.
	FileOpTimeout = 30 * time.Second

	// SearchOpTimeout applies to a whole search-vault / search-by-tags invocation.
	SearchOpTimeout = 60 * time.Second
)

// AllowedExtensions is the whitelist of file extensions the server will read or
// write. Kept as a func rather than slice so callers can't mutate it.
func AllowedExtensions() []string { return []string{".md"} }
