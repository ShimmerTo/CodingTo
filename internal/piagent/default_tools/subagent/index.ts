import type { ExtensionAPI } from '@earendil-works/pi-coding-agent';
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import readline from 'node:readline';

type BridgeError = { code: string; message: string; retryable?: boolean };
type BridgeResponse = {
  version: number;
  id: string;
  ok: boolean;
  result?: any;
  error?: BridgeError;
};
type BridgeNotification = {
  version: number;
  type: 'event';
  runId: string;
  agentKey: string;
  event: any;
};
type PendingCall = {
  resolve: (value: any) => void;
  reject: (error: Error) => void;
  timer: ReturnType<typeof setTimeout>;
  onEvent?: (event: BridgeNotification) => void;
};

const bridgeBin = String(process.env.CODINGTO_SUBAGENT_BRIDGE_BIN || '').trim();
const configPath = String(process.env.CODINGTO_SUBAGENT_CONFIG || '').trim();
const sessionDir = path.resolve(process.env.CODINGTO_SESSION_DIR || process.cwd());
const workDir = path.resolve(process.env.CODINGTO_WORK_DIR || process.cwd());
const authorizedKeys = new Set(
  String(process.env.CODINGTO_SUBAGENT_KEYS || '')
    .split(',')
    .map((value) => value.trim())
    .filter(Boolean),
);

class BridgeClient {
  private child: ChildProcessWithoutNullStreams | null = null;
  private pending = new Map<string, PendingCall>();
  private nextID = 0;
  private stderr = '';

  private start() {
    if (this.child && !this.child.killed && this.child.exitCode === null) return;
    if (!bridgeBin || !configPath) {
      throw bridgeFailure('bridge_unavailable', 'Subagent Bridge 未配置；请启用 Subagent 扩展后重启当前 Agent。');
    }
    const child = spawn(bridgeBin, [
      'subagent-bridge',
      'serve',
      '--session-dir', sessionDir,
      '--work-dir', workDir,
      '--config', configPath,
    ], {
      cwd: workDir,
      stdio: ['pipe', 'pipe', 'pipe'],
      windowsHide: true,
    });
    this.child = child;
    this.stderr = '';
    const lines = readline.createInterface({ input: child.stdout, crlfDelay: Infinity });
    lines.on('line', (line) => {
      let message: BridgeResponse | BridgeNotification;
      try {
        message = JSON.parse(line);
      } catch {
        this.failAll(bridgeFailure('bridge_protocol_error', 'Subagent Bridge 返回了无效 JSON。'));
        this.terminate();
        return;
      }
      if ((message as BridgeNotification).type === 'event') {
        const notification = message as BridgeNotification;
        for (const pending of this.pending.values()) pending.onEvent?.(notification);
        return;
      }
      const response = message as BridgeResponse;
      const pending = this.pending.get(response.id);
      if (!pending) return;
      this.pending.delete(response.id);
      clearTimeout(pending.timer);
      if (response.ok) pending.resolve(response.result);
      else pending.reject(bridgeFailure(
        response.error?.code || 'bridge_error',
        response.error?.message || 'Subagent Bridge 请求失败。',
      ));
    });
    child.stderr.on('data', (chunk) => {
      if (this.stderr.length < 4096) this.stderr += String(chunk).slice(0, 4096 - this.stderr.length);
    });
    child.once('error', (error) => {
      this.failAll(bridgeFailure('bridge_start_failed', `无法启动 Subagent Bridge：${error.message}`));
    });
    child.once('exit', (code) => {
      if (this.child === child) this.child = null;
      const suffix = this.stderr.trim() ? `：${this.stderr.trim()}` : '';
      this.failAll(bridgeFailure('bridge_exited', `Subagent Bridge 意外退出（${code ?? 'unknown'}）${suffix}`));
    });
  }

