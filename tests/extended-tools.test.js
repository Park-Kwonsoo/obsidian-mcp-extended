import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { readSection, patchNote, toggleCheckbox } from '../src/tools.js';
import { writeFile, mkdir, rm } from 'fs/promises';
import path from 'path';
import os from 'os';

const TEST_VAULT = path.join(os.tmpdir(), 'test-vault-extended');

const SAMPLE_NOTE = `---
tags:
  - planning
---

# Sample Planning Document

## 개요

이 문서는 테스트용 플래닝 문서입니다.

## TODO

- [ ] 작업 A: UserService 구현
- [ ] 작업 B: API 엔드포인트 추가
- [x] 작업 C: 데이터베이스 스키마 정의

## 세부 작업

### UserService 구현

UserRepository에서 userId로 조회하는 로직을 추가한다.

### API 엔드포인트

GET /api/users/:id 엔드포인트를 추가한다.

## 참고 자료

- PRD 링크
- 관련 문서
`;

async function setupTestVault() {
  await mkdir(TEST_VAULT, { recursive: true });
  await writeFile(path.join(TEST_VAULT, 'sample.md'), SAMPLE_NOTE, 'utf-8');
}

async function cleanupTestVault() {
  await rm(TEST_VAULT, { recursive: true, force: true });
}

describe('readSection', () => {
  beforeEach(setupTestVault);
  afterEach(cleanupTestVault);

  it('should read a section by heading', async () => {
    const result = await readSection(TEST_VAULT, 'sample.md', { heading: 'TODO' });
    expect(result.content).toContain('- [ ] 작업 A');
    expect(result.content).toContain('- [x] 작업 C');
    expect(result.content).not.toContain('### UserService');
    expect(result.totalLines).toBeGreaterThan(0);
  });

  it('should read a sub-section by heading', async () => {
    const result = await readSection(TEST_VAULT, 'sample.md', { heading: 'UserService 구현' });
    expect(result.content).toContain('UserRepository');
    expect(result.content).not.toContain('GET /api/users');
  });

  it('should read by line range', async () => {
    const result = await readSection(TEST_VAULT, 'sample.md', { startLine: 1, endLine: 5 });
    expect(result.startLine).toBe(1);
    expect(result.endLine).toBe(5);
    expect(result.content).toContain('---');
  });

  it('should error if heading not found', async () => {
    await expect(
      readSection(TEST_VAULT, 'sample.md', { heading: 'NonExistent' })
    ).rejects.toThrow('not found');
  });

  it('should error if no heading or line range given', async () => {
    await expect(
      readSection(TEST_VAULT, 'sample.md', {})
    ).rejects.toThrow('Either heading or startLine');
  });
});

describe('patchNote', () => {
  beforeEach(setupTestVault);
  afterEach(cleanupTestVault);

  it('should replace a unique string', async () => {
    const result = await patchNote(TEST_VAULT, 'sample.md', '작업 A: UserService 구현', '작업 A: UserService 완료');
    expect(result.totalReplacements).toBe(1);
    expect(result.changedLines.length).toBe(1);
  });

  it('should error on non-existent string', async () => {
    await expect(
      patchNote(TEST_VAULT, 'sample.md', 'this does not exist', 'replacement')
    ).rejects.toThrow('not found');
  });

  it('should error on ambiguous match without replaceAll', async () => {
    // "작업" appears multiple times
    await expect(
      patchNote(TEST_VAULT, 'sample.md', '작업', 'task')
    ).rejects.toThrow('occurrences');
  });

  it('should replace all with replaceAll flag', async () => {
    const result = await patchNote(TEST_VAULT, 'sample.md', '- [ ]', '- [x]', true);
    expect(result.totalReplacements).toBe(2);
  });
});

describe('toggleCheckbox', () => {
  beforeEach(setupTestVault);
  afterEach(cleanupTestVault);

  it('should check an unchecked checkbox', async () => {
    const result = await toggleCheckbox(TEST_VAULT, 'sample.md', 'UserService 구현', true);
    expect(result.before).toContain('- [ ]');
    expect(result.after).toContain('- [x]');
    expect(result.line).toBeGreaterThan(0);
  });

  it('should uncheck a checked checkbox', async () => {
    const result = await toggleCheckbox(TEST_VAULT, 'sample.md', '데이터베이스 스키마', false);
    expect(result.before).toContain('- [x]');
    expect(result.after).toContain('- [ ]');
  });

  it('should return same line if already in desired state', async () => {
    const result = await toggleCheckbox(TEST_VAULT, 'sample.md', 'UserService 구현', false);
    expect(result.before).toBe(result.after);
  });

  it('should error if checkbox not found', async () => {
    await expect(
      toggleCheckbox(TEST_VAULT, 'sample.md', 'nonexistent task', true)
    ).rejects.toThrow('No checkbox found');
  });
});
