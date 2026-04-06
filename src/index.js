#!/usr/bin/env node

import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { createServer } from './server.js';
import { initBackends } from './backends/resolver.js';

// Get vault path from command line args
const vaultPath = process.argv[2];
if (!vaultPath) {
  console.error('Usage: node index.js <vault-path>');
  process.exit(1);
}

// Initialize backends (detect CLI availability)
const { cliAvailable, vaultName } = await initBackends(vaultPath);

if (cliAvailable) {
  console.error(`Obsidian CLI detected (vault: ${vaultName}), hybrid mode active`);
} else {
  console.error('Obsidian CLI not detected, using filesystem-only mode');
}

const server = createServer(vaultPath, { cliAvailable });

// Start the server
const transport = new StdioServerTransport();
await server.connect(transport);
