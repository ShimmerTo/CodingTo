import type { ExtensionAPI } from '@earendil-works/pi-coding-agent';
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import readline from 'node:readline';

type BridgeResponse = {
  version: number;
  id: string;
  ok: boolean;
  result?: any;
  error?: { code?: string; message?: string };
};

type PendingCall = {
  resolve: (value: any) => void;
  reject: (error: Error) => void;
  timer: ReturnType<typeof setTimeout>;
  cleanup: () => void;
};

type NeedsConfirm = {
  token: string;
  resourceId: string;
  resourceName: string;
  capability: string;
  description?: string;
  params?: Record<string, string>;
  reason: string;
};

const bridgeBin = String(process.env.CODINGTO_SSH_BRIDGE_BIN || '').trim();
const configPath = String(process.env.CODINGTO_SSH_CONFIG_PATH || '').trim();
const workDir = process.env.CODINGTO_WORK_DIR || process.cwd();
const sessionAllows = new Set<string>();

class BridgeClient {
  private child: ChildProcessWithoutNullStreams | null = null;
  private pending = new Map<string, PendingCall>();
  private nextID = 0;

  private start() {
    if (this.child && !this.child.killed && this.child.exitCode === null) return;
    if (!bridgeBin || !configPath) {
      throw failure('ssh_disabled', 'SSH 工具未启用：请在工作空间中关联 SSH 配置后重新开始对话。');
    }
    const child = spawn(bridgeBin, ['ssh-security-bridge', 'serve', '--config', configPath], {
      cwd: workDir,
      stdio: ['pipe', 'pipe', 'pipe'],
      windowsHide: true,
    });
    this.child = child;
    const lines = readline.createInterface({ input: child.stdout, crlfDelay: Infinity });
    lines.on('line', (line) => {
      let response: BridgeResponse;
      try {
        response = JSON.parse(line);
      } catch {
        this.failAll(failure('bridge_protocol_error', 'SSH Security Bridge 返回了无效 JSON。'));
        this.terminate();
        return;
      }
      const pending = this.pending.get(response.id);
      if (!pending) return;
      this.pending.delete(response.id);
      clearTimeout(pending.timer);
      pending.cleanup();
      if (response.ok) pending.resolve(response.result);
      else pending.reject(failure(response.error?.code || 'bridge_error', response.error?.message || 'SSH Security Bridge 请求失败。'));
    });
    child.stderr.resume();
    child.once('error', (error) => this.failAll(failure('bridge_start_failed', `无法启动 SSH Security Bridge：${error.message}`)));
    child.once('exit', (code) => {
      if (this.child === child) this.child = null;
      this.failAll(failure('bridge_exited', `SSH Security Bridge 意外退出（${code ?? 'unknown'}）。`));
    });
  }

  call(action: string, params: any, signal?: AbortSignal, timeoutMs = 320_000): Promise<any> {
    this.start();
    const child = this.child!;
    const id = `ssh-${process.pid}-${++this.nextID}`;
    return new Promise((resolve, reject) => {
      const cleanup = () => signal?.removeEventListener('abort', abort);
      const timer = setTimeout(() => {
        this.pending.delete(id);
        cleanup();
        reject(failure('bridge_timeout', 'SSH Security Bridge 请求超时。'));
        this.terminate();
      }, timeoutMs);
      const abort = () => {
        if (child.exitCode === null && !child.killed) {
          child.stdin.write(`${JSON.stringify({ version: 1, id: `cancel-${id}`, action: 'cancel', params: { requestId: id } })}\n`, (error) => {
            if (!error) return;
            const pending = this.pending.get(id);
            if (!pending) return;
            this.pending.delete(id);
            clearTimeout(pending.timer);
            pending.cleanup();
            pending.reject(failure('bridge_write_failed', `无法取消 SSH Security Bridge 请求：${error.message}`));
          });
        }
      };
      this.pending.set(id, { resolve, reject, timer, cleanup });
      signal?.addEventListener('abort', abort, { once: true });
      child.stdin.write(`${JSON.stringify({ version: 1, id, action, params })}\n`, (error) => {
        if (!error) return;
        cleanup();
        this.pending.delete(id);
        clearTimeout(timer);
        reject(failure('bridge_write_failed', `无法发送 SSH Security Bridge 请求：${error.message}`));
      });
    });
  }

  close() {
    const child = this.child;
    if (!child) return;
    child.stdin.end();
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
      pending.cleanup();
      pending.reject(error);
    }
    this.pending.clear();
  }
}

function failure(code: string, message: string) {
  const error = new Error(message) as Error & { code: string };
  error.code = code;
  return error;
}

function required(params: any, key: string) {
  const value = String(params?.[key] || '').trim();
  if (!value) throw failure('bad_request', `${key} 不能为空。`);
  return value;
}

