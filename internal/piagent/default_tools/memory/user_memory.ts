import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';

export const MAX_USER_MEMORY_BYTES = 8 * 1024;

const MAX_PATCH_ENTRIES = 8;
const MAX_ENTRY_BYTES = 512;
const USER_MEMORY_HEADING = '## User Memory';

function errorCode(error: unknown): string {
  return typeof error === 'object' && error !== null && 'code' in error
    ? String((error as { code?: unknown }).code || '')
    : '';
}

function memoryPath(): string {
  return String(process.env.CODINGTO_USER_MEMORY_PATH || '').trim();
}

function normalizeEntry(value: unknown): string {
  if (typeof value !== 'string') return '';
  return value
    .replace(/^\s*(?:[-*+]\s+|\d+[.)]\s+)/, '')
    .replace(/\s+/g, ' ')
    .trim();
}

function entryKey(value: string): string {
  return value.toLocaleLowerCase().replace(/[。！？；;，,、：:\s]+/g, '');
}

function bulletKey(line: string): string {
  if (!/^\s*(?:[-*+]\s+|\d+[.)]\s+)/.test(line)) return '';
  return entryKey(normalizeEntry(line));
}

async function readMemory(file: string): Promise<string> {
  try {
    return await readFile(file, 'utf8');
  } catch (error) {
    if (errorCode(error) === 'ENOENT') return '';
    throw error;
  }
}

function sectionEnd(lines: string[], headingIndex: number): number {
  let index = headingIndex + 1;
  while (index < lines.length && !/^#{1,6}\s+/.test(lines[index].trim())) index += 1;
  return index;
}

function applyPatch(content: string, additions: string[], removals: Set<string>): { content: string; removed: number } {
  const normalized = content.replace(/\r\n/g, '\n').trimEnd();
  let lines = normalized ? normalized.split('\n') : [];
  let removed = 0;
  lines = lines.filter((line) => {
    const key = bulletKey(line);
    if (!key || !removals.has(key)) return true;
    removed += 1;
    return false;
  });

  if (additions.length) {
    const headingIndex = lines.findIndex((line) => /^#{1,6}\s+User Memory\s*$/i.test(line.trim()));
    const bullets = additions.map((entry) => `- ${entry}`);
    if (headingIndex < 0) {
      lines = lines.length ? [...lines, '', USER_MEMORY_HEADING, '', ...bullets] : [USER_MEMORY_HEADING, '', ...bullets];
    } else {
      lines.splice(sectionEnd(lines, headingIndex), 0, ...bullets);
    }
  }

  return { content: lines.length ? `${lines.join('\n')}\n` : '', removed };
}

export async function patchUserMemory(addValues: unknown, removeValues: unknown) {
  const file = memoryPath();
  if (!file) throw new Error('user memory path is not configured');

  const addCandidates = Array.isArray(addValues) ? addValues.slice(0, MAX_PATCH_ENTRIES) : [];
  const removeCandidates = Array.isArray(removeValues) ? removeValues.slice(0, MAX_PATCH_ENTRIES) : [];
  let skipped = 0;
  skipped += Array.isArray(addValues) ? Math.max(0, addValues.length - addCandidates.length) : 0;
  skipped += Array.isArray(removeValues) ? Math.max(0, removeValues.length - removeCandidates.length) : 0;

  const additions: string[] = [];
  for (const value of addCandidates) {
    const entry = normalizeEntry(value);
    if (!entry || Buffer.byteLength(entry) > MAX_ENTRY_BYTES || additions.some((item) => entryKey(item) === entryKey(entry))) {
      skipped += 1;
      continue;
    }
    additions.push(entry);
  }

  const removals = new Set<string>();
  for (const value of removeCandidates) {
    const entry = normalizeEntry(value);
    if (entry) removals.add(entryKey(entry));
    else skipped += 1;
  }

  const previous = await readMemory(file);
  const currentKeys = new Set(previous.split(/\r?\n/).map(bulletKey).filter(Boolean));
  const retainedAdditions = additions.filter((entry) => {
    const key = entryKey(entry);
    if (currentKeys.has(key) || removals.has(key)) {
      skipped += 1;
      return false;
    }
    currentKeys.add(key);
    return true;
  });
  const next = applyPatch(previous, retainedAdditions, removals);
  if (!next.removed && !retainedAdditions.length) {
    return { file, added: [], removed: 0, skipped, bytes: Buffer.byteLength(previous) };
  }
  if (Buffer.byteLength(next.content) > MAX_USER_MEMORY_BYTES) {
    throw new Error(`user memory exceeds ${MAX_USER_MEMORY_BYTES} bytes`);
  }
  await mkdir(path.dirname(file), { recursive: true, mode: 0o700 });
  await writeFile(file, next.content, { encoding: 'utf8', mode: 0o600 });
  return { file, added: retainedAdditions, removed: next.removed, skipped, bytes: Buffer.byteLength(next.content) };
}
