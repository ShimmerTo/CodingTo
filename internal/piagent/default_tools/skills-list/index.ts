import type { ExtensionAPI } from '@earendil-works/pi-coding-agent';
import { readFile, readdir } from 'node:fs/promises';
import path from 'node:path';

// skills_list is intentionally a normal builtin tool rather than a CodingTo
// side-channel. Pi starts it for every isolated agent, and all roots are
// resolved from that process's PI_CODING_AGENT_DIR.
async function readText(file: string) {
  try { return await readFile(file, 'utf8'); } catch { return ''; }
}

function frontmatter(text: string) {
  const lines = text.replace(/\r\n/g, '\n').split('\n');
  if (lines[0]?.trim() !== '---') return null;
  const end = lines.findIndex((line, index) => index > 0 && line.trim() === '---');
  if (end < 0) return null;
  const values: Record<string, string> = {};
  for (const line of lines.slice(1, end)) {
    const colon = line.indexOf(':');
    if (colon < 0) continue;
    values[line.slice(0, colon).trim()] = line.slice(colon + 1).trim().replace(/^['"]|['"]$/g, '');
  }
  if (!values.name || !values.description) return null;
  return { name: values.name, description: values.description };
}

async function walk(root: string, output: any[], seen: Set<string>) {
  let entries: any[];
  try { entries = await readdir(root, { withFileTypes: true }); } catch { return; }
  for (const entry of entries) {
    if (entry.name === '.git' || entry.name === 'node_modules') continue;
    const absolute = path.join(root, entry.name);
    if (entry.isDirectory()) {
      const skillFile = path.join(absolute, 'SKILL.md');
      const meta = frontmatter(await readText(skillFile));
      if (meta && !seen.has(skillFile)) {
        seen.add(skillFile);
        output.push({ path: skillFile, ...meta });
      }
      await walk(absolute, output, seen);
    } else if (entry.name === 'SKILL.md') {
      const meta = frontmatter(await readText(absolute));
      if (meta && !seen.has(absolute)) {
        seen.add(absolute);
        output.push({ path: absolute, ...meta });
      }
    }
  }
}

async function packageRoots(dataDir: string) {
  const roots: string[] = [];
  let settings: any = {};
  try { settings = JSON.parse(await readText(path.join(dataDir, 'settings.json')) || '{}'); } catch { settings = {}; }
  for (const raw of Array.isArray(settings.packages) ? settings.packages : []) {
    const source = typeof raw === 'string' ? raw : String(raw?.source || '');
    if (source.startsWith('npm:')) {
      const spec = source.slice(4).replace(/@[^/]+$/, '');
      roots.push(path.join(dataDir, 'npm', 'node_modules', spec));
    } else if (source.startsWith('git:')) {
      const value = source.slice(4).replace(/@[^/]+$/, '').replace(/^https?:\/\//, '');
      const slash = value.indexOf('/');
      if (slash > 0) roots.push(path.join(dataDir, 'git', value.slice(0, slash), value.slice(slash + 1)));
    }
  }
  return roots;
}

async function listSkills() {
  const dataDir = path.resolve(String(process.env.PI_CODING_AGENT_DIR || '').trim() || '.');
  const output: any[] = [];
  const seen = new Set<string>();
  for (const root of [path.join(dataDir, 'skills'), path.join(dataDir, 'skills_list'), ...(await packageRoots(dataDir))]) {
    await walk(root, output, seen);
  }
  output.sort((a, b) => a.name.localeCompare(b.name));
  return output;
}

function textResult(result: any) {
  return { content: [{ type: 'text', text: JSON.stringify(result, null, 2) }], details: result };
}

export default function registerSkillsList(api: ExtensionAPI) {
  api.registerTool({
    name: 'skills_list',
    description: '列出当前隔离 Agent 可使用的全部 Skills。返回每个 Skill 的 SKILL.md 绝对路径、名称和描述；只读取当前 PI_CODING_AGENT_DIR，不会泄漏其他 Agent 的 Skills。',
    parameters: { type: 'object', properties: {}, required: [] },
    async execute() {
      const skills = await listSkills();
      return textResult({ skills, count: skills.length, agentDir: path.resolve(String(process.env.PI_CODING_AGENT_DIR || '').trim() || '.') });
    },
  });
}
