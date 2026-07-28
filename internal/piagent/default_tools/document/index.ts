import type { ExtensionAPI } from '@earendil-works/pi-coding-agent';
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import { readFile, realpath, stat } from 'node:fs/promises';
import path from 'node:path';
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

const bridgeBin = String(process.env.CODINGTO_DOCUMENT_BRIDGE_BIN || '').trim();
const sessionDir = path.resolve(process.env.CODINGTO_SESSION_DIR || process.cwd());
const workDir = path.resolve(process.env.CODINGTO_WORK_DIR || process.cwd());
const modalities = new Set(
  String(process.env.CODINGTO_MODEL_INPUT_MODALITIES || 'text')
    .split(',')
    .map((value) => value.trim().toLowerCase())
    .filter(Boolean),
);
const objectRoot = path.resolve(sessionDir, '.document-bridge', 'objects');
const workRootPromise = realpath(workDir).catch(() => workDir);
const sessionRootPromise = realpath(sessionDir).catch(() => sessionDir);

class BridgeClient {
  private child: ChildProcessWithoutNullStreams | null = null;
  private pending = new Map<string, PendingCall>();
  private nextID = 0;
  private stderr = '';

  private start() {
    if (this.child && !this.child.killed && this.child.exitCode === null) return;
    if (!bridgeBin) {
      throw bridgeFailure('bridge_unavailable', 'Document Bridge 未配置；请重启 CodingTo 或检查 helper 安装。');
    }
    const child = spawn(bridgeBin, [
      'document-bridge',
      'serve',
      '--session-dir', sessionDir,
      '--work-dir', workDir,
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
        this.failAll(bridgeFailure('bridge_protocol_error', 'Document Bridge 返回了无效 JSON。'));
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
          response.error?.message || 'Document Bridge 请求失败。',
        ));
      }
    });
    child.stderr.on('data', (chunk) => {
      if (this.stderr.length < 4096) this.stderr += String(chunk).slice(0, 4096 - this.stderr.length);
    });
    child.once('error', (error) => {
      this.failAll(bridgeFailure('bridge_start_failed', `无法启动 Document Bridge：${error.message}`));
    });
    child.once('exit', (code) => {
      if (this.child === child) this.child = null;
      this.failAll(bridgeFailure(
        'bridge_exited',
        `Document Bridge 意外退出（${code ?? 'unknown'}）。`,
      ));
    });
  }

  call(action: string, params: any, signal?: AbortSignal): Promise<any> {
    this.start();
    const child = this.child!;
    const id = `document-${process.pid}-${++this.nextID}`;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(bridgeFailure('bridge_timeout', 'Document Bridge 请求超时。'));
        this.terminate();
      }, 120_000);
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
        reject(bridgeFailure('bridge_write_failed', `无法发送 Document Bridge 请求：${error.message}`));
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
  if (!['inspect', 'read', 'search', 'sheet', 'image', 'preview', 'create', 'download_list'].includes(action)) {
    throw bridgeFailure('bad_request', 'action 必须是 inspect、read、search、sheet、image、preview、create 或 download_list。');
  }
  if (action === 'inspect') required(params, 'path', '{"action":"inspect","path":"C:/path/demo.pdf"}');
  if (['read', 'search', 'sheet', 'image', 'preview'].includes(action)) {
    required(params, 'documentId', `{"action":"${action}","documentId":"doc_..."}`);
  }
  if (action === 'search') required(params, 'query', '{"action":"search","documentId":"doc_...","query":"关键词"}');
  if (action === 'image' && !String(params?.imageId || '').trim() && !String(params?.blockId || '').trim()) {
    throw bridgeFailure('bad_request', 'image 需要 imageId 或 blockId。');
  }
  if (action === 'create') {
    const format = String(params?.format || '').trim().toLowerCase();
    if (!['docx', 'xlsx', 'pdf', 'zip', 'md', 'txt', 'csv', 'html', 'json', 'rtf'].includes(format)) {
      throw bridgeFailure('bad_request', 'create.format 必须是 docx、xlsx、pdf、zip、md、txt、csv、html、json 或 rtf。');
    }
    required(params, 'path', '{"action":"create","format":"md","path":"notes.md"}');
    if (format === 'zip') required(params, 'paths', '{"action":"create","format":"zip","path":"bundle.zip","paths":["a.md","report.pdf"]}');
  }
  if (action === 'download_list') {
    if (!Array.isArray(params?.paths) || params.paths.length === 0) {
      throw bridgeFailure('bad_request', 'download_list 需要 paths 数组（绝对路径，且位于工作目录或会话目录内）。');
    }
  }
  return action;
}

