import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { execFile } from 'node:child_process';

// Mock child_process before importing the module
vi.mock('node:child_process', () => ({
  execFile: vi.fn(),
}));

// Must import after mock
const { detectCli, resolveVaultName, execCli, resetDetection } = await import('../../src/cli/executor.js');

describe('CLI Executor', () => {
  beforeEach(() => {
    resetDetection();
    vi.clearAllMocks();
  });

  describe('detectCli', () => {
    it('should detect CLI when binary exists and responds', async () => {
      execFile
        .mockImplementationOnce((cmd, args, opts, cb) => {
          // which obsidian
          cb(null, '/usr/local/bin/obsidian\n', '');
        })
        .mockImplementationOnce((cmd, args, opts, cb) => {
          // obsidian version
          cb(null, '1.12.0\n', '');
        });

      const result = await detectCli();
      expect(result.available).toBe(true);
      expect(result.version).toBe('1.12.0');
    });

    it('should return unavailable when binary not found', async () => {
      execFile.mockImplementationOnce((cmd, args, opts, cb) => {
        cb(new Error('not found'), '', 'obsidian not found');
      });

      const result = await detectCli();
      expect(result.available).toBe(false);
      expect(result.version).toBe(null);
    });

    it('should cache detection results', async () => {
      execFile
        .mockImplementationOnce((cmd, args, opts, cb) => cb(null, '/usr/local/bin/obsidian\n', ''))
        .mockImplementationOnce((cmd, args, opts, cb) => cb(null, '1.12.0\n', ''));

      await detectCli();
      await detectCli();

      // Should only call execFile twice (which + version), not four times
      expect(execFile).toHaveBeenCalledTimes(2);
    });

    it('should return unavailable when version check fails', async () => {
      execFile
        .mockImplementationOnce((cmd, args, opts, cb) => cb(null, '/usr/local/bin/obsidian\n', ''))
        .mockImplementationOnce((cmd, args, opts, cb) => {
          const err = new Error('exit 1');
          err.code = 1;
          cb(err, '', 'not running');
        });

      const result = await detectCli();
      expect(result.available).toBe(false);
    });
  });

  describe('resolveVaultName', () => {
    it('should parse vault name from vaults output', async () => {
      execFile.mockImplementationOnce((cmd, args, opts, cb) => {
        cb(null, 'MyVault  /Users/test/vault\nOther  /Users/test/other', '');
      });

      const name = await resolveVaultName('/Users/test/vault');
      expect(name).toBe('MyVault');
    });

    it('should fallback to directory basename', async () => {
      execFile.mockImplementationOnce((cmd, args, opts, cb) => {
        cb(new Error('failed'), '', '');
      });

      const name = await resolveVaultName('/Users/test/my-vault');
      expect(name).toBe('my-vault');
    });
  });

  describe('execCli', () => {
    it('should build correct arguments', async () => {
      resetDetection();

      execFile.mockImplementationOnce((cmd, args, opts, cb) => {
        cb(null, 'result output', '');
      });

      const result = await execCli('search', { query: 'hello', limit: 10 });

      expect(execFile).toHaveBeenCalledWith(
        'obsidian',
        expect.arrayContaining(['search', 'query=hello', 'limit=10']),
        expect.any(Object),
        expect.any(Function)
      );
      expect(result.stdout).toBe('result output');
    });

    it('should handle boolean flags', async () => {
      execFile.mockImplementationOnce((cmd, args, opts, cb) => {
        cb(null, 'ok', '');
      });

      await execCli('create', { name: 'test', open: true, overwrite: false });

      const callArgs = execFile.mock.calls[0][1];
      expect(callArgs).toContain('open');
      expect(callArgs).not.toContain('overwrite');
    });

    it('should throw CliExecutionError on non-zero exit', async () => {
      execFile.mockImplementationOnce((cmd, args, opts, cb) => {
        const err = new Error('exit 1');
        err.code = 1;
        cb(err, '', 'something went wrong');
      });

      await expect(execCli('read', { path: 'test.md' }))
        .rejects.toThrow('something went wrong');
    });

    it('should throw ObsidianNotRunningError when app not running', async () => {
      execFile.mockImplementationOnce((cmd, args, opts, cb) => {
        const err = new Error('exit 1');
        err.code = 1;
        cb(err, '', 'Obsidian is not running');
      });

      await expect(execCli('read', { path: 'test.md' }))
        .rejects.toThrow('Obsidian app is not running');
    });

    // Obsidian CLI reports domain errors (e.g. file-not-found) via exit 0 with
    // an "Error: ..." line on stdout. Callers used to misparse that as data;
    // execCli now surfaces it as an execution error.
    it('should throw on "Error:" prefix in stdout even when exit code is 0', async () => {
      execFile.mockImplementationOnce((cmd, args, opts, cb) => {
        cb(null, 'Error: File "missing.md" not found.\n', '');
      });

      await expect(execCli('read', { path: 'missing.md' }))
        .rejects.toThrow(/File "missing\.md" not found/);
    });
  });
});
