/**
 * Obsidian CLI command executor
 *
 * Wraps all interaction with the `obsidian` CLI binary.
 * Uses execFile (not exec) to prevent shell injection.
 */

import { execFile } from 'node:child_process';
import { readFile } from 'fs/promises';
import path from 'path';
import { config } from '../config.js';
import {
  CliUnavailableError,
  CliTimeoutError,
  CliExecutionError,
  ObsidianNotRunningError,
} from './errors.js';

let cachedDetection = null;
let cachedVaultName = null;

/**
 * Run a raw command via execFile and return { stdout, stderr, exitCode }
 */
function runCommand(cmd, args = [], timeout = 5000) {
  return new Promise((resolve, reject) => {
    const proc = execFile(cmd, args, { timeout, encoding: 'utf-8' }, (error, stdout, stderr) => {
      if (error && error.killed) {
        reject(new CliTimeoutError(`${cmd} ${args.join(' ')}`, timeout));
        return;
      }
      resolve({
        stdout: (stdout || '').trim(),
        stderr: (stderr || '').trim(),
        exitCode: error ? error.code ?? 1 : 0,
      });
    });
  });
}

/**
 * Detect if the Obsidian CLI is available and the app is running.
 * Caches result for process lifetime (use resetDetection() to clear).
 *
 * @returns {{ available: boolean, version: string|null }}
 */
export async function detectCli() {
  if (cachedDetection !== null) return cachedDetection;

  try {
    // Check binary exists
    const which = await runCommand('which', ['obsidian'], 3000);
    if (which.exitCode !== 0) {
      cachedDetection = { available: false, version: null };
      return cachedDetection;
    }

    // Check version (also verifies app is responsive)
    const ver = await runCommand('obsidian', ['version'], 5000);
    if (ver.exitCode !== 0) {
      cachedDetection = { available: false, version: null };
      return cachedDetection;
    }

    cachedDetection = { available: true, version: ver.stdout };
    return cachedDetection;
  } catch {
    cachedDetection = { available: false, version: null };
    return cachedDetection;
  }
}

/**
 * Reset cached detection (e.g., for health-check retry)
 */
export function resetDetection() {
  cachedDetection = null;
  cachedVaultName = null;
}

/**
 * Resolve vault path to vault name.
 * Tries: 1) `obsidian vaults` output matching, 2) directory basename fallback
 *
 * @param {string} vaultPath - Absolute path to the vault
 * @returns {string} vault name
 */
export async function resolveVaultName(vaultPath) {
  if (cachedVaultName) return cachedVaultName;

  const normalizedPath = path.resolve(vaultPath);

  try {
    const result = await runCommand('obsidian', ['vaults'], 5000);
    if (result.exitCode === 0 && result.stdout) {
      // Parse vault list — each line is typically "VaultName  /path/to/vault"
      const lines = result.stdout.split('\n');
      for (const line of lines) {
        // Match line containing the vault path
        if (line.includes(normalizedPath)) {
          // Extract vault name (first column before whitespace or path)
          const name = line.split(/\s{2,}/)[0]?.trim();
          if (name) {
            cachedVaultName = name;
            return cachedVaultName;
          }
        }
      }
    }
  } catch {
    // Fall through to basename fallback
  }

  // Fallback: use directory name
  cachedVaultName = path.basename(normalizedPath);
  return cachedVaultName;
}

/**
 * Build CLI argument list from an args object.
 * { query: "hello", limit: 10, open: true } → ["query=hello", "limit=10", "open"]
 */
function buildArgList(args) {
  const result = [];
  for (const [key, value] of Object.entries(args)) {
    if (value === undefined || value === null) continue;
    if (value === true) {
      result.push(key); // boolean flag
    } else if (value === false) {
      continue; // skip false flags
    } else {
      result.push(`${key}=${String(value)}`);
    }
  }
  return result;
}

/**
 * Execute an Obsidian CLI command.
 *
 * @param {string} command - CLI command (e.g., 'read', 'search', 'daily:append')
 * @param {Object} args - Key-value arguments
 * @param {Object} [options] - Execution options
 * @param {string} [options.vault] - Vault name override
 * @param {number} [options.timeout] - Timeout in ms
 * @returns {{ stdout: string, stderr: string, exitCode: number }}
 */
export async function execCli(command, args = {}, options = {}) {
  const timeout = options.timeout || config.cli.timeout;
  const vault = options.vault || cachedVaultName;

  const cliArgs = [command];
  if (vault) {
    cliArgs.unshift(`vault=${vault}`);
  }
  cliArgs.push(...buildArgList(args));

  const result = await runCommand('obsidian', cliArgs, timeout);

  if (result.exitCode !== 0) {
    // Detect "Obsidian is not running" pattern
    const errMsg = result.stderr.toLowerCase();
    if (errMsg.includes('not running') || errMsg.includes('connect') || errMsg.includes('refused')) {
      throw new ObsidianNotRunningError();
    }
    throw new CliExecutionError(
      `obsidian ${cliArgs.join(' ')}`,
      result.exitCode,
      result.stderr
    );
  }

  // Obsidian CLI reports errors (file not found, invalid args) as exit 0 with
  // `Error: <message>` on stdout. Without this guard callers would misparse the
  // error string as data — e.g. get-backlinks was fabricating a backlink whose
  // path was the error message itself.
  if (/^Error:\s/.test(result.stdout)) {
    throw new CliExecutionError(
      `obsidian ${cliArgs.join(' ')}`,
      0,
      result.stdout
    );
  }

  return result;
}

/**
 * Execute CLI command and parse stdout as JSON.
 * Falls back to raw string if JSON parsing fails.
 *
 * @param {string} command - CLI command
 * @param {Object} args - Arguments (format=json is auto-added)
 * @param {Object} [options] - Execution options
 * @returns {Object|string} Parsed JSON or raw string
 */
export async function execCliJson(command, args = {}, options = {}) {
  const result = await execCli(command, { ...args, format: 'json' }, options);
  try {
    return JSON.parse(result.stdout);
  } catch {
    return result.stdout;
  }
}