function textResult(result: any, extra = '') {
  const text = `${JSON.stringify(result, null, 2)}${extra}`;
  return { content: [{ type: 'text', text }], details: result };
}

function safeArtifactPath(relative: string) {
  if (!relative || path.isAbsolute(relative)) {
    throw bridgeFailure('bridge_protocol_error', 'Document Bridge 返回了不安全的图片路径。');
  }
  const absolute = path.resolve(sessionDir, relative);
  const rel = path.relative(objectRoot, absolute);
  if (rel === '' || rel === '..' || rel.startsWith(`..${path.sep}`) || path.isAbsolute(rel)) {
    throw bridgeFailure('bridge_protocol_error', 'Document Bridge 图片路径超出会话对象目录。');
  }
  return absolute;
}

function within(base: string, target: string) {
  const rel = path.relative(base, target);
  return rel === '' || (rel !== '..' && !rel.startsWith(`..${path.sep}`) && !path.isAbsolute(rel));
}

function kindOf(filePath: string): string {
  const ext = (path.extname(filePath) || '').toLowerCase().replace(/^\./, '');
  if (ext === 'docx' || ext === 'doc') return 'word';
  if (ext === 'xlsx' || ext === 'xls') return 'excel';
  if (ext === 'pdf') return 'pdf';
  if (ext === 'zip') return 'zip';
  if (ext === 'md') return 'markdown';
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'tiff'].includes(ext)) return 'image';
  if (['txt', 'csv', 'json', 'log', 'yaml', 'yml'].includes(ext)) return 'text';
  return 'file';
}

async function safeDownloadPath(input: string): Promise<string> {
  const p = String(input || '').trim();
  if (!p) throw bridgeFailure('bad_request', '空的文件路径。');
  const abs = path.resolve(p);
  const resolved = await realpath(abs);
  const [workRoot, sessionRoot] = await Promise.all([workRootPromise, sessionRootPromise]);
  if (!within(workRoot, resolved) && !within(sessionRoot, resolved)) {
    throw bridgeFailure('bad_request', `路径不在工作目录或会话目录内：${p}`);
  }
  return resolved;
}

async function buildDownloadList(params: any) {
  const rawPaths = (params?.paths || []).map(String);
  const files: any[] = [];
  for (const p of rawPaths) {
    const abs = await safeDownloadPath(p);
    const info = await stat(abs);
    if (!info.isFile()) throw bridgeFailure('bad_request', `不是普通文件：${p}`);
    files.push({
      name: path.basename(abs),
      path: abs,
      size: info.size,
      ext: (path.extname(abs) || '').toLowerCase().replace(/^\./, ''),
      kind: kindOf(abs),
    });
  }
  return { status: 'ok', count: files.length, downloadList: files };
}

const client = new BridgeClient();
process.once('beforeExit', () => client.close());
process.once('exit', () => client.close());

