import type { ExtensionAPI, SubagentParams, SubagentUpdate } from '@earendil-works/pi-coding-agent';
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { readFile, rename, unlink, writeFile } from 'node:fs/promises';
import path from 'node:path';
import readline from 'node:readline';

type BridgeError = { code: string; message: string; retryable?: boolean };
type BridgeNotificationEvent = {
  type?: string;
  runId?: string;
  agentKey?: string;
  [key: string]: unknown;
};
type BridgeResponse = {
  version: number;
  id: string;
  ok: boolean;
  result?: unknown;
  error?: BridgeError;
};
type BridgeNotification = {
  version: number;
  type: 'event';
  runId: string;
  agentKey: string;
  event: BridgeNotificationEvent | string;
};
type PendingCall = {
  resolve: (value: unknown) => void;
  reject: (error: Error) => void;
  timer?: ReturnType<typeof setTimeout>;
  onEvent?: (event: BridgeNotification) => boolean | void;
  cleanupSignal?: () => void;
};
type RunFile = { path: string; change: string; kind?: string; bytes?: number };
type RunResult = {
  runId: string;
  agentKey: string;
  parentNodeId: string;
  status: 'running' | 'completed' | 'failed' | 'aborted' | 'timeout' | string;
  files: RunFile[];
  error?: string;
  transcript: string;
  [key: string]: unknown;
};

type BackgroundRun = {
  runId: string;
  agentKey: string;
  parentNodeId: string;
  startedAt: number;
};
type TerminalRunEntry = {
  result: RunResult;
  expiresAt: number;
};

const BRIDGE_PROTOCOL_VERSION = 3;
const START_TIMEOUT_MS = Math.min(10 * 60_000, Math.max(
  1_000,
  Number.parseInt(String(process.env.CODINGTO_SUBAGENT_START_TIMEOUT_MS || '60000'), 10) || 60_000,
));
const MAX_DETACHED_OUTPUT_BYTES = 4 * 1024;
const MAX_DETACHED_EVENT_BYTES = 12 * 1024;
const MAX_TERMINAL_RUNS = 128;
const TERMINAL_RUN_TTL_MS = 10 * 60_000;
const BACKGROUND_IDLE_TIMEOUT_MS = Math.min(24 * 60 * 60_000, Math.max(
  0,
  Number.parseInt(String(process.env.CODINGTO_SUBAGENT_IDLE_TIMEOUT_MS || String(2 * 60 * 60_000)), 10) || 0,
));