  call(action: string, params: any, signal?: AbortSignal, onEvent?: (event: BridgeNotification) => void) {
    this.start();
    const child = this.child!;
    const id = `subagent-${process.pid}-${++this.nextID}`;
    return new Promise<any>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(bridgeFailure('bridge_timeout', 'Subagent Bridge 请求超时。'));
        this.terminate();
      }, 11 * 60_000);
      this.pending.set(id, { resolve, reject, timer, onEvent });
      const abort = () => {
        if (child.exitCode === null && !child.killed) {
          child.stdin.write(`${JSON.stringify({
            version: 1,
            id: `cancel-${id}`,
            action: 'cancel',
            params: { requestId: id },
          })}\n`);
        }
      };
      signal?.addEventListener('abort', abort, { once: true });
      child.stdin.write(`${JSON.stringify({ version: 1, id, action, params })}\n`, (error) => {
        if (!error) return;
        signal?.removeEventListener('abort', abort);
        this.pending.delete(id);
        clearTimeout(timer);
        reject(bridgeFailure('bridge_write_failed', `无法发送 Subagent Bridge 请求：${error.message}`));
      });
    });
  }

  close() {
    if (!this.child) return;
    this.child.stdin.end();
    const child = this.child;
    setTimeout(() => {
      if (child.exitCode === null && !child.killed) child.kill();
    }, 1500).unref();
  }

  private terminate() {
    const child = this.child;
    this.child = null;
    if (child && child.exitCode === null && !child.killed) child.kill();
  }

  private failAll(error: Error) {
    for (const pending of this.pending.values()) {
      clearTimeout(pending.timer);
      pending.reject(error);
    }
    this.pending.clear();
  }
}

function bridgeFailure(code: string, message: string) {
  const error = new Error(message) as Error & { code: string };
  error.code = code;
  return error;
}

function textResult(result: any) {
  return {
    content: [{ type: 'text', text: JSON.stringify(result, null, 2) }],
    details: result,
  };
}

function runID() {
  return `run-${Date.now()}-${randomBytes(6).toString('hex')}`;
}

async function parentNodeID() {
  const value = (await readFile(path.join(sessionDir, '.active-change-node'), 'utf8')).trim();
  if (!/^turn-\d+$/.test(value)) throw bridgeFailure('change_node_unavailable', '当前主任务没有有效的产出节点。');
  return value;
}

const client = new BridgeClient();
process.once('beforeExit', () => client.close());
process.once('exit', () => client.close());

export default function registerSubagent(api: ExtensionAPI) {
  api.registerTool({
    name: 'codingto_subagent',
    description: [
      '调用当前 Agent 已授权的其他 CodingTo Agent 完成一个边界清晰的子任务。',
      '先用 action=list 查看可用 key；再用 action=run、key 和 task 执行。',
      'run 会等待子 Agent 完成，并返回最终文本、状态、完整对话目录和已记录的文件产出。',
    ].join(''),
    parameters: {
      type: 'object',
      properties: {
        action: { type: 'string', enum: ['list', 'run'] },
        key: { type: 'string', description: 'list 返回的子 Agent key。' },
        task: { type: 'string', description: '交给子 Agent 的完整、独立任务描述。' },
      },
      required: ['action'],
    },
    async execute(toolCallId: string, params: any, signal?: AbortSignal, onUpdate?: any) {
      const action = String(params?.action || '').trim();
      if (action === 'list') return textResult(await client.call('list', {}, signal));
      if (action !== 'run') throw bridgeFailure('bad_request', 'action 必须是 list 或 run。');
      const key = String(params?.key || '').trim();
      const task = String(params?.task || '').trim();
      if (!key || !task) throw bridgeFailure('bad_request', 'run 需要 key 和 task。');
      if (!authorizedKeys.has(key)) throw bridgeFailure('not_authorized', `未授权的子 Agent：${key}`);
      const id = runID();
      const parent = await parentNodeID();
      onUpdate?.({
        content: [{ type: 'text', text: `正在调用 Subagent ${key}…` }],
        details: {
          kind: 'subagent_event', runId: id, agentKey: key,
          parentNodeId: parent, toolCallId, status: 'running',
        },
      });
      const result = await client.call('run', {
        key, task, runId: id, parentNodeId: parent, toolCallId,
      }, signal, (notification) => {
        if (notification.runId !== id) return;
        onUpdate?.({
          content: [{ type: 'text', text: `Subagent ${key} 正在执行…` }],
          details: {
            kind: 'subagent_event', runId: id, agentKey: key,
            parentNodeId: parent, toolCallId, status: 'running',
            event: notification.event,
          },
        });
      });
      return textResult(result);
    },
  });
}
