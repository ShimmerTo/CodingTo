import type { ExtensionAPI } from '@earendil-works/pi-coding-agent';
import { readFile, readdir, rm, writeFile, mkdir } from 'node:fs/promises';
import path from 'node:path';

type ServiceStatus = 'ready' | 'login_required' | 'not_ready' | 'ok' | 'closed' | 'error';

type ServiceResponse = {
  status: ServiceStatus;
  leaseId?: string;
  code?: string;
  message?: string;
  output?: string;
};

type ProfileMeta = {
  id: string;
  origins: string[];
  lastUsed: string;
};

type LeaseState = {
  version: 1;
  leaseId: string;
  targetUrl: string;
  origin: string;
  createdAt: string;
};

const agentDir = path.resolve(process.env.PI_CODING_AGENT_DIR || '.');
const sessionDir = path.resolve(process.env.CODINGTO_SESSION_DIR || process.cwd());
const profilesRoot = process.env.CODINGTO_BROWSER_PROFILES_DIR
	? path.resolve(process.env.CODINGTO_BROWSER_PROFILES_DIR)
	: path.join(agentDir, 'browser-profile', 'profiles');
const scratchRoot = path.join(sessionDir, '.scratch', 'browser-profile');
const pendingPath = path.join(scratchRoot, 'pending-lease.json');
const activeLeasePath = path.join(scratchRoot, 'active-lease.json');

const serviceURL = String(process.env.CODINGTO_BROWSER_SERVICE_URL || '').replace(/\/+$/, '');
const serviceToken = String(process.env.CODINGTO_BROWSER_SERVICE_TOKEN || '');
const ownerAgentID = String(process.env.CODINGTO_BROWSER_AGENT_ID || '');
const ownerSessionID = String(process.env.CODINGTO_BROWSER_SESSION_ID || '');

const IDENTITY_DIALOG_TITLE = '选择浏览器身份';
const NEW_PROFILE_OPTION = '+ 新建 Profile';
const LOGIN_PATH_RE = /\/(login|signin|sign-in|sign_in|signup|sso|oauth|auth)([/?#]|$)/i;
const RETURN_URL_KEYS = ['ref', 'return_url', 'returnUrl', 'redirect', 'redirect_uri', 'continue'];

function isValidProfileKey(value: string) {
  return /^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,62}[A-Za-z0-9])?$/.test(value)
    && !/^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\..*)?$/i.test(value);
}

function parseTarget(raw: string): { url: string; origin: string } | null {
  let parsed: URL;
  try {
    parsed = new URL(raw.trim());
  } catch {
    return null;
  }
  if (
    (parsed.protocol !== 'http:' && parsed.protocol !== 'https:')
    || parsed.username
    || parsed.password
    || !parsed.hostname
  ) {
    return null;
  }
  parsed.hash = '';
  return { url: parsed.href, origin: parsed.origin.toLowerCase() };
}

export function normalizeTarget(raw: string): { url: string; origin: string } | null {
  const target = parseTarget(raw);
  if (!target) return null;
  const parsed = new URL(target.url);
  if (!LOGIN_PATH_RE.test(parsed.pathname)) return target;
  for (const key of RETURN_URL_KEYS) {
    const nested = parseTarget(parsed.searchParams.get(key) || '');
    if (
      nested
      && nested.origin === target.origin
      && !LOGIN_PATH_RE.test(new URL(nested.url).pathname)
    ) {
      return nested;
    }
  }
  return target;
}

async function readProfileMeta(profileKey: string): Promise<ProfileMeta | null> {
  if (!isValidProfileKey(profileKey)) return null;
  try {
    const profile = JSON.parse(
      await readFile(path.join(profilesRoot, profileKey, 'profile.json'), 'utf8'),
    );
    if (profile.id !== profileKey) return null;
    const origins = Array.isArray(profile.origins)
      ? profile.origins.filter((value: unknown) => typeof value === 'string')
        .map((value: string) => value.toLowerCase())
      : [];
    return {
      id: profileKey,
      origins,
      lastUsed: String(profile.lastUsedAt || profile.updatedAt || ''),
    };
  } catch {
    return null;
  }
}