const bridgeBin = String(process.env.CODINGTO_SUBAGENT_BRIDGE_BIN || '').trim();
const configPath = String(process.env.CODINGTO_SUBAGENT_CONFIG || '').trim();
const sessionDir = path.resolve(process.env.CODINGTO_SESSION_DIR || process.cwd());
const workDir = path.resolve(process.env.CODINGTO_WORK_DIR || process.cwd());
const maxConcurrency = Math.min(4, Math.max(
  1,
  Number.parseInt(String(process.env.CODINGTO_SUBAGENT_MAX_CONCURRENCY || '4'), 10) || 4,
));
const authorizedKeys = new Set(
  String(process.env.CODINGTO_SUBAGENT_KEYS || '')
    .split(',')
    .map((value) => value.trim())
    .filter(Boolean),
);
const backgroundRuns = new Map<string, BackgroundRun>();
const terminalRuns = new Map<string, TerminalRunEntry>();
const detachedEventTypes = new Set([
  'agent_start',
  'tool_execution_start', 'tool_execution_end',
  'extension_ui_request', 'subagent_ui_response',
]);

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
        for (const [id, pending] of this.pending.entries()) {
          const detachSignal = pending.onEvent?.(notification);
          if (detachSignal) pending.cleanupSignal?.();
        }
        return;
      }
      const response = message as BridgeResponse;
      const pending = this.pending.get(response.id);
      if (!pending) return;
      this.pending.delete(response.id);
      if (pending.timer) clearTimeout(pending.timer);
      pending.cleanupSignal?.();
      if (response.ok) pending.resolve(response.result);
      else pending.reject(bridgeFailure(
        response.error?.code || 'bridge_error',
        response.error?.message || 'Subagent Bridge 请求失败。',
        Boolean(response.error?.retryable),
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

  call(
    action: string,
    params: unknown,
    signal?: AbortSignal,
    onEvent?: (event: BridgeNotification) => boolean | void,
    onRequestID?: (id: string) => void,
  ): Promise<unknown> {
    if (signal?.aborted) return Promise.reject(bridgeFailure('canceled', 'Subagent Bridge 请求已取消。'));
    this.start();
    const child = this.child!;
    const id = `subagent-${process.pid}-${++this.nextID}`;
    onRequestID?.(id);
    return new Promise<unknown>((resolve, reject) => {
      // A run may legitimately take hours while the child keeps producing
      // progress. Keep the legacy timeout only for short control requests;
      // run requests end through completion, explicit abort, or process exit.
      const timer = action === 'run' ? undefined : setTimeout(() => {
        if (!this.pending.has(id)) return;
        this.rejectPending(id, bridgeFailure('bridge_timeout', 'Subagent Bridge 请求超时。'));
        this.terminate();
      }, 11 * 60_000);
      // The signal can be aborted after the caller's initial check but before
      // this pending request is installed. Check again before installing it,
      // then check once more around listener registration below.
      if (signal?.aborted) {
        if (timer) clearTimeout(timer);
        reject(bridgeFailure('canceled', 'Subagent Bridge 请求已取消。'));
        return;
      }
      const pending: PendingCall = { resolve, reject, timer, onEvent };
      let abort = () => {};
      const cleanupSignal = () => signal?.removeEventListener('abort', abort);
      abort = () => this.cancel(id);

      pending.cleanupSignal = cleanupSignal;
      this.pending.set(id, pending);
      if (signal?.aborted) {
        this.cancel(id);
        return;
      }
      signal?.addEventListener('abort', abort, { once: true });
      if (signal?.aborted) {
        this.cancel(id);
        return;
      }
      child.stdin.write(`${JSON.stringify({ version: BRIDGE_PROTOCOL_VERSION, id, action, params })}\n`, (error) => {
        if (!error) return;
        this.rejectPending(id, bridgeFailure('bridge_write_failed', `无法发送 Subagent Bridge 请求：${error.message}`));
      });
    });
  }

  cancel(requestID: string, error = bridgeFailure('canceled', 'Subagent Bridge 请求已取消。')) {
    const child = this.child;
    if (child && child.exitCode === null && !child.killed) {
      child.stdin.write(`${JSON.stringify({
        version: BRIDGE_PROTOCOL_VERSION,
        id: `cancel-${requestID}`,
        action: 'cancel',
        params: { requestId: requestID },
      })}\n`);
    }
    this.rejectPending(requestID, error);
  }

  private rejectPending(requestID: string, error: Error) {
    const pending = this.pending.get(requestID);
    if (!pending) return;
    this.pending.delete(requestID);
    if (pending.timer) clearTimeout(pending.timer);
    pending.cleanupSignal?.();
    pending.reject(error);
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
      if (pending.timer) clearTimeout(pending.timer);
      pending.cleanupSignal?.();
      pending.reject(error);
    }
    this.pending.clear();
  }
}

function bridgeFailure(code: string, message: string, retryable = false) {
  const error = new Error(message) as Error & { code: string; retryable: boolean };
  error.code = code;
  error.retryable = retryable;
  return error;
}

function textResult(result: unknown) {
  return {
    content: [{ type: 'text', text: JSON.stringify(result, null, 2) }],
    details: result,
  };
}

function backgroundFailure(run: BackgroundRun, error: unknown, status: 'failed' | 'aborted' | 'timeout' = 'failed'): RunResult {
  const failure = error as { message?: string; code?: string; retryable?: boolean };
  return {
    runId: run.runId,
    agentKey: run.agentKey,
    parentNodeId: run.parentNodeId,
    status,
    files: [],
    error: String(failure?.message || error || 'Subagent Bridge 请求失败。'),
    ...(failure?.code ? { code: failure.code } : {}),
    ...(failure?.retryable ? { retryable: true } : {}),
    transcript: path.join(sessionDir, 'subagents', run.runId),
  };
}

