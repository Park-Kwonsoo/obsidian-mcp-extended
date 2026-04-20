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
  let vault;
  let ajv;
  let schemas;

  beforeAll(() => {
    vault = path.join(tmpdir(), `schema-conformance-${Date.now()}`);
    mkdirSync(path.join(vault, '📚 Books'), { recursive: true });
    writeFileSync(
      path.join(vault, '📚 Books/note-a.md'),
      '# Note A\n\nfoo bar baz\ntag test\n'
    );
    writeFileSync(
      path.join(vault, 'note-b.md'),
      '---\ntags: [alpha]\n---\n# Note B\n\nfoo line\n#beta\n'
    );

    ajv = new Ajv({ strict: false, allErrors: true });
    const defs = getToolDefinitions(false);
    schemas = Object.fromEntries(
      defs.filter(d => d.outputSchema).map(d => [d.name, ajv.compile(d.outputSchema)])
    );
  });

  afterAll(() => {
    rmSync(vault, { recursive: true, force: true });
  });

  it('search-vault with context satisfies its outputSchema', async () => {
    const raw = await searchVault(vault, 'foo', undefined, false,
      { includeContext: true, contextLines: 2 }, 100, 0);
    const stripped = stripSearchContext(raw);
    const valid = schemas['search-vault'](stripped);
    expect(schemas['search-vault'].errors, JSON.stringify(schemas['search-vault'].errors)).toBeFalsy();
    expect(valid).toBe(true);
  });

  it('search-vault without context satisfies its outputSchema', async () => {
    const result = await searchVault(vault, 'foo', undefined, false,
      { includeContext: false }, 100, 0);
    const valid = schemas['search-vault'](result);
    expect(schemas['search-vault'].errors, JSON.stringify(schemas['search-vault'].errors)).toBeFalsy();
    expect(valid).toBe(true);
  });

  it('search-by-title satisfies its outputSchema', async () => {
    const result = await searchByTitle(vault, 'Note', undefined, false, 100, 0);
    const valid = schemas['search-by-title'](result);
    expect(schemas['search-by-title'].errors, JSON.stringify(schemas['search-by-title'].errors)).toBeFalsy();
    expect(valid).toBe(true);
  });

  it('list-notes satisfies its outputSchema', async () => {
    const result = await listNotes(vault, undefined, 100, 0);
    const valid = schemas['list-notes'](result);
    expect(schemas['list-notes'].errors, JSON.stringify(schemas['list-notes'].errors)).toBeFalsy();
    expect(valid).toBe(true);
  });

  it('search-by-tags satisfies its outputSchema', async () => {
    const result = await searchByTags(vault, ['alpha'], undefined, false);
    const valid = schemas['search-by-tags'](result);
    expect(schemas['search-by-tags'].errors, JSON.stringify(schemas['search-by-tags'].errors)).toBeFalsy();
    expect(valid).toBe(true);
  });
});
