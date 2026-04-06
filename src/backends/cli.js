/**
 * CLI backend — uses the `obsidian` CLI binary to interact with the vault.
 *
 * Each method returns data in the same shape as the filesystem backend
 * so that server.js response formatting needs no changes.
 */

import path from 'path';
import { execCli, execCliJson } from '../cli/executor.js';
import { Errors } from '../errors.js';
import {
  validateRequiredParameters,
  validateMarkdownExtension,
  validatePathWithinBase,
} from '../validation.js';

function assertValid(validationResult, errorFactory) {
  if (!validationResult.valid) {
    throw errorFactory(validationResult.error, validationResult);
  }
  return validationResult;
}

/**
 * Parse CLI search output into the standard result shape.
 * CLI `search` output: one file path per line with match count.
 * CLI `search:context` output: grep-style "path:line:content" lines.
 */
function parseSearchOutput(stdout, vaultPath) {
  if (!stdout) return [];

  const files = {};
  const lines = stdout.split('\n');

  for (const line of lines) {
    // Try grep-style: "path:lineNum:content"
    const contextMatch = line.match(/^(.+?):(\d+):(.*)$/);
    if (contextMatch) {
      const [, filePath, lineNum, content] = contextMatch;
      const relPath = filePath.startsWith('/') ? path.relative(vaultPath, filePath) : filePath;
      if (!files[relPath]) files[relPath] = [];
      files[relPath].push({
        line: parseInt(lineNum, 10),
        content: content.trim(),
      });
    } else if (line.trim()) {
      // Plain file path
      const relPath = line.trim();
      if (!files[relPath]) files[relPath] = [];
    }
  }

  const result = [];
  for (const [filePath, matches] of Object.entries(files)) {
    result.push({ path: filePath, matches });
  }
  return result;
}

/**
 * Parse CLI file list output (one path per line) into an array.
 */
function parseFileList(stdout) {
  if (!stdout) return [];
  return stdout.split('\n').map(l => l.trim()).filter(Boolean);
}

export async function searchVault(vaultPath, query, searchPath, caseSensitive = false, contextOptions = {}, limit = 100, offset = 0) {
  const paramValidation = validateRequiredParameters({ query }, ['query']);
  assertValid(paramValidation, (msg) => Errors.invalidParams(msg));

  const args = { query };
  if (searchPath) args.path = searchPath;
  if (caseSensitive) args.case = true;
  if (limit) args.limit = limit;

  // Use search:context for richer output when context is requested
  const command = contextOptions.includeContext ? 'search:context' : 'search';
  const result = await execCli(command, args);

  const files = parseSearchOutput(result.stdout, vaultPath);

  // Apply offset pagination (CLI handles limit but not offset)
  const paginated = files.slice(offset, offset + limit);
  const totalMatches = files.reduce((sum, f) => sum + Math.max(1, f.matches.length), 0);

  return {
    files: paginated,
    totalMatches,
    fileCount: files.length,
    filesSearched: files.length,
    pagination: {
      total: totalMatches,
      returned: paginated.reduce((sum, f) => sum + Math.max(1, f.matches.length), 0),
      limit,
      offset,
      hasMore: offset + limit < files.length,
    },
  };
}

export async function searchByTitle(vaultPath, query, searchPath, caseSensitive = false, limit = 100, offset = 0) {
  const paramValidation = validateRequiredParameters({ query }, ['query']);
  assertValid(paramValidation, (msg) => Errors.invalidParams(msg));

  if (!query || query.trim() === '') {
    throw Errors.invalidParams('query cannot be empty');
  }

  // CLI doesn't have a direct title search — use search with title field
  const args = { query };
  if (searchPath) args.path = searchPath;
  if (caseSensitive) args.case = true;

  const result = await execCli('search', args);
  const allFiles = parseFileList(result.stdout);

  const paginatedFiles = allFiles.slice(offset, offset + limit);

  return {
    results: paginatedFiles.map(f => ({ path: f, title: path.basename(f, '.md') })),
    count: paginatedFiles.length,
    filesSearched: allFiles.length,
    pagination: {
      total: allFiles.length,
      returned: paginatedFiles.length,
      limit,
      offset,
      hasMore: offset + limit < allFiles.length,
    },
  };
}

