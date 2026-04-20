package obscli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Backlink is one note that links to the queried file.
type Backlink struct {
	Path string `json:"path"`
}

// BacklinksResult returns in the same shape the JS backend produced so
// existing clients don't need to change.
type BacklinksResult struct {
	Backlinks []Backlink `json:"backlinks"`
	Count     int        `json:"count"`
}

// GetBacklinks returns the notes that link to file. The Obsidian CLI
// returns either `[]` with format=json, a plain-text sentinel ("No
// backlinks found."), or a JSON array of `{file, path}` shapes (the shape
// drifted across CLI versions). We normalize all three to `{path: …}`.
func (e *Executor) GetBacklinks(ctx context.Context, file string) (BacklinksResult, error) {
	r, err := e.Exec(ctx, "backlinks", map[string]any{"file": file, "format": "json"})
	if err != nil {
		return BacklinksResult{}, err
	}
	if IsEmptySentinel(r.Stdout) {
		return BacklinksResult{Backlinks: []Backlink{}, Count: 0}, nil
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(r.Stdout), &entries); err != nil {
		return BacklinksResult{}, fmt.Errorf("parse backlinks output: %w", err)
	}
	out := make([]Backlink, 0, len(entries))
	for _, entry := range entries {
		path, _ := entry["path"].(string)
		if path == "" {
			path, _ = entry["file"].(string)
		}
		if path != "" {
			out = append(out, Backlink{Path: path})
		}
	}
	return BacklinksResult{Backlinks: out, Count: len(out)}, nil
}

// PathList is the minimal result shape for orphans / deadends / templates /
// files commands, which all emit newline-separated file paths.
type PathList struct {
	Paths []string `json:"paths"`
	Count int      `json:"count"`
}

func (e *Executor) runPathList(ctx context.Context, cmd string, args map[string]any) (PathList, error) {
	r, err := e.Exec(ctx, cmd, args)
	if err != nil {
		return PathList{}, err
	}
	if IsEmptySentinel(r.Stdout) {
		return PathList{Paths: []string{}, Count: 0}, nil
	}
	out := parsePathLines(r.Stdout)
	return PathList{Paths: out, Count: len(out)}, nil
}

// parsePathLines converts "a.md\nb.md\n\n" into []{"a.md", "b.md"}. Blank
// and whitespace-only lines are dropped so callers don't see phantom
// entries from trailing newlines.
func parsePathLines(s string) []string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

// GetOrphans lists notes with zero incoming links.
func (e *Executor) GetOrphans(ctx context.Context) (PathList, error) {
	return e.runPathList(ctx, "orphans", nil)
}

// GetDeadends lists notes with zero outgoing links.
func (e *Executor) GetDeadends(ctx context.Context) (PathList, error) {
	return e.runPathList(ctx, "deadends", nil)
}

// DailyNote is the content of today's daily note plus its resolved path
// (the Obsidian CLI determines the path from the daily-notes plugin's
// configured folder + date format).
type DailyNote struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Exists  bool   `json:"exists"`
}

// GetDailyNote reads today's daily note. `daily:read` returns the body or
// errors when the note doesn't exist yet; we suppress that error and mark
// Exists=false so clients can decide whether to create it.
func (e *Executor) GetDailyNote(ctx context.Context) (DailyNote, error) {
	pathRes, err := e.Exec(ctx, "daily:path", nil)
	if err != nil {
		return DailyNote{}, err
	}
	path := strings.TrimSpace(pathRes.Stdout)
	readRes, err := e.Exec(ctx, "daily:read", nil)
	if err != nil {
		return DailyNote{Path: path, Exists: false}, nil
	}
	return DailyNote{Path: path, Content: readRes.Stdout, Exists: true}, nil
}

// AppendDailyNote appends content to today's daily note, creating the note
// if it doesn't exist yet. Returns the resolved path so callers can show
// it to the user.
func (e *Executor) AppendDailyNote(ctx context.Context, content string) (DailyNote, error) {
	if _, err := e.Exec(ctx, "daily:append", map[string]any{"content": content}); err != nil {
		return DailyNote{}, err
	}
	pathRes, err := e.Exec(ctx, "daily:path", nil)
	if err != nil {
		return DailyNote{}, err
	}
	path := strings.TrimSpace(pathRes.Stdout)
	return DailyNote{Path: path, Exists: true}, nil
}

