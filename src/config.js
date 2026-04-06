/**
 * Configuration constants for the MCP server
 */

export const config = {
  // File size limits
  limits: {
    maxFileSize: 10 * 1024 * 1024, // 10MB max file size
    maxSearchResults: 100, // Maximum number of search results (reduced from 1000 to prevent context explosion)
    maxConcurrentReads: 10, // Maximum concurrent file reads
  },

  // Timeout settings
  timeouts: {
    fileOperation: 30000, // 30 seconds for file operations
    searchOperation: 60000, // 60 seconds for search operations
  },

  // Security settings
  security: {
    allowedExtensions: ['.md'],
    sanitizeContent: true,
  },

  // Obsidian CLI settings
  cli: {
    enabled: process.env.OBSIDIAN_MCP_CLI || 'auto', // 'auto' | 'always' | 'never'
    timeout: 10000, // 10 seconds default for CLI commands
    healthCheckInterval: 60000, // Re-check CLI availability every 60s after failure
    preferCli: [
      'search-vault',
      'search-by-tags',
      'list-notes',
      'toggle-checkbox',
    ],
  },
};

// formatFileSize has been moved to functional/validation.js