export async function listNotes(vaultPath, directory, limit = 100, offset = 0) {
  const args = {};
  if (directory) args.folder = directory;

  const result = await execCli('files', { ...args, ext: 'md' });
  const allNotes = parseFileList(result.stdout);

  allNotes.sort();
  const paginatedNotes = allNotes.slice(offset, offset + limit);

  return {
    notes: paginatedNotes,
    count: paginatedNotes.length,
    pagination: {
      total: allNotes.length,
      returned: paginatedNotes.length,
      limit,
      offset,
      hasMore: offset + limit < allNotes.length,
    },
  };
}

export async function readNote(vaultPath, notePath) {
  const paramValidation = validateRequiredParameters({ path: notePath }, ['path']);
  assertValid(paramValidation, (msg) => Errors.invalidParams(msg));

  const extensionValidation = validateMarkdownExtension(notePath);
  assertValid(extensionValidation, (msg) => Errors.invalidParams(msg, { path: notePath }));

  const result = await execCli('read', { path: notePath });
  return result.stdout;
}

export async function writeNote(vaultPath, notePath, content) {
  const paramValidation = validateRequiredParameters({ path: notePath, content }, ['path', 'content']);
  assertValid(paramValidation, (msg) => Errors.invalidParams(msg));

  const extensionValidation = validateMarkdownExtension(notePath);
  assertValid(extensionValidation, (msg) => Errors.invalidParams(msg, { path: notePath }));

  await execCli('create', { path: notePath, content, overwrite: true });
  return notePath;
}

export async function deleteNote(vaultPath, notePath) {
  const paramValidation = validateRequiredParameters({ path: notePath }, ['path']);
  assertValid(paramValidation, (msg) => Errors.invalidParams(msg));

  const extensionValidation = validateMarkdownExtension(notePath);
  assertValid(extensionValidation, (msg) => Errors.invalidParams(msg, { path: notePath }));

  await execCli('delete', { file: notePath, permanent: true });
  return notePath;
}

export async function searchByTags(vaultPath, searchTags, directory = null, caseSensitive = false) {
  // Query each tag and intersect results
  const tagSets = [];

  for (const tag of searchTags) {
    const result = await execCli('tag', { name: tag });
    const files = parseFileList(result.stdout);
    tagSets.push(new Set(files));
  }

  // Intersect all sets
  let intersection = tagSets[0] || new Set();
  for (let i = 1; i < tagSets.length; i++) {
    intersection = new Set([...intersection].filter(f => tagSets[i].has(f)));
  }

  const results = [...intersection]
    .filter(f => !directory || f.startsWith(directory))
    .sort()
    .map(f => ({ path: f, tags: searchTags }));

  return {
    notes: results,
    count: results.length,
  };
}

export async function getNoteMetadata(vaultPath, notePath, options = {}) {
  // CLI property commands work on a single note; for batch mode fall back to fs
  throw new Error('CLI_FALLBACK');
}

export async function discoverMocs(vaultPath, options = {}) {
  // No direct CLI equivalent — fall back to fs
  throw new Error('CLI_FALLBACK');
}

export async function readSection(vaultPath, notePath, options = {}) {
  // CLI outline command can help but doesn't extract section content — fall back to fs
  throw new Error('CLI_FALLBACK');
}

export async function patchNote(vaultPath, notePath, oldString, newString, replaceAll = false) {
  // CLI has append/prepend but no find-replace — fall back to fs
  throw new Error('CLI_FALLBACK');
}

export async function toggleCheckbox(vaultPath, notePath, text, checked) {
  const paramValidation = validateRequiredParameters({ path: notePath, text, checked }, ['path', 'text', 'checked']);
  assertValid(paramValidation, (msg) => Errors.invalidParams(msg));

  // Use CLI task command — requires knowing the line reference
  // For now, fall back to fs for complex text matching
  throw new Error('CLI_FALLBACK');
}
