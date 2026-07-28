import type { ExtensionAPI } from '@earendil-works/pi-coding-agent';
import { isUtf8 } from 'node:buffer';
import { createHash, randomBytes } from 'node:crypto';
import { mkdir, open, readFile, readdir, rename, rm, stat } from 'node:fs/promises';
import path from 'node:path';

type FileSnapshot = {
  exists: boolean;
  hash?: string;
  size: number;
  text: boolean;
  blob?: string;
};

type NodeContext = {
  id: string;
  root: string;
  nodeDir: string;
};

type TrackedPath = {
  absolutePath: string;
  relativePath: string;
  captureDir: string;
};

const MAX_DIFF_FILE_SIZE = 5 * 1024 * 1024;
const TRACKED_TOOLS = new Set(['edit', 'write', 'write_to_file']);
const sessionDir = path.resolve(process.env.CODINGTO_SESSION_DIR || process.cwd());
const activeNodePath = path.join(sessionDir, '.active-change-node');

let writeSequence = 0;
let operationQueue: Promise<void> = Promise.resolve();

function serialized<T>(operation: () => Promise<T>): Promise<T> {
  const result = operationQueue.then(operation, operation);
  operationQueue = result.then(() => undefined, () => undefined);
  return result;
}

function errorCode(error: unknown): string {
  return typeof error === 'object' && error !== null && 'code' in error
    ? String((error as { code?: unknown }).code || '')
    : '';
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function safeName(value: string): string {
  const cleaned = value.replace(/[^A-Za-z0-9._-]/g, '_').slice(0, 96);
  return cleaned || 'unknown';
}

function pathKey(relativePath: string): string {
  return createHash('sha256').update(relativePath).digest('hex');
}

function immutableName(prefix: string): string {
  writeSequence += 1;
  const timestamp = String(Date.now()).padStart(13, '0');
  const sequence = String(writeSequence).padStart(8, '0');
  return `${timestamp}-${sequence}-${safeName(prefix)}-${randomBytes(4).toString('hex')}.json`;
}

async function exists(target: string): Promise<boolean> {
  try {
    await stat(target);
    return true;
  } catch (error) {
    if (errorCode(error) === 'ENOENT') return false;
    throw error;
  }
}

async function writeAtomicNew(target: string, data: Buffer | string): Promise<boolean> {
  await mkdir(path.dirname(target), { recursive: true, mode: 0o700 });
  if (await exists(target)) return false;

  const temporary = `${target}.tmp-${process.pid}-${randomBytes(6).toString('hex')}`;
  const handle = await open(temporary, 'wx', 0o600);
  try {
    await handle.writeFile(data);
    await handle.sync();
  } finally {
    await handle.close();
  }
  try {
    await rename(temporary, target);
    return true;
  } catch (error) {
    await rm(temporary, { force: true }).catch(() => undefined);
    if (errorCode(error) === 'EEXIST' || errorCode(error) === 'EPERM') {
      if (await exists(target)) return false;
    }
    throw error;
  }
}

async function writeJSONNew(target: string, value: unknown): Promise<boolean> {
  return writeAtomicNew(target, `${JSON.stringify(value)}\n`);
}

async function loadNodeContext(): Promise<NodeContext | null> {
  let nodeID: string;
  try {
    nodeID = (await readFile(activeNodePath, 'utf8')).trim();
  } catch (error) {
    if (errorCode(error) === 'ENOENT') return null;
    throw error;
  }
  if (!/^turn-\d+$/.test(nodeID)) {
    throw new Error(`invalid active change node id: ${nodeID || '<empty>'}`);
  }

  const nodeDir = path.join(sessionDir, 'changes', 'nodes', nodeID);
  const manifestPath = path.join(nodeDir, 'manifest.json');
  const manifest = JSON.parse(await readFile(manifestPath, 'utf8')) as {
    id?: string;
    root?: string;
    status?: string;
  };
  if (manifest.id !== nodeID || !manifest.root) {
    throw new Error(`active change node manifest is invalid: ${nodeID}`);
  }
  if (manifest.status !== 'running') {
    throw new Error(`active change node is not running: ${nodeID}`);
  }
  return { id: nodeID, root: path.resolve(manifest.root), nodeDir };
}

function toolPath(input: unknown): string {
  if (!input || typeof input !== 'object') return '';
  const value = input as Record<string, unknown>;
  for (const key of ['path', 'filePath', 'file_path']) {
    if (typeof value[key] === 'string' && value[key].trim()) return value[key].trim();
  }
  return '';
}

function resolveTrackedPath(context: NodeContext, inputPath: string): TrackedPath {
  if (!inputPath) throw new Error('file mutation tool did not provide a path');
  const absolutePath = path.resolve(context.root, inputPath);
  const relative = path.relative(context.root, absolutePath);
  if (!relative || relative === '..' || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative)) {
    throw new Error(`edited file is outside the active workspace: ${inputPath}`);
  }
  const relativePath = relative.split(path.sep).join('/');
  return {
    absolutePath,
    relativePath,
    captureDir: path.join(context.nodeDir, 'captures', 'paths', pathKey(relativePath)),
  };
}