function startedResult(run: BackgroundRun) {
  return {
    content: [{
      type: 'text',
      text: [
        `Subagent ${run.agentKey} 已在后台启动（runId: ${run.runId}）。`,
        '现在继续执行主 Agent 自己可独立完成的工作；不要等待或轮询。子 Agent 完成后，结果会自动作为 follow-up 消息送达。',
      ].join('\n'),
    }],
    details: {
      kind: 'subagent_event',
      runId: run.runId,
      agentKey: run.agentKey,
      parentNodeId: run.parentNodeId,
      status: 'running',
      detached: true,
      startedAt: run.startedAt,
    },
  };
}

function logDiagnostic(message: string, runId?: string, eventType?: string) {
  // Do not include task text, tool arguments, or results in diagnostics.
  try { console.warn(`[subagent] ${message}`, { runId, eventType }); } catch { /* logging is best effort */ }
}

function notifyCompletion(api: ExtensionAPI, result: RunResult) {
  const attempt = (retry: boolean) => {
    try {
      api.sendMessage({
        customType: 'codingto-subagent-result',
        content: [
          `后台子 Agent ${result.agentKey || 'unknown'} 已结束（runId: ${result.runId || 'unknown'}）。`,
          '请立即把下面的结果纳入当前主任务；如果其他子 Agent 仍在运行，只要还有可推进的工作，就不要等待它们。',
          JSON.stringify(result, null, 2),
        ].join('\n'),
        display: false,
        details: result,
      }, {
        deliverAs: 'followUp',
        triggerTurn: true,
      });
    } catch {
      logDiagnostic('completion delivery failed', result.runId);
      if (retry) {
        const timer = setTimeout(() => attempt(false), 2_000);
        timer.unref?.();
      }
    }
  };
  attempt(true);
}

function byteLength(value: string) {
  return Buffer.byteLength(value, 'utf8');
}

function utf8Prefix(value: string, maxBytes: number) {
  const bytes = Buffer.from(value, 'utf8');
  let end = Math.min(bytes.length, Math.max(0, maxBytes));
  while (end > 0 && (bytes[end] & 0xc0) === 0x80) end -= 1;
  return bytes.subarray(0, end).toString('utf8');
}

function truncateText(value: string, maxBytes: number) {
  if (byteLength(value) <= maxBytes) return value;
  const marker = '…[truncated]';
  const markerBytes = byteLength(marker);
  if (maxBytes <= markerBytes) return utf8Prefix(value, maxBytes);
  return `${utf8Prefix(value, maxBytes - markerBytes)}${marker}`;
}

function compactValue(value: unknown, maxBytes: number): unknown {
  if (value == null || typeof value === 'number' || typeof value === 'boolean') return value;
  if (typeof value === 'string') return truncateText(value, maxBytes);
  try {
    const serialized = JSON.stringify(value);
    if (byteLength(serialized) <= maxBytes) return value;
    return truncateText(serialized, maxBytes);
  } catch {
    return '[unserializable]';
  }
}

