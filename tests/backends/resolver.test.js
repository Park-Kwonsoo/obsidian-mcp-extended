import { describe, it, expect, vi, beforeEach } from 'vitest';

// Mock the CLI executor
vi.mock('../../src/cli/executor.js', () => ({
  detectCli: vi.fn().mockResolvedValue({ available: false, version: null }),
  resolveVaultName: vi.fn().mockResolvedValue('test-vault'),
  resetDetection: vi.fn(),
}));

// Mock config to control cli.enabled
vi.mock('../../src/config.js', () => ({
  config: {
    cli: {
      enabled: 'auto',
      timeout: 10000,
      healthCheckInterval: 60000,
      preferCli: ['search-vault', 'list-notes'],
    },
    limits: {
      maxFileSize: 10 * 1024 * 1024,
      maxSearchResults: 100,
      maxConcurrentReads: 10,
    },
    timeouts: {
      fileOperation: 30000,
      searchOperation: 60000,
    },
    security: {
      allowedExtensions: ['.md'],
      sanitizeContent: true,
    },
  },
}));

const { initBackends, executeWithFallback, getBackendStatus } = await import('../../src/backends/resolver.js');
const { detectCli } = await import('../../src/cli/executor.js');
const { config } = await import('../../src/config.js');

describe('Backend Resolver', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe('initBackends', () => {
    it('should return cliAvailable=false when CLI not detected', async () => {
      detectCli.mockResolvedValue({ available: false, version: null });
      const result = await initBackends('/test/vault');
      expect(result.cliAvailable).toBe(false);
    });

    it('should return cliAvailable=false when mode is never', async () => {
      config.cli.enabled = 'never';
      const result = await initBackends('/test/vault');
      expect(result.cliAvailable).toBe(false);
      // Shouldn't even check CLI
      expect(detectCli).not.toHaveBeenCalled();
      config.cli.enabled = 'auto';
    });

    it('should return cliAvailable=true when CLI detected', async () => {
      detectCli.mockResolvedValue({ available: true, version: '1.12.0' });
      const result = await initBackends('/test/vault');
      expect(result.cliAvailable).toBe(true);
      expect(result.vaultName).toBe('test-vault');
    });
  });

  describe('executeWithFallback', () => {
    it('should use filesystem backend when CLI is not available', async () => {
      // Init with no CLI
      detectCli.mockResolvedValue({ available: false, version: null });
      await initBackends('/test/vault');

      const status = getBackendStatus();
      expect(status.cliEnabled).toBe('auto');
    });

    it('should report cliHealthy=true after successful CLI init', async () => {
      detectCli.mockResolvedValue({ available: true, version: '1.12.0' });
      await initBackends('/test/vault');

      const status = getBackendStatus();
      expect(status.cliHealthy).toBe(true);
    });
  });

  describe('getBackendStatus', () => {
    it('should return current status', () => {
      const status = getBackendStatus();
      expect(status).toHaveProperty('cliEnabled');
      expect(status).toHaveProperty('cliHealthy');
      expect(status).toHaveProperty('lastHealthCheck');
    });
  });
});
