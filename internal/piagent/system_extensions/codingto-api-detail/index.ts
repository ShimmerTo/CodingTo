import type { ExtensionAPI } from '@earendil-works/pi-coding-agent';
import { createHash, randomBytes } from 'node:crypto';
import { access, mkdir, open, rename, rm } from 'node:fs/promises';
import path from 'node:path';

type PendingRequest = {
  id: string;
  startedAt: number;
  payload: unknown;
  response?: { receivedAt: number; status: number };
};

type DetailWrite = {
  request: PendingRequest;
  completedAt: number;
  result: unknown;
};

const sessionDir = path.resolve(process.env.CODINGTO_SESSION_DIR || process.cwd());
const outputDir = path.resolve(process.env.CODINGTO_API_DETAIL_DIR || path.join(sessionDir, 'api'));
const markerPath = String(process.env.CODINGTO_API_DETAIL_MARKER || '').trim();

// Provider calls are normally much slower than local disk writes, so this
// bound leaves ample burst capacity while preventing an unhealthy disk from
// retaining an unlimited number of full request/result objects in memory.
const maxQueuedWrites = 256;

let sequence = 0;
let pending: PendingRequest[] = [];
let writeQueue: DetailWrite[] = [];
let writerPromise: Promise<void> | null = null;
let droppedWrites = 0;

async function recordingEnabled(): Promise<boolean> {
  if (!markerPath) return false;
  try {
    await access(markerPath);
    return true;
  } catch {
    return false;
  }
}

function pad(value: number, width = 2): string {
  return String(value).padStart(width, '0');
}

function localPrefix(timestamp: number): string {
  const value = new Date(timestamp);
  return `${value.getFullYear()}-${pad(value.getMonth() + 1)}-${pad(value.getDate())}`
    + `_${pad(value.getHours())}-${pad(value.getMinutes())}-${pad(value.getSeconds())}-${pad(value.getMilliseconds(), 3)}`;
}

function safeName(value: unknown): string {
  const cleaned = String(value || '').replace(/[^A-Za-z0-9._-]/g, '_').slice(0, 72);
  return cleaned || 'unknown';
}

function safeJSONStringify(value: unknown): string {
  const seen = new WeakSet<object>();
  const serialized = JSON.stringify(value, (_key, item) => {
    if (typeof item === 'bigint') return item.toString();
    if (!item || typeof item !== 'object') return item;
    if (seen.has(item)) return '[Circular]';
    seen.add(item);
    return item;
  });
  return serialized || '{}';
}

async function writeAtomicJSON(target: string, content: string): Promise<void> {
  await mkdir(path.dirname(target), { recursive: true, mode: 0o700 });
  const temporary = `${target}.tmp-${process.pid}-${randomBytes(6).toString('hex')}`;
  try {
    const handle = await open(temporary, 'wx', 0o600);
    try {
      await handle.writeFile(`${content}\n`);
      await handle.sync();
    } finally {
      await handle.close();
    }
    await rename(temporary, target);
  } catch (error) {
    try {
      await rm(temporary, { force: true });
    } catch (cleanupError) {
      const primary = error instanceof Error ? error.message : String(error);
      const cleanup = cleanupError instanceof Error ? cleanupError.message : String(cleanupError);
      throw new Error(`${primary}; temporary file cleanup failed: ${cleanup}`);
    }
    throw error;
  }
}

function requestFileName(request: PendingRequest, result: any, completedAt: number): string {
  const responseId = String(result?.responseId || '').trim();
  if (responseId) {
    const responseKey = createHash('sha256').update(responseId).digest('hex').slice(0, 24);
    return `${localPrefix(completedAt).slice(0, 10)}_response_${responseKey}.json`;
  }
  sequence += 1;
  const payload = request.payload as any;
  const provider = safeName(result?.provider || payload?.provider);
  const model = safeName(result?.model || payload?.model);
  return `${localPrefix(completedAt)}_${pad(sequence, 6)}_${provider}_${model}_${request.id}.json`;
}

function reportWriteError(error: unknown): void {
  const message = error instanceof Error ? error.message : String(error);
  // Never log request parameters or model results; only the filesystem error.
  console.error(`[codingto-api-detail] failed to write request detail: ${message}`);
}

function reportQueueOverflow(): void {
  droppedWrites += 1;
  if (droppedWrites === 1 || droppedWrites % 100 === 0) {
    console.error(`[codingto-api-detail] write queue is full; dropped ${droppedWrites} detail record(s)`);
  }
}

async function writeDetail(item: DetailWrite): Promise<void> {
  const result = item.result as any;
  const content = safeJSONStringify({
    version: 1,
    requestId: item.request.id,
    startedAt: item.request.startedAt,
    completedAt: item.completedAt,
    request: item.request.payload,
    response: {
      ...(item.request.response || {}),
      result,
    },
  });
  const target = path.join(outputDir, requestFileName(item.request, result, item.completedAt));
  await writeAtomicJSON(target, content);
}

async function drainWriteQueue(): Promise<void> {
  while (writeQueue.length > 0) {
    const item = writeQueue.shift();
    if (!item) continue;
    try {
      await writeDetail(item);
    } catch (error) {
      reportWriteError(error);
    }
  }
}

function scheduleWriter(): void {
  if (writerPromise) return;
  // setImmediate guarantees that the provider/message hook returns before any
  // JSON serialization or filesystem work starts.
  writerPromise = new Promise<void>(resolve => setImmediate(resolve))
    .then(() => drainWriteQueue())
    .finally(() => {
      writerPromise = null;
      if (writeQueue.length > 0) scheduleWriter();
    });
}

function enqueueWrite(item: DetailWrite): void {
  if (writeQueue.length >= maxQueuedWrites) {
    reportQueueOverflow();
    return;
  }
  writeQueue.push(item);
  scheduleWriter();
}

function enqueuePending(result: unknown): void {
  const requests = pending;
  pending = [];
  const completedAt = Date.now();
  for (let index = 0; index < requests.length; index += 1) {
    enqueueWrite({
      request: requests[index],
      completedAt,
      // Provider retries are separate requests. Only the final successful
      // attempt owns the parsed assistant result; earlier attempts retain
      // their own HTTP status and payload.
      result: index === requests.length - 1 ? result : null,
    });
  }
}

async function waitForWriter(): Promise<void> {
  while (writerPromise) {
    await writerPromise;
  }
}

export default function codingToAPIDetail(api: ExtensionAPI) {
  api.on('before_provider_request', async (event: any) => {
    try {
      if (!await recordingEnabled()) return;
      pending.push({
        id: randomBytes(8).toString('hex'),
        startedAt: Date.now(),
        // Keep the already assembled provider payload by reference. It is
        // serialized by the background writer after this hook returns.
        payload: event?.payload,
      });
    } catch (error) {
      reportWriteError(error);
    }
  });

  api.on('after_provider_response', async (event: any) => {
    const request = [...pending].reverse().find(item => !item.response);
    if (!request) return;
    request.response = { receivedAt: Date.now(), status: Number(event?.status) || 0 };
  });

  api.on('message_end', async (event: any) => {
    if (String(event?.message?.role || '') !== 'assistant') return;
    enqueuePending(event.message);
  });

  api.on('session_shutdown', async () => {
    enqueuePending(null);
    await waitForWriter();
  });
}