function compactDetachedEvent(rawEvent: unknown): BridgeNotificationEvent | null {
  let event: unknown = rawEvent;
  if (typeof event === 'string') {
    try { event = JSON.parse(event); } catch { return null; }
  }
  if (!event || typeof event !== 'object') return null;
  const source = event as Record<string, unknown>;
  const type = String(source.type || '');
  if (!detachedEventTypes.has(type)) return null;
  if (type === 'tool_execution_start' || type === 'tool_execution_end') {
    return {
      type,
      toolCallId: source.toolCallId || source.id,
      toolName: source.toolName || source.name || (source.toolCall as Record<string, unknown> | undefined)?.name,
      status: source.status || (type === 'tool_execution_end' ? 'done' : 'running'),
      _recordedAt: source._recordedAt,
      runId: typeof source.runId === 'string' ? source.runId : undefined,
      args: compactValue(source.args ?? source.input ?? source.arguments, 2 * 1024),
      ...(type === 'tool_execution_end'
        ? { output: compactValue(source.output ?? source.result, MAX_DETACHED_OUTPUT_BYTES) }
        : {}),
    };
  }
  if (type === 'extension_ui_request' && source.method === 'setWidget') {
    const widgetKey = truncateText(String(source.widgetKey || ''), 256);
    if (!Array.isArray(source.widgetLines)) {
      return {
        type, method: 'setWidget', widgetKey, widgetLines: source.widgetLines,
        _recordedAt: source._recordedAt,
        runId: typeof source.runId === 'string' ? source.runId : undefined,
      };
    }

    const sourceLines = source.widgetLines.slice(-200).map(line => String(line));
    let truncated = source.widgetLines.length > sourceLines.length;
    const lines: string[] = [];
    const widgetEvent = (nextLines: string[], markTruncated: boolean): BridgeNotificationEvent => ({
      type, method: 'setWidget', widgetKey, widgetLines: nextLines,
      _recordedAt: source._recordedAt,
      runId: typeof source.runId === 'string' ? source.runId : undefined,
      ...(markTruncated ? { truncated: true } : {}),
    });

    for (const original of sourceLines) {
      const line = truncateText(original, 512);
      const lineWasTruncated = line !== original;
      if (byteLength(JSON.stringify(widgetEvent([...lines, line], true))) <= MAX_DETACHED_EVENT_BYTES) {
        lines.push(line);
        truncated ||= lineWasTruncated;
        continue;
      }

      // Keep a partial final row when possible instead of dropping the whole
      // widget. The boolean marker is included while fitting so the returned
      // event remains within the same budget after truncation is recorded.
      let low = 0;
      let high = Math.min(512, byteLength(original));
      let best: string | undefined;
      while (low <= high) {
        const middle = Math.floor((low + high) / 2);
        const candidate = truncateText(original, middle);
        if (byteLength(JSON.stringify(widgetEvent([...lines, candidate], true))) <= MAX_DETACHED_EVENT_BYTES) {
          best = candidate;
          low = middle + 1;
        } else {
          high = middle - 1;
        }
      }
      if (best) lines.push(best);
      truncated = true;
      break;
    }

    return widgetEvent(lines, truncated);
  }
  const compact: BridgeNotificationEvent = { type };
  for (const key of ['id', 'method', 'title', 'message', 'cancelled', '_recordedAt', 'runId']) {
    if (source[key] != null) compact[key] = compactValue(source[key], 2 * 1024);
  }
  return compact;
}

function forwardDetachedEvent(api: ExtensionAPI, run: BackgroundRun, toolCallId: string, rawEvent: unknown) {
  const event = compactDetachedEvent(rawEvent);
  if (!event) return;
  if (byteLength(JSON.stringify(event)) > MAX_DETACHED_EVENT_BYTES) {
    logDiagnostic('detached event exceeded size limit', run.runId, String(event.type || 'unknown'));
    return;
  }
  try {
    api.appendEntry('codingto-subagent-event', {
      kind: 'subagent_event', runId: run.runId, agentKey: run.agentKey,
      parentNodeId: run.parentNodeId, toolCallId, status: 'running', detached: true, event,
    });
  } catch {
    logDiagnostic('detached event dropped', run.runId, String(event.type || 'unknown'));
  }
}

function createCanceledPromise(signal?: AbortSignal) {
  let cleanup = () => {};
  const promise = new Promise<'canceled'>(resolve => {
    if (!signal) return;
    const abort = () => resolve('canceled');
    cleanup = () => signal.removeEventListener('abort', abort);
    if (signal.aborted) {
      abort();
      return;
    }
    signal.addEventListener('abort', abort, { once: true });
    if (signal.aborted) {
      cleanup();
      abort();
    }
  });
  return { promise, cleanup };
}

function runID() {
  return `run-${Date.now()}-${randomBytes(6).toString('hex')}`;
}

async function parentNodeID(signal?: AbortSignal) {
  if (signal?.aborted) throw bridgeFailure('canceled', 'subagent run 已取消。');
  const read = readFile(path.join(sessionDir, '.active-change-node'), 'utf8');
  const value = await new Promise<string>((resolve, reject) => {
    const abort = () => reject(bridgeFailure('canceled', 'subagent run 已取消。'));
    if (signal?.aborted) {
      abort();
      return;
    }
    signal?.addEventListener('abort', abort, { once: true });
    if (signal?.aborted) {
      signal.removeEventListener('abort', abort);
      abort();
      return;
    }
    read.then(resolve, reject).finally(() => signal?.removeEventListener('abort', abort));
  });
  const node = value.trim();
  if (!/^turn-\d+$/.test(node)) throw bridgeFailure('change_node_unavailable', '当前主任务没有有效的产出节点。');
  return node;
}

function runRecordPath(runId: string) {
  return path.join(sessionDir, 'subagents', runId, 'run.json');
}

