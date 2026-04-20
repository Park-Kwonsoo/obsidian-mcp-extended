// Package obscli wraps the `obsidian` CLI binary so Group B tools (the ones
// whose semantics depend on a running Obsidian app — link auto-update on
// rename/move, daily-note path resolution, template rendering) can stay on
// the CLI path while the fs-native tools stay pure Go.
//
// The wrapper hides three sources of Obsidian-CLI surprise:
//
//  1. Exit code 0 with `Error: ...` on stdout when a file is missing or an
//     argument is invalid. `Exec` treats that as an error, so callers never
//     have to parse the diagnostic string themselves.
//
//  2. `No backlinks found.` / `No orphans found.` sentinel lines when
//     `format=json` would normally return `[]`. Callers can check the
//     `IsEmptySentinel` helper or compare against the known sentinels.
//
//  3. "Obsidian is not running" stderr patterns. The wrapper returns
//     ErrObsidianNotRunning so callers can degrade gracefully rather than
//     leak the raw stderr to the MCP client.
package obscli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Sentinel errors returned by Exec. Callers can errors.Is against these to
// branch on the common failure modes without string matching.
var (
	// ErrCLIUnavailable — the configured obsidian binary isn't on disk.
	ErrCLIUnavailable = errors.New("obsidian CLI not available")

	// ErrObsidianNotRunning — CLI ran but reported it couldn't reach the
	// Obsidian app. Usually recoverable: the user launches Obsidian and
	// retries.
	ErrObsidianNotRunning = errors.New("obsidian app not running")

	// ErrCLITimeout — the CLI invocation exceeded the configured timeout.
	ErrCLITimeout = errors.New("obsidian CLI timed out")

	// ErrCLIFailed — catch-all for non-zero exit codes and `Error:` stdout
	// from the CLI. The wrapped error carries the raw diagnostic.
	ErrCLIFailed = errors.New("obsidian CLI failed")
)

// Result is the cleaned output of one CLI invocation.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Executor runs `obsidian` commands against a single vault. Safe for
// concurrent use; a mutex guards the cached vault name so the first
// resolveVaultName call isn't duplicated across goroutines.
type Executor struct {
	// Binary is the absolute path to the obsidian CLI, or "" to fall back
	// to $PATH lookup.
	Binary string

	// VaultPath is the absolute vault path. Used to derive the Obsidian-
	// registered vault name the CLI wants as `vault=<name>`.
	VaultPath string

	// Timeout applied per-invocation. Zero means 10 seconds.
	Timeout time.Duration

	mu        sync.Mutex
	vaultName string
	available *bool // cached availability (nil = not yet checked)
}

// binary returns the path to the obsidian CLI — either the explicit path or
// the string "obsidian" so exec.Command falls back to $PATH.
func (e *Executor) binary() string {
	if e.Binary != "" {
		return e.Binary
	}
	return "obsidian"
}

// timeout returns the per-call timeout, defaulting to 10s.
func (e *Executor) timeout() time.Duration {
	if e.Timeout > 0 {
		return e.Timeout
	}
	return 10 * time.Second
}

// Detect returns true when the obsidian CLI is reachable and responsive.
// Cached for the executor's lifetime; call InvalidateDetection to retry.
func (e *Executor) Detect(ctx context.Context) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.available != nil {
		return *e.available
	}
	// `obsidian version` is both the existence probe and the "app responsive"
	// probe — if Obsidian isn't running the version command still succeeds.
	ok := e.runRaw(ctx, "version") == nil
	e.available = &ok
	return ok
}

// InvalidateDetection forces the next Detect / Exec call to recheck CLI
// availability. Called after a timeout or "not running" error.
func (e *Executor) InvalidateDetection() {
	e.mu.Lock()
	e.available = nil
	e.mu.Unlock()
}

// runRaw is the low-level exec. Callers get exit 0 success; anything non-
// zero returns the wrapped exit-status error so Exec can inspect it.
func (e *Executor) runRaw(ctx context.Context, args ...string) error {
	cctx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	cmd := exec.CommandContext(cctx, e.binary(), args...)
	return cmd.Run()
}

// ResolveVaultName turns the absolute vault path into the short name the
// Obsidian CLI expects (`vault=<name>`). First try: parse `obsidian vaults`
// output looking for a line containing our path. Fallback: use the vault
// directory's basename.
func (e *Executor) ResolveVaultName(ctx context.Context) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.vaultName != "" {
		return e.vaultName
	}

	cctx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	out, err := exec.CommandContext(cctx, e.binary(), "vaults").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			// vault list format: "<name>  <path>" or "<name>\t<path>".
			// `strings.Fields` would split a vault name containing spaces
			// ("My Vault") and leave us with a truncated "My", which then
			// gets passed to every CLI call as vault=<wrong name>. Peel
			// the path off the end instead so names with any whitespace
			// inside survive intact.
			trimmed := strings.TrimRight(line, " \t\r\n")
			if !strings.HasSuffix(trimmed, e.VaultPath) {
				continue
			}
			name := strings.TrimSpace(trimmed[:len(trimmed)-len(e.VaultPath)])
			if name != "" {
				e.vaultName = name
				return e.vaultName
			}
		}
	}
	e.vaultName = baseName(e.VaultPath)
	return e.vaultName
}

