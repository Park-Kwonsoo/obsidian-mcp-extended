/**
 * CLI-specific error types
 */

export class CliUnavailableError extends Error {
  constructor(message = 'Obsidian CLI is not available') {
    super(message);
    this.name = 'CliUnavailableError';
  }
}

export class CliTimeoutError extends Error {
  constructor(command, timeout) {
    super(`CLI command timed out after ${timeout}ms: ${command}`);
    this.name = 'CliTimeoutError';
    this.command = command;
    this.timeout = timeout;
  }
}

export class CliExecutionError extends Error {
  constructor(command, exitCode, stderr) {
    super(`CLI command failed (exit ${exitCode}): ${stderr || command}`);
    this.name = 'CliExecutionError';
    this.command = command;
    this.exitCode = exitCode;
    this.stderr = stderr;
  }
}

export class ObsidianNotRunningError extends Error {
  constructor() {
    super('Obsidian app is not running. CLI requires the app to be open.');
    this.name = 'ObsidianNotRunningError';
  }
}