function isTerminalStatus(status: unknown) {
  return ['completed', 'failed', 'aborted', 'timeout'].includes(String(status || ''));
}

async function readDurableRun(runId: string): Promise<RunResult | null> {
  try {
    const record = JSON.parse(await readFile(runRecordPath(runId), 'utf8')) as RunResult;
    return { ...record, transcript: path.join(sessionDir, 'subagents', runId) };
  } catch {
    return null;
  }
}

function pruneTerminalRuns(now = Date.now()) {
  for (const [runId, entry] of terminalRuns) {
    if (entry.expiresAt <= now) terminalRuns.delete(runId);
  }
  while (terminalRuns.size > MAX_TERMINAL_RUNS) {
    const oldest = terminalRuns.keys().next().value as string | undefined;
    if (!oldest) break;
    terminalRuns.delete(oldest);
  }
}

function rememberTerminalRun(result: RunResult) {
  terminalRuns.set(result.runId, { result, expiresAt: Date.now() + TERMINAL_RUN_TTL_MS });
  pruneTerminalRuns();
}

function getTerminalRun(runId: string) {
  pruneTerminalRuns();
  const entry = terminalRuns.get(runId);
  return entry?.result;
}

async function recordTerminalRun(result: RunResult) {
  rememberTerminalRun(result);
  let temp: string | undefined;
  try {
    const current = await readDurableRun(result.runId);
    if (current && isTerminalStatus(current.status)) {
      terminalRuns.delete(result.runId);
      return;
    }
    const record = {
      ...(current || {}),
      runId: result.runId,
      agentKey: result.agentKey,
      parentNodeId: result.parentNodeId,
      status: result.status,
      error: result.error || '',
      endedAt: Date.now(),
      files: result.files || current?.files || [],
    };
    const target = runRecordPath(result.runId);
    temp = `${target}.${process.pid}.${randomBytes(4).toString('hex')}.tmp`;
    await writeFile(temp, `${JSON.stringify(record)}\n`, { encoding: 'utf8', mode: 0o600 });
    try {
      await rename(temp, target);
    } catch (firstError) {
      // Windows cannot rename over the backend's existing running run.json.
      // Remove only that target, then retry the rename; a failed retry is
      // still reported and the finally block removes the temporary file.
      try {
        await unlink(target);
      } catch (removeError) {
        if ((removeError as { code?: string })?.code !== 'ENOENT') throw firstError;
      }
      await rename(temp, target);
    }
    terminalRuns.delete(result.runId);
  } catch {
    logDiagnostic('terminal run record write failed', result.runId);
  } finally {
    if (temp) {
      try { await unlink(temp); } catch { /* already renamed or cleaned */ }
    }
  }
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
      `run 只负责后台启动并立即返回；互相独立的任务可在同一轮并行启动，当前主对话最多同时运行 ${maxConcurrency} 个。`,
      '启动后主 Agent 必须继续自己的独立工作，不要原地等待或反复调用 status；子 Agent 完成后会自动发送 follow-up 结果并触发主 Agent 继续。',
      '仅在恢复会话、核对指定 run 或自动通知疑似丢失时使用 action=status 和 runId 查询持久化状态。',
    ].join(''),
    promptSnippet: '在后台委派独立子任务，并在完成时自动接收 follow-up 结果。',
    promptGuidelines: [
      '调用 codingto_subagent 的 run 后，立即继续主 Agent 可独立完成的代码检查、实现或验证；不要因为子 Agent 正在运行而停止工作。',
      '不要轮询 codingto_subagent status。只有当前已无任何独立工作且结果尚未自动送达，或需要恢复历史 run 时才查询一次 status。',
      '收到某个子 Agent 的 follow-up 结果后立即处理它，并继续其他可推进工作；不要默认等待全部子 Agent 都完成。',
      '如果子 Agent 结果是完成用户任务所必需的，在结果尚未送达时不要宣称任务已完成；独立工作做完后只报告阶段进展，等待自动 follow-up 继续。',
    ],
    parameters: {
      type: 'object',
      properties: {
        action: { type: 'string', enum: ['list', 'run', 'status'] },
        key: { type: 'string', description: 'list 返回的子 Agent key。' },
        task: { type: 'string', description: '交给子 Agent 的完整、独立任务描述。' },
        runId: { type: 'string', description: 'status 要查询的 runId。' },
      },
      required: ['action'],
    },
    async execute(toolCallId: string, params: SubagentParams, signal?: AbortSignal, onUpdate?: (update: SubagentUpdate) => void) {
      const action = String(params?.action || '').trim();
      if (action === 'list') return textResult(await client.call('list', {}, signal));
      if (action === 'status') {
        const id = String(params?.runId || '').trim();
        if (!/^run-[A-Za-z0-9_-]{8,96}$/.test(id)) {
          throw bridgeFailure('bad_request', 'status 需要有效的 runId。');
        }
        const durable = await readDurableRun(id);
        if (durable && isTerminalStatus(durable.status)) return textResult(durable);
        const terminal = getTerminalRun(id);
        if (terminal) return textResult(terminal);
        const active = backgroundRuns.get(id);
        if (active) {
          return textResult({
            runId: active.runId, agentKey: active.agentKey, parentNodeId: active.parentNodeId,
            status: 'running', startedAt: active.startedAt,
            transcript: path.join(sessionDir, 'subagents', active.runId),
          });
        }
        try {
          const result = await client.call('status', { runId: id }, signal) as Record<string, unknown>;
          return textResult({ ...result, transcript: path.join(sessionDir, 'subagents', id) });
        } catch (error) {
          if (durable) return textResult(durable);
          throw error;
        }
      }
      if (action !== 'run') throw bridgeFailure('bad_request', 'action 必须是 list、run 或 status。');
      const key = String(params?.key || '').trim();
      const task = String(params?.task || '').trim();
      if (!key || !task) throw bridgeFailure('bad_request', 'run 需要 key 和 task。');
      if (!authorizedKeys.has(key)) throw bridgeFailure('not_authorized', `未授权的子 Agent：${key}`);
      if (signal?.aborted) throw bridgeFailure('canceled', 'subagent run 已取消。');
      const id = runID();
      const parent = await parentNodeID(signal);
      const run: BackgroundRun = {
        runId: id,
        agentKey: key,
        parentNodeId: parent,
        startedAt: Date.now(),
      };
      backgroundRuns.set(id, run);

      const emitUpdate = (value: SubagentUpdate) => {
        try { onUpdate?.(value); } catch { /* tool result may already be finalized */ }
      };
      emitUpdate({
        content: [{ type: 'text', text: `正在后台启动 Subagent ${key}…` }],
        details: {
          kind: 'subagent_event', runId: id, agentKey: key,
          parentNodeId: parent, toolCallId, status: 'running', detached: true,
        },
      });

      let acknowledgeStart!: () => void;
      const started = new Promise<void>((resolve) => { acknowledgeStart = resolve; });
      let toolReturned = false;
      let idleTimer: ReturnType<typeof setTimeout> | undefined;
      let resetIdle = () => {};
      let earlyResult: RunResult | undefined;
      let requestID = '';
      let request: Promise<unknown>;
      try {
        request = client.call('run', {
          key, task, runId: id, parentNodeId: parent, toolCallId,
        }, signal, (notification) => {
          if (notification.runId !== id || notification.agentKey !== key) return false;
          const event = typeof notification.event === 'string'
            ? (() => { try { return JSON.parse(notification.event as string); } catch { return null; } })()
            : notification.event;
          const eventObject = event && typeof event === 'object' ? event as BridgeNotificationEvent : null;
          const isStart = eventObject?.type === 'subagent_run_started'
            && eventObject.runId === id && eventObject.agentKey === key;
          if (!toolReturned) {
            if (isStart) acknowledgeStart();
            const compact = compactDetachedEvent(eventObject);
            if (compact) {
              emitUpdate({
                content: [{ type: 'text', text: isStart ? `Subagent ${key} 正在执行…` : `Subagent ${key} 正在启动…` }],
                details: {
                  kind: 'subagent_event', runId: id, agentKey: key,
                  parentNodeId: parent, toolCallId, status: 'running', detached: true,
                  event: compact,
                },
              });
            }
          } else {
            forwardDetachedEvent(api, run, toolCallId, eventObject);
            resetIdle();
          }
          // Once the durable start acknowledgement is observed, deliberately
          // detach the parent AbortSignal. Later cancellation belongs to the
          // explicit abortSubagent/.abort lifecycle, not the detached run.
          return Boolean(isStart);
        }, (value) => { requestID = value; });
      } catch (error) {
        backgroundRuns.delete(id);
        throw error;
      }
      const finished = request.then((value) => {
        const result = value && typeof value === 'object' ? value as Record<string, unknown> : {};
        const completed = { ...result, parentNodeId: parent, toolCallId } as unknown as RunResult;
        if (toolReturned) notifyCompletion(api, completed);
        else if (!earlyResult) {
          emitUpdate({
            content: [{ type: 'text', text: `Subagent ${key} 已结束。` }],
            details: { kind: 'subagent_event', ...completed, detached: true },
          });
          earlyResult = completed;
        }
        if (isTerminalStatus(completed.status)) void recordTerminalRun(completed);
        return completed;
      }).catch((error) => {
        const code = String((error as { code?: string })?.code || '');
        const failed = {
          ...backgroundFailure(
            run,
            error,
            code === 'canceled' ? 'aborted' : code === 'bridge_timeout' ? 'timeout' : 'failed',
          ),
          toolCallId,
        } as RunResult;
        void recordTerminalRun(failed);
        if (toolReturned) notifyCompletion(api, failed);
        else if (!earlyResult) {
          emitUpdate({
            content: [{ type: 'text', text: `Subagent ${key} 启动或执行失败。` }],
            details: { kind: 'subagent_event', ...failed, detached: true },
          });
          earlyResult = failed;
        }
        return failed;
      }).finally(() => {
        if (idleTimer) clearTimeout(idleTimer);
        backgroundRuns.delete(id);
      });

      resetIdle = () => {
        if (!toolReturned || BACKGROUND_IDLE_TIMEOUT_MS <= 0) return;
        if (idleTimer) clearTimeout(idleTimer);
        idleTimer = setTimeout(() => {
          if (!toolReturned || earlyResult) return;
          const idleError = bridgeFailure('bridge_timeout', 'Subagent Bridge 长时间无事件响应。');
          if (requestID) client.cancel(requestID, idleError);
        }, BACKGROUND_IDLE_TIMEOUT_MS);
        idleTimer.unref?.();
      };

      const { promise: canceled, cleanup: canceledCleanup } = createCanceledPromise(signal);
      let timeoutCleanup = () => {};
      const timeout = new Promise<'timeout'>(resolve => {
        const timer = setTimeout(() => resolve('timeout'), START_TIMEOUT_MS);
        timeoutCleanup = () => clearTimeout(timer);
      });

      // Wait only for a validated subagent_run_started event, completion, or
      // the bounded startup liveness guards. After this point the run is fully
      // detached and has no normal wall-clock deadline.
      const outcome = await Promise.race([
        started.then(() => 'started' as const),
        finished.then(() => 'finished' as const),
        canceled,
        timeout,
      ]);
      canceledCleanup();
      timeoutCleanup();
      // The bridge can emit subagent_run_started and then fail preparation in
      // the same stdout turn. In that ordering the completion handler has
      // already populated earlyResult even though Promise.race selected the
      // earlier start signal. Prefer that terminal result so the tool never
      // returns a permanently-running card without a completion follow-up.
      if (earlyResult) return textResult(earlyResult);
      if (outcome === 'started') {
        toolReturned = true;
        resetIdle();
        return startedResult(run);
      }
      if (outcome === 'finished') return textResult(await finished);

      const timeoutError = outcome === 'timeout'
        ? bridgeFailure('bridge_timeout', 'Subagent Bridge 启动确认超时。')
        : bridgeFailure('canceled', 'subagent run 已取消。');
      const terminal = {
        ...backgroundFailure(run, timeoutError, outcome === 'timeout' ? 'timeout' : 'aborted'),
        toolCallId,
      } as RunResult;
      earlyResult = terminal;
      backgroundRuns.delete(id);
      void recordTerminalRun(terminal);
      emitUpdate({
        content: [{ type: 'text', text: outcome === 'timeout' ? `Subagent ${key} 启动超时。` : `Subagent ${key} 已取消。` }],
        details: { kind: 'subagent_event', ...terminal, detached: true },
      });
      if (requestID) client.cancel(requestID, timeoutError);
      return textResult(terminal);
    },
  });
}