async function listProfilesForOrigin(origin: string): Promise<ProfileMeta[]> {
  let entries: string[];
  try {
    entries = await readdir(profilesRoot);
  } catch {
    return [];
  }
  const profiles: ProfileMeta[] = [];
  for (const entry of entries) {
    const profile = await readProfileMeta(entry);
    if (profile?.origins.includes(origin)) profiles.push(profile);
  }
  profiles.sort((left, right) => right.lastUsed.localeCompare(left.lastUsed));
  return profiles;
}

async function readLease(file: string): Promise<LeaseState | null> {
  try {
    const state = JSON.parse(await readFile(file, 'utf8')) as LeaseState;
    const target = normalizeTarget(state.targetUrl);
    if (
      state.version !== 1
      || !/^bl_[0-9a-f]{24}$/i.test(state.leaseId)
      || !target
      || target.origin !== state.origin
    ) {
      await clearLease(file);
      return null;
    }
    return state;
  } catch {
    return null;
  }
}

async function writeLease(file: string, response: ServiceResponse, target: { url: string; origin: string }) {
  if (!response.leaseId) return;
  await mkdir(scratchRoot, { recursive: true });
  await writeFile(file, JSON.stringify({
    version: 1,
    leaseId: response.leaseId,
    targetUrl: target.url,
    origin: target.origin,
    createdAt: new Date().toISOString(),
  } satisfies LeaseState, null, 2), { mode: 0o600 });
}

async function clearLease(file: string) {
  try {
    await rm(file);
  } catch {
    // Missing state is already clear.
  }
}

function serviceConfigured() {
  return serviceURL.startsWith('http://127.0.0.1:')
    && serviceToken.length >= 32
    && ownerAgentID.length > 0
    && /^\d+$/.test(ownerSessionID);
}

