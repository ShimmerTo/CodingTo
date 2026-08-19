import type { ExtensionAPI } from '@earendil-works/pi-coding-agent';
import { mkdir, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { updateProjectRules } from './project_rules';
import { MAX_USER_MEMORY_BYTES, patchUserMemory } from './user_memory';

let injected = false;

const IMMUTABLE_MEMORY_GUARD = `# CodingTo User Memory Guard

- 只记录用户明确要求长期记住、或在多个任务中重复出现，并且可跨项目复用的用户偏好。
- 不记录当前项目、技术栈、客户、一次性要求、客户原话、敏感信息或可从代码读取的内容；不确定时不写入。
- 更新前保留未涉及的已有偏好；优先使用 codingto_memory_patch_user 做增删，只有用户明确要求整体替换时才使用 codingto_memory_update_user。`;

const DEFAULT_MEMORY_PROMPT = `# CodingTo Memory Policy

Memory 分为三层：

- User Memory：仅保存从多个任务中推断出的跨项目通用偏好或注意事项，或在多个任务中反复出现的同一偏好。
- Project History：位于当前项目 .codingto/history，仅在任务确实需要历史信息时调用 codingto_memory_search 读取。
- Project Rules：位于当前工作目录 AGENTS.md 的”项目规则”小节；每次任务完成前检查，只有形成长期有效的项目注意事项时才调用 codingto_memory_update_project_rules。
- 正常任务先读取真实代码和当前项目状态，不把历史记录作为推理前提。
- 不要保存项目事实、一次性要求、客户原话、敏感信息或可从代码读取的内容。不确定时不写入。
- 更新 User Memory 优先调用 codingto_memory_patch_user，仅增删简短条目并保留已有内容；只有用户明确要求整体替换时才调用 codingto_memory_update_user。
- 仅当任务完成且形成值得长期保留的项目改动时，调用 codingto_memory_write_history；记录应保留文件路径、技术标识符、关键词、原因和最终方案。普通或无持久价值的任务不要写入历史。`;

function result(value: any) {
  return { content: [{ type: 'text', text: JSON.stringify(value, null, 2) }], details: value };
}

async function readText(file: string) {
  try { return await readFile(file, 'utf8'); } catch { return ''; }
}

async function memoryPrompt() {
  const file = String(process.env.CODINGTO_MEMORY_PROMPT_PATH || '').trim();
  if (!file) return DEFAULT_MEMORY_PROMPT;
  try { return (await readFile(file, 'utf8')).trim(); } catch { return DEFAULT_MEMORY_PROMPT; }
}

function historyRoot() {
  return path.join(path.resolve(process.cwd()), '.codingto', 'history');
}

function historyLimit() {
  const parsed = Number.parseInt(String(process.env.CODINGTO_PROJECT_HISTORY_LIMIT || '100'), 10);
  return Number.isFinite(parsed) ? Math.max(1, Math.min(10000, parsed)) : 100;
}

function safeTitle(value: string) {
  const cleaned = value.trim().replace(/[<>:"/\\|?*\x00-\x1f]/g, '_').replace(/\s+/g, '_').replace(/_+/g, '_');
  return (cleaned || 'project_history').slice(0, 80);
}

function timestamp(date = new Date()) {
  const two = (value: number) => String(value).padStart(2, '0');
  return `${date.getFullYear()}${two(date.getMonth() + 1)}${two(date.getDate())}_${two(date.getHours())}${two(date.getMinutes())}${two(date.getSeconds())}`;
}

function createExclusiveError(error: any) {
  return String(error?.code || '') === 'EEXIST';
}

async function historyFiles() {
  let entries: any[] = [];
  try { entries = await readdir(historyRoot(), { withFileTypes: true }); } catch { return []; }
  return entries
    .filter((entry) => entry.isFile() && entry.name.toLowerCase().endsWith('.md'))
    .map((entry) => String(entry.name))
    .sort((a, b) => b.localeCompare(a));
}

async function pruneHistory() {
  const files = await historyFiles();
  for (const file of files.slice(historyLimit())) {
    await rm(path.join(historyRoot(), file), { force: true });
  }
}

function excerpt(content: string, query: string) {
  const index = content.toLocaleLowerCase().indexOf(query);
  const start = Math.max(0, index - 1200);
  const end = Math.min(content.length, index + query.length + 2800);
  return `${start > 0 ? '…\n' : ''}${content.slice(start, end)}${end < content.length ? '\n…' : ''}`;
}

function listSection(title: string, values: unknown) {
  const items = Array.isArray(values) ? values.map(String).map((item) => item.trim()).filter(Boolean) : [];
  return items.length ? `\n## ${title}\n${items.map((item) => `- ${item}`).join('\n')}\n` : '';
}

export default function registerMemory(api: ExtensionAPI) {
  api.registerTool({
    name: 'codingto_memory_search',
    description: '全文搜索当前项目 .codingto/history 中的 Markdown 记录，返回匹配文件及关键词附近的摘录。',
    parameters: {
      type: 'object',
      properties: {
        query: { type: 'string', description: '要搜索的技术标识符或关键词，如 WSHandler、internal/ws/handler.go、nginx。' },
        limit: { type: 'number', description: '最多返回多少条，默认 10，最大 50。' },
      },
      required: ['query'],
    },
    async execute(_id: string, params: any) {
      const query = String(params.query || '').trim().toLocaleLowerCase();
      if (!query) return result({ ok: false, error: 'query is required' });
      const max = Math.max(1, Math.min(50, Number(params.limit) || 10));
      const matches: any[] = [];
      let outputBytes = 0;
      for (const file of await historyFiles()) {
        const content = await readText(path.join(historyRoot(), file));
        if (!content.toLocaleLowerCase().includes(query)) continue;
        const snippet = excerpt(content, query);
        const snippetBytes = Buffer.byteLength(snippet);
        if (matches.length > 0 && outputBytes + snippetBytes > 32 * 1024) break;
        matches.push({ file: path.join(historyRoot(), file), excerpt: snippet });
        outputBytes += snippetBytes;
        if (matches.length >= max || outputBytes >= 32 * 1024) break;
      }
      return result({ ok: true, query: params.query, count: matches.length, historyDir: historyRoot(), matches });
    },
  });

  api.registerTool({
    name: 'codingto_memory_update_project_rules',
    description: '任务完成前，将当前项目长期有效的注意事项、易错点或通用规范以短句去重追加到工作目录 AGENTS.md 的项目规则小节。不要记录临时要求、客户原话或敏感信息。',
    parameters: {
      type: 'object',
      properties: {
        rules: { type: 'array', items: { type: 'string' }, description: '最多 8 条简短、可复用的项目规则；每条建议不超过 512 字节。' },
      },
      required: ['rules'],
    },
    async execute(_id: string, params: any) {
      const update = await updateProjectRules(params?.rules);
      return result({ ok: true, ...update });
    },
  });

  api.registerTool({
    name: 'codingto_memory_write_history',
    description: '写入一条结构化项目历史，随后按全局配置清理超过保留数的旧记录。',
    parameters: {
      type: 'object',
      properties: {
        title: { type: 'string', description: '简短标题。' },
        summary: { type: 'string', description: '本次改动摘要。' },
        files: { type: 'array', items: { type: 'string' }, description: '涉及文件路径。' },
        symbols: { type: 'array', items: { type: 'string' }, description: '类、方法、函数、路由等技术标识符。' },
        keywords: { type: 'array', items: { type: 'string' }, description: '功能与技术关键词。' },
        cause: { type: 'string', description: '问题原因。' },
        solution: { type: 'string', description: '最终方案。' },
      },
      required: ['title', 'summary', 'files', 'symbols', 'keywords', 'cause', 'solution'],
    },
    async execute(_id: string, params: any) {
      await mkdir(historyRoot(), { recursive: true, mode: 0o700 });
      const title = safeTitle(String(params.title || ''));
      const base = `${timestamp()}_${title}`;
      let finalPath = path.join(historyRoot(), `${base}.md`);
      const content = [
        `# ${String(params.title || '').trim() || '项目历史'}`,
        '',
        '## 摘要',
        String(params.summary || '').trim(),
        listSection('涉及文件', params.files),
        listSection('技术标识符', params.symbols),
        listSection('关键词', params.keywords),
        '\n## 原因',
        String(params.cause || '').trim(),
        '',
        '## 最终方案',
        String(params.solution || '').trim(),
        '',
      ].join('\n').replace(/\n{3,}/g, '\n\n');
      for (let suffix = 0; ; suffix += 1) {
        if (suffix > 0) finalPath = path.join(historyRoot(), `${base}_${suffix}.md`);
        try {
          await writeFile(finalPath, content, { encoding: 'utf8', mode: 0o600, flag: 'wx' });
          break;
        } catch (error) {
          if (!createExclusiveError(error) || suffix >= 99) throw error;
        }
      }
      await pruneHistory();
      return result({ ok: true, file: finalPath, retained: historyLimit() });
    },
  });

  api.registerTool({
    name: 'codingto_memory_patch_user',
    description: '安全增删全局 User Memory 的简短偏好条目，默认保留未涉及的已有内容。仅用于长期、稳定、跨项目适用的用户偏好；不要写入项目事实、临时要求、客户原话或敏感信息。',
    parameters: {
      type: 'object',
      properties: {
        add: { type: 'array', items: { type: 'string' }, description: '最多 8 条要新增的简短用户偏好。' },
        remove: { type: 'array', items: { type: 'string' }, description: '最多 8 条要删除的已有偏好，按条目文本匹配。' },
      },
    },
    async execute(_id: string, params: any) {
      const update = await patchUserMemory(params?.add, params?.remove);
      return result({ ok: true, ...update });
    },
  });

  api.registerTool({
    name: 'codingto_memory_update_user',
    description: '用完整 Markdown 替换全局 User Memory；仅在用户明确要求整体替换时使用，普通偏好更新请使用 codingto_memory_patch_user。',
    parameters: {
      type: 'object',
      properties: { content: { type: 'string', description: '替换后的完整 User Memory Markdown，最多 8 KiB。' } },
      required: ['content'],
    },
    async execute(_id: string, params: any) {
      const file = String(process.env.CODINGTO_USER_MEMORY_PATH || '').trim();
      if (!file) return result({ ok: false, error: 'user memory path is not configured' });
      const content = String(params.content || '').trim();
      if (Buffer.byteLength(content) > MAX_USER_MEMORY_BYTES) return result({ ok: false, error: `user memory exceeds ${MAX_USER_MEMORY_BYTES} bytes` });
      await mkdir(path.dirname(file), { recursive: true, mode: 0o700 });
      await writeFile(file, content ? `${content}\n` : '', { encoding: 'utf8', mode: 0o600 });
      return result({ ok: true, file, bytes: Buffer.byteLength(content) });
    },
  });

  api.on('before_agent_start', async (event: any) => {
    if (injected) return event;
    injected = true;
    const memoryPath = String(process.env.CODINGTO_USER_MEMORY_PATH || '').trim();
    const prompt = await memoryPrompt();
    let userMemory = memoryPath ? (await readText(memoryPath)).trim() : '';
    while (Buffer.byteLength(userMemory) > MAX_USER_MEMORY_BYTES && userMemory.length > 0) {
      userMemory = userMemory.slice(0, Math.floor(userMemory.length * 0.9));
    }
    return {
      prompt: event?.message || '',
      include: [[
        IMMUTABLE_MEMORY_GUARD,
        '',
        prompt,
        '',
        '## User Memory',
        userMemory || '（空）',
      ].join('\n')],
      exclude: [],
    };
  });
}