function textResult(result: any, suffix = '') {
  return { content: [{ type: 'text', text: `${JSON.stringify(result, null, 2)}${suffix}` }], details: result };
}

function confirmKey(needs: NeedsConfirm) {
  const params = Object.entries(needs.params || {})
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([name, value]) => [name, String(value)]);
  return `${needs.resourceId}::${needs.capability}::${JSON.stringify(params)}`;
}

async function executeWithConfirm(params: any, signal: AbortSignal | undefined, ctx: any, onUpdate?: any) {
  let result = await client.call('execute', params, signal);
  let hops = 0;
  while (result?.needsConfirm) {
    if (++hops > 2) throw failure('confirm_loop', 'SSH 确认流程异常循环，已中止。');
    const needs = result.needsConfirm as NeedsConfirm;
    const key = confirmKey(needs);
    const raw = needs.capability === 'shell.raw';
    const summary = Object.entries(needs.params || {}).map(([name, value]) => `${name}=${value}`).join('\n');
    let choice = !raw && sessionAllows.has(key) ? 'allow_once' : 'deny';
    if (choice === 'deny') {
      if (!ctx?.ui?.select) throw failure('ui_unavailable', '当前客户端无法显示 SSH 操作确认窗口，操作已拒绝。');
      onUpdate?.({
        content: [{ type: 'text', text: `等待用户确认 SSH 操作…\n${needs.capability}\n${summary}` }],
        details: { status: 'confirming', needsConfirm: needs },
      });
      const options: any[] = [
        { label: '允许本次', value: 'allow_once', description: needs.reason },
      ];
      if (!raw) options.push({ label: '允许本轮（本会话同类操作不再询问）', value: 'allow_session', description: needs.description || summary });
      options.push({ label: '拒绝', value: 'deny' });
      const picked = await ctx.ui.select('SSH 操作确认', options);
      choice = picked === 'allow_once' || picked === 'allow_session' ? String(picked) : 'deny';
    }
    if (choice === 'allow_session') {
      sessionAllows.add(key);
      choice = 'allow_once';
    }
    if (choice === 'deny') {
      const denied = await client.call('confirm', { token: needs.token, decision: 'deny' }, signal);
      return { status: 'denied', capability: needs.capability, bridge: denied };
    }
    result = await client.call('confirm', { token: needs.token, decision: 'allow' }, signal);
  }
  return result;
}

const client = new BridgeClient();
process.once('beforeExit', () => client.close());
process.once('exit', () => client.close());

export default function (api: ExtensionAPI) {
  api.registerTool({
    name: 'codingto_ssh',
    description: [
      '通过安全能力模板访问当前工作空间已授权的 SSH 资源。',
      '先用 resources 列出 resourceId，再用 catalog 查看该资源可用的 system/git/docker/service/custom 能力、权限和强类型参数。',
      'execute 只能提交 capability 名称与 params，禁止自行拼接 Shell；ASK 操作会由确定性代码请求用户确认。',
      'DENY 后不要重复调用；输出 truncated=true 时请缩小查询范围。',
    ].join(''),
    parameters: {
      type: 'object',
      properties: {
        action: { type: 'string', enum: ['resources', 'catalog', 'execute'] },
        resourceId: { type: 'string', description: 'resources 返回的 SSH 资源 ID。' },
        capability: { type: 'string', description: 'catalog 返回的完整能力名，例如 docker.logs。' },
        params: { type: 'object', description: 'catalog 声明的强类型参数对象；不得添加未声明参数。' },
      },
      required: ['action'],
    },
    async execute(_id: string, params: any, signal?: AbortSignal, onUpdate?: any, ctx?: any) {
      try {
        const action = required(params, 'action');
        if (action === 'resources') return textResult(await client.call('resources', {}, signal));
        const resourceId = required(params, 'resourceId');
        if (action === 'catalog') return textResult(await client.call('catalog', { resourceId }, signal));
        if (action !== 'execute') throw failure('bad_request', 'action 必须是 resources、catalog 或 execute。');
        const capability = required(params, 'capability');
        if (params?.params !== undefined && (!params.params || Array.isArray(params.params) || typeof params.params !== 'object')) {
          throw failure('bad_request', 'params 必须是对象。');
        }
        onUpdate?.({ content: [{ type: 'text', text: `正在执行 SSH 能力 ${capability}…` }], details: { status: 'working', capability } });
        const result = await executeWithConfirm({ resourceId, capability, params: params.params || {} }, signal, ctx, onUpdate);
        return textResult(result, result?.status === 'denied' ? '\n用户拒绝了该 SSH 操作。' : '');
      } catch (error: any) {
        return textResult({ status: 'error', code: String(error?.code || 'ssh_error'), message: String(error?.message || error || 'SSH Security Bridge 请求失败。') });
      }
    },
  });
}