async function captureSnapshot(context: NodeContext, absolutePath: string): Promise<FileSnapshot> {
  let data: Buffer;
  try {
    data = await readFile(absolutePath);
  } catch (error) {
    if (errorCode(error) === 'ENOENT') return { exists: false, size: 0, text: true };
    throw error;
  }

  const hash = createHash('sha256').update(data).digest('hex');
  const snapshot: FileSnapshot = { exists: true, hash, size: data.length, text: false };
  if (data.length > MAX_DIFF_FILE_SIZE || !isUtf8(data) || data.includes(0)) return snapshot;

  snapshot.text = true;
  snapshot.blob = `${hash}.txt`;
  await writeAtomicNew(path.join(context.nodeDir, 'blobs', snapshot.blob), data);
  return snapshot;
}

async function captureBefore(
  context: NodeContext,
  tracked: TrackedPath,
  toolName: string,
  toolCallID: string,
): Promise<void> {
  await mkdir(tracked.captureDir, { recursive: true, mode: 0o700 });
  await writeJSONNew(path.join(tracked.captureDir, 'meta.json'), {
    version: 1,
    path: tracked.relativePath,
  });

  const beforePath = path.join(tracked.captureDir, 'before.json');
  if (!(await exists(beforePath))) {
    const snapshot = await captureSnapshot(context, tracked.absolutePath);
    await writeJSONNew(beforePath, {
      version: 1,
      recordedAt: Date.now(),
      snapshot,
    });
  }

  const callsDir = path.join(context.nodeDir, 'captures', 'calls');
  await writeJSONNew(path.join(callsDir, immutableName(`${toolCallID}-start`)), {
    version: 1,
    phase: 'start',
    nodeId: context.id,
    toolCallId: toolCallID,
    toolName,
    path: tracked.relativePath,
    recordedAt: Date.now(),
  });
}

async function captureAfter(
  context: NodeContext,
  tracked: TrackedPath,
  toolName: string,
  toolCallID: string,
  isError: boolean,
  final: boolean,
): Promise<void> {
  const snapshot = await captureSnapshot(context, tracked.absolutePath);
  const afterDir = path.join(tracked.captureDir, 'after');
  await writeJSONNew(path.join(afterDir, immutableName(final ? 'final' : toolCallID)), {
    version: 1,
    recordedAt: Date.now(),
    final,
    toolCallId: toolCallID,
    snapshot,
  });

  if (!final) {
    const callsDir = path.join(context.nodeDir, 'captures', 'calls');
    await writeJSONNew(path.join(callsDir, immutableName(`${toolCallID}-end`)), {
      version: 1,
      phase: 'end',
      nodeId: context.id,
      toolCallId: toolCallID,
      toolName,
      path: tracked.relativePath,
      isError,
      recordedAt: Date.now(),
    });
  }
}

async function captureFinalState(context: NodeContext): Promise<void> {
  const pathsRoot = path.join(context.nodeDir, 'captures', 'paths');
  let entries;
  try {
    entries = await readdir(pathsRoot, { withFileTypes: true });
  } catch (error) {
    if (errorCode(error) === 'ENOENT') return;
    throw error;
  }
  for (const entry of entries) {
    if (!entry.isDirectory()) continue;
    const captureDir = path.join(pathsRoot, entry.name);
    const meta = JSON.parse(await readFile(path.join(captureDir, 'meta.json'), 'utf8')) as { path?: string };
    const tracked = resolveTrackedPath(context, String(meta.path || ''));
    if (path.resolve(tracked.captureDir) !== path.resolve(captureDir)) {
      throw new Error(`change capture path key mismatch: ${meta.path || '<empty>'}`);
    }
    await captureAfter(context, tracked, 'agent_end', 'agent-end', false, true);
  }
}

export default function codingToChangeCapture(api: ExtensionAPI) {
  api.on('tool_call', async (event: any) => {
    if (!TRACKED_TOOLS.has(String(event?.toolName || ''))) return;
    return serialized(async () => {
      try {
        const context = await loadNodeContext();
        if (!context) return;
        const tracked = resolveTrackedPath(context, toolPath(event?.input));
        await captureBefore(
          context,
          tracked,
          String(event.toolName),
          String(event.toolCallId || 'unknown'),
        );
      } catch (error) {
        return {
          block: true,
          reason: `CodingTo 无法在写入前保存文件快照，已阻止本次修改：${errorMessage(error)}`,
        };
      }
    });
  });

  api.on('tool_result', async (event: any) => {
    if (!TRACKED_TOOLS.has(String(event?.toolName || ''))) return;
    return serialized(async () => {
      const context = await loadNodeContext();
      if (!context) return;
      const tracked = resolveTrackedPath(context, toolPath(event?.input));
      await captureAfter(
        context,
        tracked,
        String(event.toolName),
        String(event.toolCallId || 'unknown'),
        Boolean(event?.isError),
        false,
      );
    });
  });

  api.on('agent_end', async () => {
    await serialized(async () => {
      const context = await loadNodeContext();
      if (!context) return;
      await captureFinalState(context);
    }).catch(() => undefined);
  });
}
