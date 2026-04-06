/**
 * Backend resolver — selects between CLI and filesystem backends,
 * with automatic fallback on CLI failures.
 */

import * as filesystemBackend from './filesystem.js';
import * as cliBackend from './cli.js';
import { detectCli, resolveVaultName, resetDetection } from '../cli/executor.js';
import { CliUnavailableError, CliTimeoutError, CliExecutionError, ObsidianNotRunningError } from '../cli/errors.js';
import { config } from '../config.js';

let cliHealthy = false;
let lastHealthCheck = 0;

/**
 * Initialize backends. Call once at startup.
 *
 * @param {string} vaultPath - Absolute path to the vault
 * @returns {{ cliAvailable: boolean, vaultName: string|null }}
 */
export async function initBackends(vaultPath) {
  const mode = config.cli.enabled;

  if (mode === 'never') {
    return { cliAvailable: false, vaultName: null };
  }

  const detection = await detectCli();

  if (!detection.available) {
    if (mode === 'always') {
      throw new CliUnavailableError('CLI mode is "always" but Obsidian CLI is not available');
    }
    return { cliAvailable: false, vaultName: null };
  }

  const vaultName = await resolveVaultName(vaultPath);
  cliHealthy = true;
  lastHealthCheck = Date.now();

  return { cliAvailable: true, vaultName };
}

/**
 * Check if a tool should prefer CLI backend.
 */
function shouldUseCli(toolName) {
  if (config.cli.enabled === 'never') return false;
  if (!cliHealthy) return false;
  return config.cli.preferCli.includes(toolName);
}

/**
 * Mark CLI as unhealthy after a transient failure.
 * It will be retried after healthCheckInterval.
 */
function markCliUnhealthy() {
  cliHealthy = false;
  lastHealthCheck = Date.now();
}

/**
 * Try to recover CLI health if enough time has passed.
 */
async function maybeRecoverCli() {
  if (cliHealthy) return;
  if (Date.now() - lastHealthCheck < config.cli.healthCheckInterval) return;

  resetDetection();
  const detection = await detectCli();
  if (detection.available) {
    cliHealthy = true;
  }
  lastHealthCheck = Date.now();
}

/**
 * Check if an error is a transient CLI error (worth falling back from).
 */
function isCliTransientError(error) {
  return (
    error instanceof CliTimeoutError ||
    error instanceof CliExecutionError ||
    error instanceof ObsidianNotRunningError ||
    error.message === 'CLI_FALLBACK'
  );
}

/**
 * Execute a tool method with CLI-preferred routing and filesystem fallback.
 *
 * @param {string} toolName - Tool name (e.g., 'search-vault')
 * @param {string} methodName - Method name on the backend (e.g., 'searchVault')
 * @param {Array} args - Arguments to pass to the method
 * @returns {*} Tool result
 */
export async function executeWithFallback(toolName, methodName, args) {
  await maybeRecoverCli();

  if (shouldUseCli(toolName)) {
    try {
      return await cliBackend[methodName](...args);
    } catch (error) {
      if (isCliTransientError(error)) {
        markCliUnhealthy();
        return await filesystemBackend[methodName](...args);
      }
      throw error;
    }
  }

  return await filesystemBackend[methodName](...args);
}

/**
 * Get current backend status (for diagnostics).
 */
export function getBackendStatus() {
  return {
    cliEnabled: config.cli.enabled,
    cliHealthy,
    lastHealthCheck: new Date(lastHealthCheck).toISOString(),
  };
}
