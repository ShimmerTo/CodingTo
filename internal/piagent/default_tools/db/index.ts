import type { ExtensionAPI } from '@earendil-works/pi-coding-agent';
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import readline from 'node:readline';

type BridgeError = {
  code: string;
  message: string;
  retryable?: boolean;
};

type BridgeResponse = {
  version: number;
  id: string;
  ok: boolean;
  result?: any;
  error?: BridgeError;
};

type PendingCall = {
  resolve: (value: any) => void;
  reject: (error: Error) => void;
  timer: ReturnType<typeof setTimeout>;
};

type NeedsConfirm = {
  token: string;
  decision: string;
  connectionId: string;
  reason: string;
  statements: { sql: string; action: string; ruleId?: string; reason?: string }[];
  expiresInMs?: number;
};

const bridgeBin = String(process.env.CODINGTO_DB_BRIDGE_BIN || '').trim();
const configPath = String(process.env.CODINGTO_DB_CONFIG_PATH || '').trim();
const workDir = process.env.CODINGTO_WORK_DIR || process.cwd();

// 「允许本轮」会话内白名单：(connectionId, 语句动作组合) 命中后同类请求
// 直接发 confirm，不再弹窗；进程结束即失效。确定性代码维护，LLM 不可触达。
const sessionAllows = new Set<string>();

class BridgeClient {
  private child: ChildProcessWithoutNullStreams | null = null;
  private pending = new Map<string, PendingCall>();
  private nextID = 0;
  private stderr = '';

  private start() {
    if (this.child && !this.child.killed && this.child.exitCode === null) return;
    if (!bridgeBin || !configPath) {
      throw bridgeFailure('db_disabled', '数据库工具未启用：请在环境页「数据库」添加连接，并在工作空间编辑中勾选后重新开始对话。');
    }
    const child = spawn(bridgeBin, [
      'db-security-bridge',
      'serve',
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
      let response: BridgeResponse;
      try {
        response = JSON.parse(line);
      } catch {
        this.failAll(bridgeFailure('bridge_protocol_error', 'DB Security Bridge 返回了无效 JSON。'));
        this.terminate();
        return;
      }
      const pending = this.pending.get(response.id);
      if (!pending) return;
      this.pending.delete(response.id);
      clearTimeout(pending.timer);
      if (response.ok) {
        pending.resolve(response.result);
      } else {
        pending.reject(bridgeFailure(
          response.error?.code || 'bridge_error',
          response.error?.message || 'DB Security Bridge 请求失败。',
        ));
      }
    });
    child.stderr.on('data', (chunk) => {
      if (this.stderr.length < 4096) this.stderr += String(chunk).slice(0, 4096 - this.stderr.length);
    });
    child.once('error', (error) => {
      this.failAll(bridgeFailure('bridge_start_failed', `无法启动 DB Security Bridge：${error.message}`));
    });
    child.once('exit', (code) => {
      if (this.child === child) this.child = null;
      this.failAll(bridgeFailure(
        'bridge_exited',
        `DB Security Bridge 意外退出（${code ?? 'unknown'}）。`,
      ));
    });
  }

  call(action: string, params: any, signal?: AbortSignal, timeoutMs = 120_000): Promise<any> {
    this.start();
    const child = this.child!;
    const id = `db-${process.pid}-${++this.nextID}`;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(bridgeFailure('bridge_timeout', 'DB Security Bridge 请求超时。'));
        this.terminate();
      }, timeoutMs);
      this.pending.set(id, { resolve, reject, timer });
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
        reject(bridgeFailure('bridge_write_failed', `无法发送 DB Security Bridge 请求：${error.message}`));
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

function required(params: any, key: string, example: string) {
  const value = String(params?.[key] || '').trim();
  if (!value) throw bridgeFailure('bad_request', `缺少 ${key}。示例：${example}`);
  return value;
}

function validateAction(params: any) {
  const action = String(params?.action || '').trim();
  if (!['connections', 'schema', 'query', 'execute'].includes(action)) {
    throw bridgeFailure('bad_request', 'action 必须是 connections、schema、query 或 execute。');
  }
  if (action === 'connections') return action;
  required(params, 'connectionId', '{"action":"query","connectionId":"db-1","sql":"SELECT 1"}');
  if (action === 'query' || action === 'execute') {
    required(params, 'sql', `{"action":"${action}","connectionId":"db-1","sql":"..."}`);
    if (params?.params !== undefined && !Array.isArray(params.params)) {
      throw bridgeFailure('bad_request', 'params 必须是数组（参数化占位符按序绑定，仅支持单条语句）。');
    }
  }
  return action;
}

function textResult(result: any, extra = '') {
  const text = `${JSON.stringify(result, null, 2)}${extra}`;
  return { content: [{ type: 'text', text }], details: result };
}

function confirmKey(needs: NeedsConfirm) {
  const actions = (needs.statements || []).map((statement) => statement.action).join(',');
  return `${needs.connectionId}::${actions}`;
}

