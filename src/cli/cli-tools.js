/**
 * CLI-only tool implementations.
 *
 * These tools have no filesystem fallback — they are only available
 * when the Obsidian CLI is detected at startup.
 */

import { execCli } from './executor.js';
import { Errors } from '../errors.js';
import { validateRequiredParameters, validateMarkdownExtension } from '../validation.js';

function assertValid(validationResult, errorFactory) {
  if (!validationResult.valid) {
    throw errorFactory(validationResult.error, validationResult);
  }
}

function parseFileList(stdout) {
  if (!stdout) return [];
  return stdout.split('\n').map(l => l.trim()).filter(Boolean);
}

/**
 * Get all notes that link to a specific note.
 */
export async function getBacklinks(vaultPath, notePath) {
  const paramValidation = validateRequiredParameters({ path: notePath }, ['path']);
  assertValid(paramValidation, (msg) => Errors.invalidParams(msg));

  const result = await execCli('backlinks', { file: notePath, format: 'json' });

  let backlinks;
  try {
    backlinks = JSON.parse(result.stdout);
  } catch {
    backlinks = parseFileList(result.stdout).map(f => ({ path: f }));
  }

  return {
    backlinks: Array.isArray(backlinks) ? backlinks : [],
    count: Array.isArray(backlinks) ? backlinks.length : 0,
  };
}

/**
 * Find notes with no incoming links.
 */
export async function getOrphans(vaultPath) {
  const result = await execCli('orphans', {});
  const orphans = parseFileList(result.stdout);

  return {
    orphans: orphans.map(f => ({ path: f })),
    count: orphans.length,
  };
}

/**
 * Find notes with no outgoing links.
 */
export async function getDeadends(vaultPath) {
  const result = await execCli('deadends', {});
  const deadends = parseFileList(result.stdout);

  return {
    deadends: deadends.map(f => ({ path: f })),
    count: deadends.length,
  };
}

/**
 * Read today's daily note content.
 */
export async function getDailyNote(vaultPath) {
  const pathResult = await execCli('daily:path', {});
  const dailyPath = pathResult.stdout.trim();

  let content;
  try {
    const readResult = await execCli('daily:read', {});
    content = readResult.stdout;
  } catch {
    content = null;
  }

  return {
    path: dailyPath,
    content,
    exists: content !== null,
  };
}

/**
 * Append content to today's daily note.
 */
export async function appendToDailyNote(vaultPath, content) {
  const paramValidation = validateRequiredParameters({ content }, ['content']);
  assertValid(paramValidation, (msg) => Errors.invalidParams(msg));

  await execCli('daily:append', { content });

  const pathResult = await execCli('daily:path', {});

  return {
    path: pathResult.stdout.trim(),
    success: true,
  };
}

/**
 * Move a note to a new location (with automatic link updates).
 */
export async function moveNote(vaultPath, notePath, toPath) {
  const paramValidation = validateRequiredParameters({ path: notePath, to: toPath }, ['path', 'to']);
  assertValid(paramValidation, (msg) => Errors.invalidParams(msg));

  await execCli('move', { file: notePath, to: toPath });

  return {
    oldPath: notePath,
    newPath: toPath,
  };
}

/**
 * Rename a note (with automatic link updates).
 */
export async function renameNote(vaultPath, notePath, newName) {
  const paramValidation = validateRequiredParameters({ path: notePath, name: newName }, ['path', 'name']);
  assertValid(paramValidation, (msg) => Errors.invalidParams(msg));

  await execCli('rename', { file: notePath, name: newName });

  return {
    oldPath: notePath,
    newName,
  };
}

/**
 * List available templates.
 */
export async function listTemplates(vaultPath) {
  const result = await execCli('templates', {});
  const templates = parseFileList(result.stdout);

  return {
    templates: templates.map(t => ({ name: t })),
    count: templates.length,
  };
}

/**
 * Read a template's content.
 */
export async function readTemplate(vaultPath, templateName, resolve = false) {
  const paramValidation = validateRequiredParameters({ name: templateName }, ['name']);
  assertValid(paramValidation, (msg) => Errors.invalidParams(msg));

  const args = { name: templateName };
  if (resolve) args.resolve = true;

  const result = await execCli('template:read', args);

  return {
    name: templateName,
    content: result.stdout,
  };
}

/**
 * List all tasks in the vault.
 */
export async function listTasks(vaultPath, options = {}) {
  const args = {};
  if (options.done) args.done = true;
  if (options.todo) args.todo = true;
  if (options.daily) args.daily = true;
  args.format = 'json';

  const result = await execCli('tasks', args);

  let tasks;
  try {
    tasks = JSON.parse(result.stdout);
  } catch {
    // Fallback: parse line-based output
    const lines = parseFileList(result.stdout);
    tasks = lines.map(line => {
      const checkMatch = line.match(/^- \[([ x])\]\s*(.*)$/);
      if (checkMatch) {
        return { text: checkMatch[2], checked: checkMatch[1] === 'x' };
      }
      return { text: line, checked: false };
    });
  }

  return {
    tasks: Array.isArray(tasks) ? tasks : [],
    count: Array.isArray(tasks) ? tasks.length : 0,
  };
}