export default function (api: ExtensionAPI) {
  api.registerTool({
    name: 'codingto_document',
    description: [
      '本地文档与图片解析与创建工具。支持 PDF、DOCX、XLSX、CSV、TXT、MD、PNG/JPEG/GIF/WEBP/BMP/TIFF。',
      '首次处理文件必须先 inspect；大文档先 search 再 read，不要一次读取全文；电子表格用 sheet。',
      'image 可读取文档内图片；纯文本模型会收到本地 OCR 提取的图片文字，支持图片的模型还会收到图片内容。',
      '用户已直传给视觉模型的普通图片通常无需重复调用。preview 只请求 CodingTo 界面打开解析产物。',
      'create 可在工作目录生成 md/txt/csv/html/json/rtf/docx/xlsx/pdf/zip 文件：content 为 { title, elements:[{type:"heading"|"paragraph"|"list"|"table", text, items, headers, rows, level}] }，md/txt/csv/html 也可直接传字符串，json 直接序列化 content；zip 使用 paths 打包现有文件（仅支持文件，不支持目录）。',
      'download_list 传入 paths（需位于工作目录或会话目录内），返回对话内可下载文件列表，用户可点击下载图标保存并打开。',
    ].join(''),
    parameters: {
      type: 'object',
      properties: {
        action: { type: 'string', enum: ['inspect', 'read', 'search', 'sheet', 'image', 'preview', 'create', 'download_list'] },
        path: { type: 'string', description: 'inspect 使用的本地归档路径；create 使用的输出相对路径（如 report.pdf）。' },
        format: { type: 'string', enum: ['docx', 'xlsx', 'pdf', 'zip', 'md', 'txt', 'csv', 'html', 'json', 'rtf'], description: 'create 的文件格式。' },
        content: {
          anyOf: [{ type: 'object' }, { type: 'string' }],
          description: 'create 的文档内容：{ title, elements:[{type:"heading"|"paragraph"|"list"|"table", text, items, headers, rows, level}] }；md/txt/csv/html/rtf 也可直接传入字符串。',
        },
        paths: { type: 'array', items: { type: 'string' }, description: 'create(format=zip) 要打包的现有文件路径；download_list 要展示的文件路径（均需在 workDir/sessionDir 内）。' },
        overwrite: { type: 'boolean', description: 'create 时若文件已存在是否覆盖，默认 false。' },
        documentId: { type: 'string', description: 'inspect 返回的稳定文档 ID。' },
        query: { type: 'string', description: 'search 使用的关键词。' },
        pageStart: { type: 'number' },
        pageEnd: { type: 'number' },
        maxChars: { type: 'number' },
        sheet: { type: 'string' },
        range: { type: 'string', description: 'A1:H100 格式。' },
        maxRows: { type: 'number' },
        imageId: { type: 'string' },
        blockId: { type: 'string' },
        page: { type: 'number' },
      },
      required: ['action'],
    },
    async execute(_id: string, params: any, signal?: AbortSignal, onUpdate?: any) {
      try {
        const action = validateAction(params);
        if (action === 'preview') {
          const result = {
            status: 'requested',
            documentId: String(params.documentId),
            page: Math.max(1, Number(params.page || 1)),
          };
          return textResult(result, '\n已请求 CodingTo 打开文档预览。');
        }
        if (action === 'download_list') {
          const list = await buildDownloadList(params);
          return textResult(list, `\n已生成 ${list.downloadList.length} 个可下载文件。`);
        }
        if (action === 'create') {
          const { action: _create, ...requestParams } = params as any;
          const result = await client.call('create', requestParams, signal);
          return textResult(result, `\n已创建 ${result.format} 文件：${result.name}`);
        }
        onUpdate?.({
          content: [{ type: 'text', text: `正在执行文档 ${action}…` }],
          details: { status: 'working', action },
        });
        const { action: _action, ...requestParams } = params;
        const result = await client.call(action, requestParams, signal);
        if (action !== 'image') return textResult(result);

        const ocrNotice = result.ocrText
          ? `\n\nOCR 文字：\n${result.ocrText}`
          : `\n\nOCR 未提取到文字${result.ocrWarnings?.length ? `：${result.ocrWarnings.join('；')}` : '。'}`;
        const visible = { ...result };
        delete visible.ocrText;
        if (!modalities.has('image')) {
          return {
            content: [{ type: 'text', text: `${JSON.stringify(visible, null, 2)}${ocrNotice}` }],
            details: result,
          };
        }
        const absolute = safeArtifactPath(String(result.artifactPath || ''));
        const data = await readFile(absolute);
        return {
          content: [
            { type: 'text', text: `${JSON.stringify(visible, null, 2)}${ocrNotice}` },
            { type: 'image', data: data.toString('base64'), mimeType: result.mime },
          ],
          details: result,
        } as any;
      } catch (error: any) {
        const result = {
          status: 'error',
          code: String(error?.code || 'document_error'),
          message: String(error?.message || error || 'Document Bridge 请求失败。'),
        };
        return textResult(result);
      }
    },
  });
}