async function callService(
  method: 'POST' | 'DELETE',
  endpoint: string,
  body?: unknown,
): Promise<ServiceResponse> {
  if (!serviceConfigured()) {
    return {
      status: 'error',
      code: 'SERVICE_UNAVAILABLE',
      message: 'Browser Session Service 未启动，请重启 CodingTo 后重试。',
    };
  }
  const response = await fetch(`${serviceURL}${endpoint}`, {
    method,
    headers: {
      Authorization: `Bearer ${serviceToken}`,
      'Content-Type': 'application/json',
      'X-CodingTo-Agent-ID': ownerAgentID,
      'X-CodingTo-Session-ID': ownerSessionID,
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(120_000),
  });
  let result: ServiceResponse;
  try {
    result = await response.json() as ServiceResponse;
  } catch {
    result = { status: 'error', code: 'SERVICE_PROTOCOL_ERROR', message: '浏览器服务返回了无效响应。' };
  }
  return result;
}

function toolResult(response: ServiceResponse) {
  const visible = {
    status: response.status,
    ...(response.leaseId ? { leaseId: response.leaseId } : {}),
    ...(response.code ? { code: response.code } : {}),
    ...(response.message ? { message: response.message } : {}),
  };
  let hint = '';
  if (response.status === 'ready') {
    hint = '\n目标页面已经打开。后续使用 codingto_browser_execute，不要调用 agent_browser 或重复 open。';
  } else if (response.status === 'login_required' || response.status === 'not_ready') {
    hint = '\n请让用户在已打开的窗口中完成登录或安全检查，然后结束当前回合。';
  }
  return {
    content: [{ type: 'text', text: JSON.stringify(visible) + hint }],
    details: visible,
  };
}

async function rememberResponse(
  response: ServiceResponse,
  target: { url: string; origin: string },
) {
  if (response.status === 'ready') {
    await clearLease(pendingPath);
    await writeLease(activeLeasePath, response, target);
  } else if (response.status === 'login_required' || response.status === 'not_ready') {
    await clearLease(activeLeasePath);
    await writeLease(pendingPath, response, target);
  } else if (
    response.status === 'error'
    && (response.code === 'CHROME_EXITED' || response.code === 'LEASE_NOT_FOUND')
  ) {
    await clearLease(pendingPath);
    await clearLease(activeLeasePath);
  }
}

async function closeRememberedLease(state: LeaseState | null) {
  if (state) {
    await callService('DELETE', `/v1/browser/${encodeURIComponent(state.leaseId)}`);
  }
  await clearLease(pendingPath);
  await clearLease(activeLeasePath);
}

function textResult(details: any) {
  return {
    content: [{ type: 'text', text: JSON.stringify(details) }],
    details,
  };
}

export default function (api: ExtensionAPI) {
  api.registerTool({
    name: 'codingto_browser_prepare',
    description: [
      'Browser Profile 登录态管理器。仅当你已经访问目标 URL 并发现页面需要登录时调用，公开页面不要调用。',
      '必须传入用户最初给出的目标页面 URL，不要传登录重定向 URL。',
      '本工具让用户选择同域 Profile（最多展示 20 个），选择“+ 新建 Profile”会在同一对话框内联输入新 Key 并由 Go 服务创建；然后由 CodingTo 的 Go 服务启动并持有可见 Chrome。',
      'status=ready 时只会返回 leaseId；目标页已经打开，后续必须调用 codingto_browser_execute。',
      'status=login_required 或 not_ready 时，让用户在窗口中登录或完成人机验证，然后结束当前回合。',
      '用户发来下一条消息后再次调用本工具验证；没有固定次数上限，窗口由 Go 服务保持。',
      '不要自行读取 Profile 路径、Cookie、CDP 端口或 Chrome 进程信息。',
    ].join(''),
    parameters: {
      type: 'object',
      properties: {
        url: { type: 'string', description: '已确认需要登录的 http/https 目标页面地址。' },
      },
      required: ['url'],
    },
    async execute(_id: string, params: any, _signal?: any, onUpdate?: any, ctx?: any) {
      const target = normalizeTarget(String(params?.url || ''));
      if (!target) {
        return toolResult({ status: 'error', code: 'INVALID_URL', message: '仅支持不带内嵌凭据的 http/https URL。' });
      }
      const progress = (text: string) => onUpdate?.({
        content: [{ type: 'text', text }],
        details: { status: 'working' },
      });
      try {
        const pending = await readLease(pendingPath);
        if (pending?.targetUrl === target.url) {
          progress('正在通过 Go 服务验证当前浏览器页面…');
          const response = await callService(
            'POST',
            `/v1/browser/${encodeURIComponent(pending.leaseId)}/verify`,
          );
          await rememberResponse(response, {
            url: pending.targetUrl,
            origin: pending.origin,
          });
          if (response.code !== 'LEASE_NOT_FOUND') return toolResult(response);
        }
        if (pending) await closeRememberedLease(pending);

        const active = await readLease(activeLeasePath);
        if (active?.targetUrl === target.url) {
          progress('正在验证现有 Browser Lease…');
          const response = await callService(
            'POST',
            `/v1/browser/${encodeURIComponent(active.leaseId)}/verify`,
          );
          await rememberResponse(response, target);
          if (response.code !== 'LEASE_NOT_FOUND') return toolResult(response);
        }
        if (active) await closeRememberedLease(active);

        for (;;) {
          const candidates = await listProfilesForOrigin(target.origin);
          if (!ctx?.ui?.select) {
            return toolResult({ status: 'error', code: 'UI_UNAVAILABLE', message: '当前客户端不能显示 Browser Profile 选择窗口。' });
          }
          const options = candidates.slice(0, 20).map((profile) => ({
            label: profile.id,
            value: profile.id,
            description: profile.origins.join(', '),
          }));
          options.push({
            label: NEW_PROFILE_OPTION,
            value: NEW_PROFILE_OPTION,
            createProfile: true,
            targetUrl: target.url,
          });
          const picked = await ctx.ui.select(
            `${IDENTITY_DIALOG_TITLE} · ${target.origin}`,
            options,
          );
          if (!picked) {
            return toolResult({ status: 'error', code: 'AUTH_CANCELLED', message: '用户取消了 Browser Profile 选择。' });
          }
          if (!isValidProfileKey(picked)) {
            return toolResult({ status: 'error', code: 'INVALID_PROFILE_KEY', message: 'Browser Profile Key 不合法。' });
          }
          const profile = await readProfileMeta(picked);
          if (!profile) {
            return toolResult({ status: 'error', code: 'PROFILE_UNAVAILABLE', message: '未能读取所选 Browser Profile。' });
          }

          progress('正在由 Go 服务启动 Browser Profile 窗口…');
          const response = await callService('POST', '/v1/browser/prepare', {
            agentId: ownerAgentID,
            codingToSessionId: Number(ownerSessionID),
            profileId: profile.id,
            targetUrl: target.url,
          });
          if (response.code === 'PROFILE_RECLAIMED' || response.code === 'PROFILE_BUSY') {
            progress(response.code === 'PROFILE_RECLAIMED'
              ? '旧的 Profile 浏览器已关闭，请重新选择 Profile。'
              : 'Profile 正在启动，暂时不能回收，请选择其他 Profile 或稍后重试。');
            continue;
          }
          await rememberResponse(response, target);
          return toolResult(response);
        }
      } catch {
        return toolResult({
          status: 'error',
          code: 'SERVICE_REQUEST_FAILED',
          message: '无法连接 Browser Session Service，请重启 CodingTo 后重试。',
        });
      }
    },
  });

  api.registerTool({
    name: 'codingto_browser_execute',
    description: [
      '在 Go 持有的 Browser Lease 上执行浏览器操作。',
      'leaseId 必须来自 codingto_browser_prepare。',
      '允许 snapshot、click、fill、type、press、hover、scroll、select、check、uncheck、get、eval、screenshot、wait、find、back、forward、reload。',
      '禁止 close、quit、exit、open，以及 --profile、--cdp、--session 等连接参数。',
    ].join(''),
    parameters: {
      type: 'object',
      properties: {
        leaseId: { type: 'string', description: 'codingto_browser_prepare 返回的 leaseId。' },
        args: { type: 'array', items: { type: 'string' }, description: 'agent-browser 命令参数，例如 ["snapshot","-i"]。' },
        timeoutMs: { type: 'number', description: '可选超时，1000 到 120000 毫秒。' },
      },
      required: ['leaseId', 'args'],
    },
    async execute(_id: string, params: any) {
      const leaseId = String(params?.leaseId || '');
      const active = await readLease(activeLeasePath);
      if (!active || active.leaseId !== leaseId) {
        return toolResult({ status: 'error', code: 'LEASE_NOT_ACTIVE', message: '该 Browser Lease 不属于当前活动会话。' });
      }
      try {
        const response = await callService(
          'POST',
          `/v1/browser/${encodeURIComponent(leaseId)}/execute`,
          {
            args: Array.isArray(params?.args) ? params.args.map(String) : [],
            timeoutMs: Number(params?.timeoutMs || 30_000),
          },
        );
        if (response.status === 'ok') {
          return {
            content: [{ type: 'text', text: response.output || '' }],
            details: { status: 'ok', leaseId },
          };
        }
        await rememberResponse(response, { url: active.targetUrl, origin: active.origin });
        return toolResult(response);
      } catch {
        return toolResult({ status: 'error', code: 'SERVICE_REQUEST_FAILED', message: '浏览器操作服务不可用。' });
      }
    },
  });

  api.on('tool_call', async (event: any) => {
    if (event?.toolName !== 'agent_browser') return;
    const pending = await readLease(pendingPath);
    if (pending) {
      return {
        block: true,
        reason: 'Browser Profile 正在等待用户登录；不要操作匿名浏览器，请结束当前回合。',
      };
    }
    const active = await readLease(activeLeasePath);
    if (active) {
      return {
        block: true,
        reason: `Browser Profile 由 Go 服务持有；请使用 codingto_browser_execute 和 leaseId ${active.leaseId}。`,
      };
    }
  });

  api.on('before_agent_start', async (event: any) => {
    const pending = await readLease(pendingPath);
    if (pending) {
      return {
        prompt: event?.message || '',
        include: [[
          '# Browser Profile 登录继续',
          '',
          '用户已在等待登录的流程中发来一条消息。',
          `立即调用 codingto_browser_prepare，参数 url 为 ${pending.targetUrl}，通过现有 lease 验证页面。`,
          '不要重新访问匿名浏览器，也不要再次询问用户选择 Profile。',
        ].join('\n')],
        exclude: [],
      };
    }
    const active = await readLease(activeLeasePath);
    if (!active) return event;
    return {
      prompt: event?.message || '',
      include: [[
        '# Browser Profile Lease 已就绪',
        '',
        `继续使用 codingto_browser_execute，leaseId 为 ${active.leaseId}。`,
        '目标页已打开；不要调用 agent_browser，也不要重复 open。',
      ].join('\n')],
      exclude: [],
    };
  });
}
