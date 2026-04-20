import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from '@modelcontextprotocol/sdk/types.js';
import { getToolDefinitions } from './toolDefinitions.js';
import { Errors, MCPError } from './errors.js';
import { textResponse, structuredResponse, errorResponse, createMetadata, stripSearchContext, appendItemList } from './response-formatter.js';
import { executeWithFallback } from './backends/resolver.js';
import { getBacklinks, getOrphans, getDeadends, getDailyNote, appendToDailyNote, moveNote, renameNote, listTemplates, readTemplate, listTasks } from './cli/cli-tools.js';

// Tool name → backend method name mapping
const toolMethodMap = {
  'search-vault': 'searchVault',
  'search-by-title': 'searchByTitle',
  'list-notes': 'listNotes',
  'read-note': 'readNote',
  'write-note': 'writeNote',
  'delete-note': 'deleteNote',
  'search-by-tags': 'searchByTags',
  'get-note-metadata': 'getNoteMetadata',
  'discover-mocs': 'discoverMocs',
  'read-section': 'readSection',
  'patch-note': 'patchNote',
  'toggle-checkbox': 'toggleCheckbox',
};

export function createServer(vaultPath, options = {}) {
  const { cliAvailable = false } = options;
  const server = new Server(
    {
      name: 'obsidian-mcp-filesystem',
      version: '0.1.0',
    },
    {
      capabilities: {
        tools: {
          listChanged: false
        },
      },
    }
  );

  // Define available tools (includes CLI-only tools when CLI is detected)
  const activeToolDefinitions = getToolDefinitions(cliAvailable);
  server.setRequestHandler(ListToolsRequestSchema, async () => ({
    tools: activeToolDefinitions,
  }));

  // Handle tool calls
  server.setRequestHandler(CallToolRequestSchema, async (request) => {
    const { name, arguments: args } = request.params;
    const startTime = Date.now();

    try {
      switch (name) {
      case 'search-vault': {
        const { query, path: searchPath, caseSensitive = false, includeContext = true, contextLines = 2, limit = 100, offset = 0 } = args;
        const contextOptions = { includeContext, contextLines };
        const result = await executeWithFallback('search-vault', 'searchVault', [vaultPath, query, searchPath, caseSensitive, contextOptions, limit, offset]);

        let description = result.totalMatches === 0
          ? `No matches found for "${query}"`
          : `Showing ${result.pagination.returned} of ${result.pagination.total} matches in ${result.fileCount} files for "${query}"`;

        if (result.pagination.hasMore) {
          const nextOffset = offset + limit;
          description += `\n(Use limit=${limit}, offset=${nextOffset} to get next page)`;
        }

        if (includeContext && result.files.length > 0) {
          description += '\n\n';
          const maxFilesInPreview = 5;
          const filesToShow = result.files.slice(0, maxFilesInPreview);

          filesToShow.forEach(file => {
            description += `${file.path} (${file.matchCount} matches)\n`;
            file.matches.slice(0, 3).forEach(match => {
              if (match.context) {
                description += `  Line ${match.line}: ${match.context.highlighted}\n`;
              } else {
                description += `  Line ${match.line}: ${match.content}\n`;
              }
            });
            if (file.matchCount > 3) {
              description += `  ... and ${file.matchCount - 3} more matches\n`;
            }
            description += '\n';
          });

          if (result.files.length > maxFilesInPreview) {
            description += `\n... and ${result.files.length - maxFilesInPreview} more files.\n`;
          }
        }

        const metadata = createMetadata(startTime, {
          tool: 'search-vault',
          filesSearched: result.filesSearched || 0
        });

        const structuredContent = stripSearchContext(result);

        return structuredResponse(structuredContent, description, metadata);
      }

      case 'search-by-title': {
        const { query, path: searchPath, caseSensitive = false, limit = 100, offset = 0 } = args;
        const result = await executeWithFallback('search-by-title', 'searchByTitle', [vaultPath, query, searchPath, caseSensitive, limit, offset]);

        let description = result.count === 0
          ? `No notes found with title matching "${query}"`
          : `Showing ${result.pagination.returned} of ${result.pagination.total} notes with title matching "${query}"`;

        if (result.pagination.hasMore) {
          const nextOffset = offset + limit;
          description += `\n(Use limit=${limit}, offset=${nextOffset} to get next page)`;
        }

        description = appendItemList(description, result.results,
          r => `- ${r.file}: ${r.title}${r.line ? ` (line ${r.line})` : ''}`);

        const metadata = createMetadata(startTime, {
          tool: 'search-by-title',
          filesSearched: result.filesSearched || 0
        });

        return structuredResponse(result, description, metadata);
      }

      case 'list-notes': {
        const { directory, limit = 100, offset = 0 } = args;
        const result = await executeWithFallback('list-notes', 'listNotes', [vaultPath, directory, limit, offset]);

        let description = result.count === 0
          ? `No notes found${directory ? ` in ${directory}` : ''}`
          : `Showing ${result.pagination.returned} of ${result.pagination.total} notes${directory ? ` in ${directory}` : ''}`;

        if (result.pagination.hasMore) {
          const nextOffset = offset + limit;
          description += `\n(Use limit=${limit}, offset=${nextOffset} to get next page)`;
        }

        description = appendItemList(description, result.notes, p => `- ${p}`);

        const metadata = createMetadata(startTime, { tool: 'list-notes' });

        return structuredResponse(result, description, metadata);
      }

      case 'read-note': {
        const { path: notePath } = args;
        const content = await executeWithFallback('read-note', 'readNote', [vaultPath, notePath]);
        
        // For read-note, we return the content directly as text
        const metadata = createMetadata(startTime, { 
          tool: 'read-note',
          contentLength: content.length 
        });
        return textResponse(content, metadata);
      }

      case 'write-note': {
        const { path: notePath, content } = args;
        await executeWithFallback('write-note', 'writeNote', [vaultPath, notePath, content]);
        
        const metadata = createMetadata(startTime, { 
          tool: 'write-note',
          contentLength: content.length 
        });
        return textResponse(`Note written successfully to ${notePath}`, metadata);
      }

      case 'delete-note': {
        const { path: notePath } = args;
        await executeWithFallback('delete-note', 'deleteNote', [vaultPath, notePath]);

        const metadata = createMetadata(startTime, { tool: 'delete-note' });
        return textResponse(`Note deleted successfully: ${notePath}`, metadata);
      }

      case 'search-by-tags': {
        const { tags, directory, caseSensitive = false } = args;
        const result = await executeWithFallback('search-by-tags', 'searchByTags', [vaultPath, tags, directory, caseSensitive]);

        const tagList = tags.join(', ');
        let description = result.count === 0
          ? `No notes found with tags: ${tagList}`
          : `Found ${result.count} notes with tags: ${tagList}`;

        description = appendItemList(description, result.notes,
          n => `- ${n.path}${n.tags?.length ? ` [${n.tags.join(', ')}]` : ''}`);

        const metadata = createMetadata(startTime, {
          tool: 'search-by-tags',
          tagsSearched: tags.length
        });

        return structuredResponse(result, description, metadata);
      }

      case 'get-note-metadata': {
        const { path: notePath, batch = false, directory, limit = 50, offset = 0 } = args;

        const pathArg = batch && directory ? directory : notePath;
        const result = await executeWithFallback('get-note-metadata', 'getNoteMetadata', [vaultPath, pathArg, { batch, limit, offset }]);

        let description;

        if (batch) {
          description = result.count === 0
            ? 'No notes found'
            : `Showing ${result.pagination.returned} of ${result.pagination.total} notes`;
          if (result.errors && result.errors.length > 0) {
            description += ` (${result.errors.length} errors)`;
          }

          if (result.pagination.hasMore) {
            const nextOffset = offset + limit;
            description += `\n(Use limit=${limit}, offset=${nextOffset} to get next page)`;
          }

          description = appendItemList(description, result.notes,
            n => `- ${n.path}${n.title ? `: ${n.title}` : ''}`);
        } else {
          description = `Retrieved metadata for: ${notePath}`;
        }

        const metadata = createMetadata(startTime, {
          tool: 'get-note-metadata',
          mode: batch ? 'batch' : 'single'
        });

        return structuredResponse(result, description, metadata);
      }

      case 'discover-mocs': {
        const { mocName, directory } = args;
        const result = await executeWithFallback('discover-mocs', 'discoverMocs', [vaultPath, { mocName, directory }]);

        let description = result.count === 0
          ? 'No MOCs found'
          : `Found ${result.count} MOCs`;

        if (mocName) {
          description += ` matching "${mocName}"`;
        }
        if (directory) {
          description += ` in ${directory}`;
        }

        if (result.mocs.length > 0) {
          description += '\n\n';
          result.mocs.forEach(moc => {
            description += `${moc.title} (${moc.linkCount} linked notes)\n`;
            description += `  Path: ${moc.path}\n`;
            if (moc.linkedNotes.length > 0) {
              description += `  Links: ${moc.linkedNotes.join(', ')}\n`;
            }
            if (moc.linkedMocs && moc.linkedMocs.length > 0) {
              description += `  Links to MOCs: ${moc.linkedMocs.join(', ')}\n`;
            }
            description += '\n';
          });
        }

        const metadata = createMetadata(startTime, {
          tool: 'discover-mocs',
          mocsFound: result.count,
          totalLinkedNotes: result.mocs.reduce((sum, moc) => sum + moc.linkCount, 0)
        });

        return structuredResponse(result, description, metadata);
      }

      case 'read-section': {
        const { path: notePath, heading, startLine, endLine } = args;
        const result = await executeWithFallback('read-section', 'readSection', [vaultPath, notePath, { heading, startLine, endLine }]);

        const description = heading
          ? `Read section "${heading}" from ${notePath} (lines ${result.startLine}-${result.endLine} of ${result.totalLines})`
          : `Read lines ${result.startLine}-${result.endLine} of ${result.totalLines} from ${notePath}`;

        const metadata = createMetadata(startTime, {
          tool: 'read-section',
          linesReturned: result.endLine - result.startLine + 1,
          totalLines: result.totalLines
        });

        return structuredResponse(result, description, metadata);
      }

      case 'patch-note': {
        const { path: notePath, old_string: oldString, new_string: newString, replaceAll = false } = args;
        const result = await executeWithFallback('patch-note', 'patchNote', [vaultPath, notePath, oldString, newString, replaceAll]);

        const description = `Patched ${notePath}: ${result.totalReplacements} replacement(s) at line(s) ${result.changedLines.join(', ')}`;

        const metadata = createMetadata(startTime, {
          tool: 'patch-note',
          replacements: result.totalReplacements
        });

        return structuredResponse(result, description, metadata);
      }

      case 'toggle-checkbox': {
        const { path: notePath, text, checked } = args;
        const result = await executeWithFallback('toggle-checkbox', 'toggleCheckbox', [vaultPath, notePath, text, checked]);

        const state = checked ? 'checked' : 'unchecked';
        const description = `Toggled checkbox to ${state} at line ${result.line} in ${notePath}`;

        const metadata = createMetadata(startTime, {
          tool: 'toggle-checkbox',
          line: result.line
        });

        return structuredResponse(result, description, metadata);
      }

      // --- CLI-only tools (available when Obsidian CLI is detected) ---

      case 'get-backlinks': {
        const { path: notePath } = args;
        const result = await getBacklinks(vaultPath, notePath);
        const header = result.count === 0
          ? `No backlinks found for ${notePath}`
          : `Found ${result.count} backlinks for ${notePath}`;
        const description = appendItemList(header, result.backlinks, b => `- ${b.path}`);
        const metadata = createMetadata(startTime, { tool: 'get-backlinks' });
        return structuredResponse(result, description, metadata);
      }

      case 'get-orphans': {
        const result = await getOrphans(vaultPath);
        const header = result.count === 0
          ? 'No orphan notes found'
          : `Found ${result.count} orphan notes (no incoming links)`;
        const description = appendItemList(header, result.orphans, o => `- ${o.path}`);
        const metadata = createMetadata(startTime, { tool: 'get-orphans' });
        return structuredResponse(result, description, metadata);
      }

      case 'get-deadends': {
        const result = await getDeadends(vaultPath);
        const header = result.count === 0
          ? 'No dead-end notes found'
          : `Found ${result.count} dead-end notes (no outgoing links)`;
        const description = appendItemList(header, result.deadends, d => `- ${d.path}`);
        const metadata = createMetadata(startTime, { tool: 'get-deadends' });
        return structuredResponse(result, description, metadata);
      }

      case 'daily-note': {
        const result = await getDailyNote(vaultPath);
        const description = result.exists
          ? `Daily note: ${result.path}`
          : `Daily note not yet created: ${result.path}`;
        const metadata = createMetadata(startTime, { tool: 'daily-note' });
        if (result.content !== null) {
          return textResponse(result.content, metadata);
        }
        return structuredResponse(result, description, metadata);
      }

      case 'daily-append': {
        const { content } = args;
        const result = await appendToDailyNote(vaultPath, content);
        const metadata = createMetadata(startTime, { tool: 'daily-append' });
        return textResponse(`Appended to daily note: ${result.path}`, metadata);
      }

      case 'move-note': {
        const { path: notePath, to } = args;
        const result = await moveNote(vaultPath, notePath, to);
        const metadata = createMetadata(startTime, { tool: 'move-note' });
        return structuredResponse(result, `Moved ${result.oldPath} → ${result.newPath}`, metadata);
      }

      case 'rename-note': {
        const { path: notePath, name: newName } = args;
        const result = await renameNote(vaultPath, notePath, newName);
        const metadata = createMetadata(startTime, { tool: 'rename-note' });
        return structuredResponse(result, `Renamed ${result.oldPath} → ${result.newName}`, metadata);
      }

      case 'list-templates': {
        const result = await listTemplates(vaultPath);
        const description = result.count === 0
          ? 'No templates found'
          : `Found ${result.count} templates`;
        const metadata = createMetadata(startTime, { tool: 'list-templates' });
        return structuredResponse(result, description, metadata);
      }

      case 'read-template': {
        const { name: templateName, resolve = false } = args;
        const result = await readTemplate(vaultPath, templateName, resolve);
        const metadata = createMetadata(startTime, { tool: 'read-template' });
        return textResponse(result.content, metadata);
      }

      case 'list-tasks': {
        const { done, todo, daily } = args;
        const result = await listTasks(vaultPath, { done, todo, daily });
        const description = result.count === 0
          ? 'No tasks found'
          : `Found ${result.count} tasks`;
        const metadata = createMetadata(startTime, { tool: 'list-tasks' });
        return structuredResponse(result, description, metadata);
      }

      default:
        throw Errors.toolNotFound(name);
      }
    } catch (error) {
      if (error instanceof MCPError) {
        throw error;
      }
      return errorResponse(error);
    }
  });

  return server;
}