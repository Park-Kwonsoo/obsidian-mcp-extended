// Package security provides input-validation primitives for the MCP server:
// path-traversal containment, markdown extension check, required-parameter
// asserts, content sanitization, and file-size bounds.
//
// The JS implementation split these across validation.js (pure) and security.js
// (error-throwing wrappers). Go doesn't need the split — errors are values, so
// the "pure" and "wrapper" forms collapse into one function that returns
// (result, error). Error messages match the JS strings byte-for-byte so
// existing JSON-RPC clients see the same diagnostics.
package security

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"obsidian-mcp/internal/config"
)

// Sentinel errors. Callers can use errors.Is to branch on kind without string
// matching. Error messages on wrapping remain compatible with JS output.
var (
	ErrPathOutsideVault   = errors.New("path traversal detected")
	ErrAbsolutePathOutside = errors.New("Absolute path outside vault directory")
	ErrBasePathRequired   = errors.New("Base path is required")
	ErrNotMarkdown        = errors.New("Only markdown files (.md) are supported")
	ErrFilePathRequired   = errors.New("File path is required")
	ErrFileTooLarge       = errors.New("file too large")
)

// ResolveVaultPath validates that targetPath resolves inside vaultPath and
// returns the absolute, cleaned path. Empty targetPath means the vault root.
//
// This is the single authoritative traversal check. Everywhere else that needs
// to open a file should call it first; relying on open-time errors would still
// leak information about vault structure.
func ResolveVaultPath(vaultPath, targetPath string) (string, error) {
	if vaultPath == "" {
		return "", ErrBasePathRequired
	}

	base, err := filepath.Abs(vaultPath)
	if err != nil {
		return "", fmt.Errorf("resolve base: %w", err)
	}

	if targetPath == "" {
		return base, nil
	}

	// Reject absolute paths that don't already live inside the vault. Allowing
	// them even when they *do* resolve inside is kept for backward compatibility
	// with the JS server — some callers hand us absolute vault paths.
	if filepath.IsAbs(targetPath) {
		absTarget, err := filepath.Abs(targetPath)
		if err != nil {
			return "", fmt.Errorf("resolve target: %w", err)
		}
		if !strings.HasPrefix(absTarget, base+string(filepath.Separator)) && absTarget != base {
			return "", ErrAbsolutePathOutside
		}
		return absTarget, nil
	}

	resolved, err := filepath.Abs(filepath.Join(base, targetPath))
	if err != nil {
		return "", fmt.Errorf("resolve target: %w", err)
	}
	if !strings.HasPrefix(resolved, base+string(filepath.Separator)) && resolved != base {
		return "", ErrPathOutsideVault
	}
	return resolved, nil
}

// RequireMarkdown rejects any path whose extension is not .md (case-insensitive).
// Called before read/write/delete so the server never touches .obsidian/ config
// or binary attachments.
func RequireMarkdown(filePath string) error {
	if filePath == "" {
		return ErrFilePathRequired
	}
	if !strings.EqualFold(filepath.Ext(filePath), ".md") {
		return ErrNotMarkdown
	}
	return nil
}

// RequireParams checks that every name in required has a non-empty entry in
// params. Matches validateRequiredParameters from JS. Returns the first missing
// param name in the error.
func RequireParams(params map[string]any, required []string) error {
	if params == nil {
		return errors.New("Parameters must be an object")
	}
	for _, name := range required {
		v, ok := params[name]
		if !ok || v == nil {
			return fmt.Errorf("Missing required parameter: %s", name)
		}
		// Zero-length strings count as missing for string params — JS treats
		// undefined and "" as equivalent for most tools.
		if s, isString := v.(string); isString && s == "" {
			return fmt.Errorf("Missing required parameter: %s", name)
		}
	}
	return nil
}

// SanitizeContent strips NUL bytes from input. The JS server applies this on
// write to prevent binary payloads smuggled through the content= field.
func SanitizeContent(content string) string {
	return strings.ReplaceAll(content, "\x00", "")
}

// CheckFileSize returns nil if size is within limit, or an ErrFileTooLarge
// wrapped with the same human-readable message the JS server produces.
func CheckFileSize(size int64, maxSize int64) error {
	if maxSize <= 0 {
		maxSize = config.MaxFileSize
	}
	if size > maxSize {
		return fmt.Errorf("File too large: %s exceeds maximum allowed size of %s: %w",
			FormatFileSize(size), FormatFileSize(maxSize), ErrFileTooLarge)
	}
	return nil
}

// FormatFileSize renders a byte count with a unit suffix — B, KB, MB, GB.
// Matches formatFileSize from validation.js including the two-decimal format.
func FormatFileSize(bytes int64) string {
	if bytes < 0 {
		return "Invalid size"
	}
	const kib = 1024.0
	units := []string{"B", "KB", "MB", "GB"}
	size := float64(bytes)
	idx := 0
	for size >= kib && idx < len(units)-1 {
		size /= kib
		idx++
	}
	return fmt.Sprintf("%.2f %s", size, units[idx])
}
