// MCP handlers for the Group B tools — the ones that route through the
// `obsidian` CLI because their semantics (link auto-update on rename/move,
// daily-note path resolution, template rendering) depend on a running
// Obsidian app and have no filesystem equivalent.
//
// registerCLITools is called only when the CLI detection succeeds at
// startup. When Obsidian isn't available, these tools simply don't appear
// in the tools/list response so clients can't invoke them and get a
// confusing failure.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"obsidian-mcp/internal/obscli"
)

// ─── Tool argument types ─────────────────────────────────────────────

type fileArg struct {
	File string `json:"file" jsonschema:"vault-relative note path"`
}

type dailyAppendArgs struct {
	Content string `json:"content" jsonschema:"text to append to today's daily note"`
}

type moveNoteArgs struct {
	File string `json:"file" jsonschema:"current vault-relative path"`
	To   string `json:"to"   jsonschema:"new vault-relative path"`
}

type readTemplateArgs struct {
	Name    string `json:"name"              jsonschema:"template name (as listed by list-templates)"`
	Resolve bool   `json:"resolve,omitempty" jsonschema:"evaluate {{placeholders}} before returning (default false)"`
}

type listTasksArgs struct {
	Done  bool `json:"done,omitempty"  jsonschema:"include checked tasks"`
	Todo  bool `json:"todo,omitempty"  jsonschema:"include unchecked tasks"`
	Daily bool `json:"daily,omitempty" jsonschema:"restrict to today's daily note"`
}

// registerCLITools wires every Group B handler against a live Executor.
// The Executor has already been detection-probed; callers pass nil to
// skip registration entirely when the CLI isn't available.
func registerCLITools(server *mcp.Server, exec *obscli.Executor) {
	if exec == nil {
		return
	}
	log.Println("obsidian CLI detected — registering Group B tools")

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get-backlinks",
		Description: "List notes that link to the given file. Requires a running Obsidian app.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a fileArg) (*mcp.CallToolResult, any, error) {
		res, err := exec.GetBacklinks(ctx, a.File)
		if err != nil {
			return nil, nil, err
		}
		return toolResult(fmt.Sprintf("%d backlinks to %s", res.Count, a.File), res)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get-orphans",
		Description: "List notes with no incoming links (vault-wide).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		res, err := exec.GetOrphans(ctx)
		if err != nil {
			return nil, nil, err
		}
		return toolResult(fmt.Sprintf("%d orphans", res.Count), res)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get-deadends",
		Description: "List notes with no outgoing links (vault-wide).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		res, err := exec.GetDeadends(ctx)
		if err != nil {
			return nil, nil, err
		}
		return toolResult(fmt.Sprintf("%d deadends", res.Count), res)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "daily-note",
		Description: "Read today's daily note. Returns path and content; content may be empty if the note hasn't been created yet.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		res, err := exec.GetDailyNote(ctx)
		if err != nil {
			return nil, nil, err
		}
		summary := "daily note at " + res.Path
		if !res.Exists {
			summary += " (not yet created)"
		}
		return toolResult(summary, res)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "daily-append",
		Description: "Append content to today's daily note, creating it if necessary.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a dailyAppendArgs) (*mcp.CallToolResult, any, error) {
		res, err := exec.AppendDailyNote(ctx, a.Content)
		if err != nil {
			return nil, nil, err
		}
		return toolResult("appended to "+res.Path, res)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "move-note",
		Description: "Move a note to a new vault-relative path. Wikilinks elsewhere in the vault are rewritten automatically by Obsidian.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a moveNoteArgs) (*mcp.CallToolResult, any, error) {
		res, err := exec.MoveNote(ctx, a.File, a.To)
		if err != nil {
			return nil, nil, err
		}
		return toolResult(fmt.Sprintf("moved %s → %s", res.OldPath, res.NewPath), res)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list-templates",
		Description: "List available Obsidian templates.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		res, err := exec.ListTemplates(ctx)
		if err != nil {
			return nil, nil, err
		}
		return toolResult(fmt.Sprintf("%d templates", res.Count), res)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read-template",
		Description: "Read a template by name. Set resolve=true to evaluate {{placeholders}} before returning.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a readTemplateArgs) (*mcp.CallToolResult, any, error) {
		res, err := exec.ReadTemplate(ctx, a.Name, a.Resolve)
		if err != nil {
			return nil, nil, err
		}
		return toolResult(fmt.Sprintf("template %s (%d bytes)", res.Name, len(res.Content)), res)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list-tasks",
		Description: "List tasks in the vault. Filters: done (checked), todo (unchecked), daily (today's daily note only).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a listTasksArgs) (*mcp.CallToolResult, any, error) {
		res, err := exec.ListTasks(ctx, obscli.TaskFilters{Done: a.Done, Todo: a.Todo, Daily: a.Daily})
		if err != nil {
			return nil, nil, err
		}
		return toolResult(fmt.Sprintf("%d tasks", res.Count), res)
	})
}
