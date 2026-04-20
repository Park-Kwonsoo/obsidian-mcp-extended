import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import Ajv from 'ajv';
import { searchVault, searchByTitle, listNotes, searchByTags } from '../src/backends/filesystem.js';
import { stripSearchContext } from '../src/response-formatter.js';
import { getToolDefinitions } from '../src/toolDefinitions.js';

// Guards against regressions where backend output diverges from declared
// outputSchema — strict MCP clients validate and reject such responses,
// manifesting as `Tool execution failed` with no useful diagnostic.
describe('output schema conformance', () => {
  const vault = path.join(tmpdir(), `schema-conformance-${Date.now()}`);
  const ajv = new Ajv({ strict: false, allErrors: true });
  const schemas = {};

  function assertMatchesSchema(toolName, data) {
    const validate = schemas[toolName];
    const ok = validate(data);
    if (!ok) {
      throw new Error(
        `${toolName} output violates schema:\n${ajv.errorsText(validate.errors, { separator: '\n' })}`
      );
    }
    expect(ok).toBe(true);
  }

  beforeAll(() => {
    mkdirSync(path.join(vault, '📚 Books'), { recursive: true });
    writeFileSync(
      path.join(vault, '📚 Books/note-a.md'),
      '# Note A\n\nfoo bar baz\ntag test\n'
    );
    writeFileSync(
      path.join(vault, 'note-b.md'),
      '---\ntags: [alpha]\n---\n# Note B\n\nfoo line\n#beta\n'
    );

    const exercised = ['search-vault', 'search-by-title', 'list-notes', 'search-by-tags'];
    for (const def of getToolDefinitions(false)) {
      if (def.outputSchema && exercised.includes(def.name)) {
        schemas[def.name] = ajv.compile(def.outputSchema);
      }
    }
  });

  afterAll(() => {
    rmSync(vault, { recursive: true, force: true });
  });

  it('search-vault with context satisfies its outputSchema', async () => {
    const raw = await searchVault(vault, 'foo', undefined, false,
      { includeContext: true, contextLines: 2 }, 100, 0);
    assertMatchesSchema('search-vault', stripSearchContext(raw));
  });

  it('search-vault without context satisfies its outputSchema', async () => {
    const result = await searchVault(vault, 'foo', undefined, false,
      { includeContext: false }, 100, 0);
    assertMatchesSchema('search-vault', result);
  });

  it('search-by-title satisfies its outputSchema', async () => {
    const result = await searchByTitle(vault, 'Note', undefined, false, 100, 0);
    assertMatchesSchema('search-by-title', result);
  });

  it('list-notes satisfies its outputSchema', async () => {
    const result = await listNotes(vault, undefined, 100, 0);
    assertMatchesSchema('list-notes', result);
  });

  it('search-by-tags satisfies its outputSchema', async () => {
    const result = await searchByTags(vault, ['alpha'], undefined, false);
    assertMatchesSchema('search-by-tags', result);
  });
});
