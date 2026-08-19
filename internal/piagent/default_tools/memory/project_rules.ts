import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';

export const PROJECT_RULES_HEADING = '## 项目规则';

const MAX_RULES_PER_UPDATE = 8;
const MAX_RULE_BYTES = 512;
const MAX_AGENTS_BYTES = 64 * 1024;

function errorCode(error: unknown): string {
  return typeof error === 'object' && error !== null && 'code' in error
    ? String((error as { code?: unknown }).code || '')
    : '';
}

function projectRulesPath(): string {
  const configured = String(process.env.CODINGTO_PROJECT_RULES_PATH || '').trim();
  if (configured) return path.resolve(configured);
  const workDir = String(process.env.CODINGTO_WORK_DIR || '').trim();
  return workDir ? path.join(path.resolve(workDir), 'AGENTS.md') : '';
}

function normalizeRule(value: unknown): string {
  if (typeof value !== 'string') return '';
  return value
    .replace(/^\s*(?:[-*+]\s+|\d+[.)]\s+)/, '')
    .replace(/\s+/g, ' ')
    .trim();
}

function ruleKey(value: string): string {
  return value.toLocaleLowerCase().replace(/[。！？；;，,、：:\s]+/g, '');
}

function existingRuleKey(line: string): string {
  if (!/^\s*(?:[-*+]\s+|\d+[.)]\s+)/.test(line)) return '';
  return ruleKey(normalizeRule(line));
}

async function readProjectRulesFile(file: string): Promise<string> {
  try {
    return await readFile(file, 'utf8');
  } catch (error) {
    if (errorCode(error) === 'ENOENT') return '';
    throw error;
  }
}

function insertRules(content: string, rules: string[]): { content: string; added: string[] } {
  const normalized = content.replace(/\r\n/g, '\n');
  const lines = normalized.split('\n');
  const headingIndex = lines.findIndex((line) => /^##\s+(?:项目规则|Project Rules)\s*$/.test(line.trim()));
  const existing = new Set<string>();
  if (headingIndex >= 0) {
    for (let index = headingIndex + 1; index < lines.length; index += 1) {
      if (/^#{1,6}\s+/.test(lines[index].trim())) break;
      const key = existingRuleKey(lines[index]);
      if (key) existing.add(key);
    }
  }

  const added: string[] = [];
  for (const rule of rules) {
    const key = ruleKey(rule);
    if (!key || existing.has(key)) continue;
    existing.add(key);
    added.push(`- ${rule}`);
  }
  if (!added.length) return { content: normalized, added: [] };

  if (headingIndex < 0) {
    const prefix = normalized.trimEnd();
    return {
      content: `${prefix ? `${prefix}\n\n` : ''}${PROJECT_RULES_HEADING}\n\n${added.join('\n')}\n`,
      added,
    };
  }

  let sectionEnd = headingIndex + 1;
  while (sectionEnd < lines.length && !/^#{1,6}\s+/.test(lines[sectionEnd].trim())) sectionEnd += 1;
  lines.splice(sectionEnd, 0, ...added);
  return { content: lines.join('\n').replace(/\n*$/, '\n'), added };
}

export async function updateProjectRules(values: unknown) {
  const file = projectRulesPath();
  if (!file) throw new Error('project rules path is not configured');

  const candidates = Array.isArray(values) ? values.slice(0, MAX_RULES_PER_UPDATE) : [];
  const rules: string[] = [];
  let skipped = 0;
  for (const value of candidates) {
    const rule = normalizeRule(value);
    if (!rule || Buffer.byteLength(rule) > MAX_RULE_BYTES) {
      skipped += 1;
      continue;
    }
    if (!rules.some((existing) => ruleKey(existing) === ruleKey(rule))) rules.push(rule);
    else skipped += 1;
  }
  if (Array.isArray(values)) skipped += Math.max(0, values.length - candidates.length);
  if (!rules.length) return { file, added: [], skipped };

  const previous = await readProjectRulesFile(file);
  const next = insertRules(previous, rules);
  if (!next.added.length) return { file, added: [], skipped: skipped + rules.length };
  if (Buffer.byteLength(next.content) > MAX_AGENTS_BYTES) {
    throw new Error(`AGENTS.md exceeds ${MAX_AGENTS_BYTES} bytes after project rule update`);
  }
  await mkdir(path.dirname(file), { recursive: true, mode: 0o700 });
  await writeFile(file, next.content, { encoding: 'utf8', mode: 0o600 });
  return { file, added: next.added.map((rule) => rule.slice(2)), skipped };
}
