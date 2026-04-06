import { describe, it, expect, vi, beforeEach } from 'vitest';

// Mock the executor
vi.mock('../../src/cli/executor.js', () => ({
  execCli: vi.fn(),
  execCliJson: vi.fn(),
}));

const { execCli } = await import('../../src/cli/executor.js');
const {
  getBacklinks,
  getOrphans,
  getDeadends,
  getDailyNote,
  appendToDailyNote,
  moveNote,
  renameNote,
  listTemplates,
  readTemplate,
  listTasks,
} = await import('../../src/cli/cli-tools.js');

describe('CLI Tools', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe('getBacklinks', () => {
    it('should parse backlinks from CLI output', async () => {
      execCli.mockResolvedValue({ stdout: 'note1.md\nnote2.md\n', stderr: '', exitCode: 0 });
      const result = await getBacklinks('/vault', 'target.md');
      expect(result.count).toBe(2);
      expect(result.backlinks).toHaveLength(2);
      expect(result.backlinks[0].path).toBe('note1.md');
    });

    it('should handle JSON output', async () => {
      execCli.mockResolvedValue({
        stdout: JSON.stringify([{ path: 'a.md' }, { path: 'b.md' }]),
        stderr: '', exitCode: 0,
      });
      const result = await getBacklinks('/vault', 'target.md');
      expect(result.count).toBe(2);
    });

    it('should throw on missing path', async () => {
      await expect(getBacklinks('/vault', undefined)).rejects.toThrow();
    });
  });

  describe('getOrphans', () => {
    it('should list orphan files', async () => {
      execCli.mockResolvedValue({ stdout: 'orphan1.md\norphan2.md', stderr: '', exitCode: 0 });
      const result = await getOrphans('/vault');
      expect(result.count).toBe(2);
      expect(result.orphans[0].path).toBe('orphan1.md');
    });

    it('should handle empty result', async () => {
      execCli.mockResolvedValue({ stdout: '', stderr: '', exitCode: 0 });
      const result = await getOrphans('/vault');
      expect(result.count).toBe(0);
    });
  });

  describe('getDeadends', () => {
    it('should list dead-end files', async () => {
      execCli.mockResolvedValue({ stdout: 'deadend.md', stderr: '', exitCode: 0 });
      const result = await getDeadends('/vault');
      expect(result.count).toBe(1);
    });
  });

  describe('getDailyNote', () => {
    it('should read daily note path and content', async () => {
      execCli
        .mockResolvedValueOnce({ stdout: 'Daily/2026-04-05.md', stderr: '', exitCode: 0 })
        .mockResolvedValueOnce({ stdout: '# Daily\n- todo item', stderr: '', exitCode: 0 });

      const result = await getDailyNote('/vault');
      expect(result.path).toBe('Daily/2026-04-05.md');
      expect(result.content).toBe('# Daily\n- todo item');
      expect(result.exists).toBe(true);
    });

    it('should handle missing daily note', async () => {
      execCli
        .mockResolvedValueOnce({ stdout: 'Daily/2026-04-05.md', stderr: '', exitCode: 0 })
        .mockRejectedValueOnce(new Error('not found'));

      const result = await getDailyNote('/vault');
      expect(result.exists).toBe(false);
      expect(result.content).toBe(null);
    });
  });

  describe('appendToDailyNote', () => {
    it('should append content and return path', async () => {
      execCli
        .mockResolvedValueOnce({ stdout: '', stderr: '', exitCode: 0 })
        .mockResolvedValueOnce({ stdout: 'Daily/2026-04-05.md', stderr: '', exitCode: 0 });

      const result = await appendToDailyNote('/vault', '- new task');
      expect(result.success).toBe(true);
      expect(result.path).toBe('Daily/2026-04-05.md');
    });

    it('should throw on missing content', async () => {
      await expect(appendToDailyNote('/vault', undefined)).rejects.toThrow();
    });
  });

  describe('moveNote', () => {
    it('should move note and return paths', async () => {
      execCli.mockResolvedValue({ stdout: '', stderr: '', exitCode: 0 });
      const result = await moveNote('/vault', 'old/path.md', 'new/path.md');
      expect(result.oldPath).toBe('old/path.md');
      expect(result.newPath).toBe('new/path.md');
    });
  });

  describe('renameNote', () => {
    it('should rename note', async () => {
      execCli.mockResolvedValue({ stdout: '', stderr: '', exitCode: 0 });
      const result = await renameNote('/vault', 'note.md', 'new-name');
      expect(result.oldPath).toBe('note.md');
      expect(result.newName).toBe('new-name');
    });
  });

  describe('listTemplates', () => {
    it('should list templates', async () => {
      execCli.mockResolvedValue({ stdout: 'Daily\nMeeting\nProject', stderr: '', exitCode: 0 });
      const result = await listTemplates('/vault');
      expect(result.count).toBe(3);
      expect(result.templates[0].name).toBe('Daily');
    });
  });

  describe('readTemplate', () => {
    it('should read template content', async () => {
      execCli.mockResolvedValue({ stdout: '# {{title}}\n{{date}}', stderr: '', exitCode: 0 });
      const result = await readTemplate('/vault', 'Daily', false);
      expect(result.name).toBe('Daily');
      expect(result.content).toContain('{{title}}');
    });
  });

  describe('listTasks', () => {
    it('should parse JSON task output', async () => {
      const tasks = [
        { text: 'Buy groceries', checked: false },
        { text: 'Review PR', checked: true },
      ];
      execCli.mockResolvedValue({ stdout: JSON.stringify(tasks), stderr: '', exitCode: 0 });

      const result = await listTasks('/vault', { todo: true });
      expect(result.count).toBe(2);
      expect(result.tasks[0].text).toBe('Buy groceries');
    });

    it('should parse markdown task output as fallback', async () => {
      execCli.mockResolvedValue({
        stdout: '- [ ] Buy groceries\n- [x] Review PR',
        stderr: '', exitCode: 0,
      });

      const result = await listTasks('/vault', {});
      expect(result.count).toBe(2);
      expect(result.tasks[0].checked).toBe(false);
      expect(result.tasks[1].checked).toBe(true);
    });
  });
});