// MoveResult carries the before/after paths so a client can update any
// bookmarks it was holding. The Obsidian CLI updates internal wikilinks
// automatically as part of the move.
type MoveResult struct {
	OldPath string `json:"oldPath"`
	NewPath string `json:"newPath"`
}

// MoveNote moves a note to a new location. Wikilink rewriting across the
// vault is handled by Obsidian itself — that's the reason this stays on
// the CLI path rather than becoming a go-native copy+delete.
func (e *Executor) MoveNote(ctx context.Context, from, to string) (MoveResult, error) {
	if _, err := e.Exec(ctx, "move", map[string]any{"file": from, "to": to}); err != nil {
		return MoveResult{}, err
	}
	return MoveResult{OldPath: from, NewPath: to}, nil
}

// TemplatesResult mirrors `obsidian templates` — a flat list of template
// names (not full paths).
type TemplatesResult struct {
	Templates []string `json:"templates"`
	Count     int      `json:"count"`
}

func (e *Executor) ListTemplates(ctx context.Context) (TemplatesResult, error) {
	r, err := e.Exec(ctx, "templates", nil)
	if err != nil {
		return TemplatesResult{}, err
	}
	names := parsePathLines(r.Stdout)
	return TemplatesResult{Templates: names, Count: len(names)}, nil
}

// TemplateContent is one resolved template's body. `resolve=true` tells
// the CLI to evaluate `{{date}}`-style placeholders before returning.
type TemplateContent struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (e *Executor) ReadTemplate(ctx context.Context, name string, resolve bool) (TemplateContent, error) {
	args := map[string]any{"name": name}
	if resolve {
		args["resolve"] = true
	}
	r, err := e.Exec(ctx, "template:read", args)
	if err != nil {
		return TemplateContent{}, err
	}
	return TemplateContent{Name: name, Content: r.Stdout}, nil
}

// Task is one line in the vault that parses as an Obsidian task (`- [ ]`
// / `- [x]` style). Checked is true when the box is marked.
type Task struct {
	Text    string `json:"text"`
	Checked bool   `json:"checked"`
}

// TasksResult is the combined view the `tasks` command produces.
type TasksResult struct {
	Tasks []Task `json:"tasks"`
	Count int    `json:"count"`
}

// TaskFilters controls which subset the CLI returns. All three flags are
// mutually usable — if both Done and Todo are true the CLI returns both;
// `Daily` restricts to today's daily note.
type TaskFilters struct {
	Done  bool
	Todo  bool
	Daily bool
}

// ListTasks asks the Obsidian CLI for tasks. Prefers JSON output but falls
// back to the CLI's human-readable format when `format=json` is ignored
// (seen on older CLI builds).
func (e *Executor) ListTasks(ctx context.Context, f TaskFilters) (TasksResult, error) {
	args := map[string]any{"format": "json"}
	if f.Done {
		args["done"] = true
	}
	if f.Todo {
		args["todo"] = true
	}
	if f.Daily {
		args["daily"] = true
	}

	r, err := e.Exec(ctx, "tasks", args)
	if err != nil {
		return TasksResult{}, err
	}

	var parsed []Task
	if json.Unmarshal([]byte(r.Stdout), &parsed) == nil {
		return TasksResult{Tasks: parsed, Count: len(parsed)}, nil
	}

	// Fallback: parse "- [ ] text" / "- [x] text" lines.
	for _, ln := range parsePathLines(r.Stdout) {
		if len(ln) < 6 || !strings.HasPrefix(ln, "- [") {
			parsed = append(parsed, Task{Text: ln})
			continue
		}
		box := ln[3]
		text := strings.TrimSpace(ln[5:])
		parsed = append(parsed, Task{Text: text, Checked: box == 'x' || box == 'X'})
	}
	return TasksResult{Tasks: parsed, Count: len(parsed)}, nil
}