// 确认编排：bridge 判定 confirm 时不执行，返回 needsConfirm；本函数以
// 确定性代码弹确认框并把用户裁决回传 bridge。此路径不经过 LLM，
// 不可能被提示词绕过；「允许本轮」只在 TS 侧缓存白名单。
async function runWithConfirm(
  action: string,
  requestParams: any,
  signal: AbortSignal | undefined,
  ctx: any,
  onUpdate?: any,
): Promise<any> {
  let result = await client.call(action, requestParams, signal);
  let hops = 0;
  while (result?.needsConfirm) {
    if (++hops > 3) {
      throw bridgeFailure('confirm_loop', '确认流程异常循环，已中止。');
    }
    const needs = result.needsConfirm as NeedsConfirm;
    const key = confirmKey(needs);
    let choice = 'deny';
    if (sessionAllows.has(key)) {
      choice = 'allow_once';
    } else if (!ctx?.ui?.select) {
      throw bridgeFailure('ui_unavailable', '当前客户端无法显示数据库操作确认窗口，操作已按拒绝处理。');
    } else {
      const summary = (needs.statements || [])
        .map((statement) => `[${statement.action}] ${statement.sql}`)
        .join('\n');
      onUpdate?.({
        content: [{ type: 'text', text: `等待用户确认数据库操作…\n${summary}` }],
        details: { status: 'confirming', needsConfirm: needs },
      });
      const picked = await ctx.ui.select('数据库操作确认', [
        { label: '允许本次', value: 'allow_once', description: needs.reason },
        { label: '允许本轮（本会话同类操作不再询问）', value: 'allow_session', description: summary },
        { label: '拒绝', value: 'deny' },
      ]);
      choice = picked === 'allow_once' || picked === 'allow_session' ? String(picked) : 'deny';
    }
    if (choice === 'allow_session') {
      sessionAllows.add(key);
      choice = 'allow_once';
    }
    if (choice === 'deny') {
      const denied = await client.call('confirm', { token: needs.token, decision: 'deny' }, signal);
      return {
        status: 'denied',
        reason: needs.reason || '用户拒绝了该操作',
        statements: needs.statements,
        bridge: denied,
      };
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
    name: 'codingto_db',
    description: [
      '在安全策略管控下访问用户已配置并授权给本工作空间的数据库（MySQL/PostgreSQL/SQLite）。',
      'connections 列出可用连接；schema 查看表清单或表结构（可选 table）；',
      'query 执行只读语句（SELECT/SHOW/DESCRIBE/EXPLAIN），execute 执行写入/DDL（INSERT/UPDATE/DELETE/CREATE…）。',
      'sql 支持多条分号分隔（逐条独立判定，任一被拒则整批拒绝）；参数化用 params 数组按占位符顺序绑定（仅单条语句）。',
      '危险操作会触发用户确认对话框：被拒时不要反复重试同一条语句，可先向用户说明原因。',
      '结果行数有上限，truncated=true 表示已截断；请优先用 LIMIT/WHERE 缩小范围。',
    ].join(''),
    parameters: {
      type: 'object',
      properties: {
        action: { type: 'string', enum: ['connections', 'schema', 'query', 'execute'] },
        connectionId: { type: 'string', description: 'connections 返回的连接 ID。' },
        table: { type: 'string', description: 'schema 可选：查看指定表结构。' },
        sql: { type: 'string', description: 'query/execute 的完整 SQL，可多条分号分隔。' },
        params: { type: 'array', description: '参数化绑定的值数组（仅单条语句时可用）。' },
      },
      required: ['action'],
    },
    async execute(_id: string, params: any, signal?: AbortSignal, onUpdate?: any, ctx?: any) {
      try {
        const action = validateAction(params);
        if (action === 'connections') {
          const result = await client.call('connections', {}, signal);
          return textResult(result);
        }
        if (action === 'schema') {
          const requestParams: any = { connectionId: String(params.connectionId).trim() };
          if (String(params?.table || '').trim()) requestParams.table = String(params.table).trim();
          const result = await runWithConfirm('schema', requestParams, signal, ctx, onUpdate);
          return textResult(result);
        }
        onUpdate?.({
          content: [{ type: 'text', text: `正在执行数据库 ${action}…` }],
          details: { status: 'working', action },
        });
        const requestParams: any = {
          connectionId: String(params.connectionId).trim(),
          sql: String(params.sql),
        };
        if (Array.isArray(params.params) && params.params.length > 0) {
          requestParams.params = params.params;
        }
        const result = await runWithConfirm(action, requestParams, signal, ctx, onUpdate);
        if (result?.denied) {
          return textResult(result, '\n用户拒绝了该数据库操作。');
        }
        return textResult(result);
      } catch (error: any) {
        const result = {
          status: 'error',
          code: String(error?.code || 'db_error'),
          message: String(error?.message || error || 'DB Security Bridge 请求失败。'),
        };
        return textResult(result);
      }
    },
  });
}