func baseName(p string) string {
	// filepath.Base does the job but importing filepath here would drag in
	// more than we need; this is tight enough to inline.
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return p
	}
	return p[i+1:]
}

// buildArgList renders a key/value map into Obsidian CLI args:
//   - true           → bare flag (`overwrite`)
//   - false / nil    → omitted
//   - anything else  → `key=value`
//
// Nil and empty-string values are dropped so callers can build args from
// the MCP handler's struct without pre-filtering.
func buildArgList(args map[string]any) []string {
	out := make([]string, 0, len(args))
	for k, v := range args {
		if v == nil {
			continue
		}
		switch t := v.(type) {
		case bool:
			if t {
				out = append(out, k)
			}
		case string:
			if t == "" {
				continue
			}
			out = append(out, k+"="+t)
		default:
			out = append(out, fmt.Sprintf("%s=%v", k, t))
		}
	}
	return out
}

// notRunningRe matches the strings the Obsidian CLI emits to stderr when
// it can't talk to a running Obsidian app. Anchored loosely to survive
// minor wording drift across CLI versions.
var notRunningRe = regexp.MustCompile(`(?i)not running|connection refused|could not connect`)

// errorPrefixRe catches `Error: <message>` on stdout — the Obsidian CLI
// reports many failures this way with exit code 0, which is the single
// most error-prone interaction pattern with this CLI.
var errorPrefixRe = regexp.MustCompile(`^Error:\s`)

// Exec runs `obsidian <command> vault=<name> <args>` and returns a cleaned
// Result. Diagnoses:
//
//   - exit != 0 → wraps ErrCLIFailed (or ErrObsidianNotRunning if stderr
//     matches notRunningRe, or ErrCLITimeout on deadline)
//   - exit == 0 but stdout begins with `Error:` → wraps ErrCLIFailed
//
// This is the single chokepoint every Group B tool funnels through.
func (e *Executor) Exec(ctx context.Context, command string, args map[string]any) (Result, error) {
	if !e.Detect(ctx) {
		return Result{}, ErrCLIUnavailable
	}

	vaultName := e.ResolveVaultName(ctx)
	cli := []string{"vault=" + vaultName, command}
	cli = append(cli, buildArgList(args)...)

	cctx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()

	cmd := exec.CommandContext(cctx, e.binary(), cli...)
	stdoutB, err := cmd.Output()
	stdout := strings.TrimSpace(string(stdoutB))
	var stderr string
	if ee, ok := err.(*exec.ExitError); ok {
		stderr = strings.TrimSpace(string(ee.Stderr))
	}

	if cctx.Err() == context.DeadlineExceeded {
		return Result{Stdout: stdout, Stderr: stderr, ExitCode: -1},
			fmt.Errorf("%w: %s", ErrCLITimeout, command)
	}

	if err != nil {
		exitCode := 1
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
		if notRunningRe.MatchString(stderr) {
			e.InvalidateDetection()
			return Result{Stdout: stdout, Stderr: stderr, ExitCode: exitCode},
				ErrObsidianNotRunning
		}
		return Result{Stdout: stdout, Stderr: stderr, ExitCode: exitCode},
			fmt.Errorf("%w: %s: %s", ErrCLIFailed, command, stderr)
	}

	// Exit-0 with "Error: ..." on stdout — fooled plenty of earlier callers
	// into treating error messages as data; catch it once, here.
	if errorPrefixRe.MatchString(stdout) {
		return Result{Stdout: stdout, Stderr: stderr, ExitCode: 0},
			fmt.Errorf("%w: %s: %s", ErrCLIFailed, command, stdout)
	}

	return Result{Stdout: stdout, Stderr: stderr, ExitCode: 0}, nil
}

// IsEmptySentinel matches the "no results" strings the Obsidian CLI emits
// when format=json is ignored on empty result sets. Callers use this to
// normalize to [] before JSON decoding.
func IsEmptySentinel(stdout string) bool {
	s := strings.TrimSpace(stdout)
	return s == "" ||
		strings.EqualFold(s, "No backlinks found.") ||
		strings.EqualFold(s, "No orphans found.") ||
		strings.EqualFold(s, "No deadends found.")
}
