// CodingTo managed dcg bridge.
// Upstream decision contract: https://github.com/Dicklesworthstone/destructive_command_guard/blob/main/docs/pi-integration.md
import { spawn } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';

const DCG_DIALOG_PREFIX = '__CODINGTO_DCG_CONFIRM__:';
const DCG_TIMEOUT_MS = 800;
const MAX_OUTPUT_BYTES = 256 * 1024;
const DCG_DISABLE_MARKER_FILE = 'dcg_disabled';

// sessionDcgDisabled reports whether the current conversation has DCG
// interception turned off at the conversation scope. The marker is written by
// the CodingTo backend into the session directory; it is re-read on every bash
// call so toggling the composer security menu takes effect immediately without
// restarting the agent process. Subagents inherit CODINGTO_DCG_DISABLE_MARKER
// pointing at the same file, so children follow the same conversation policy.
function sessionDcgDisabled(): boolean {
  const marker =
    text(process.env.CODINGTO_DCG_DISABLE_MARKER) ||
    (text(process.env.CODINGTO_SESSION_DIR)
      ? path.join(text(process.env.CODINGTO_SESSION_DIR), DCG_DISABLE_MARKER_FILE)
      : '');
  if (!marker) return false;
  try {
    return existsSync(marker) && readFileSync(marker, 'utf8').trim() === '1';
  } catch {
    return false;
  }
}

type DCGDecision = {
  deny: boolean;
  reason: string;
  ruleId: string;
  remediation: string;
};

function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function commandPreview(command: string): string {
  const limit = 2_000;
  return command.length <= limit ? command : `${command.slice(0, limit)}\n…（命令已截断）`;
}

function dcgDecision(binary: string, command: string): Promise<DCGDecision> {
  return new Promise((resolve) => {
    let settled = false;
    let stdout = '';
    const finish = (decision: DCGDecision) => {
      if (settled) return;
      settled = true;
      resolve(decision);
    };
    const child = spawn(binary, ['--robot', 'test', command], {
      shell: false,
      stdio: ['ignore', 'pipe', 'ignore'],
      windowsHide: true,
    });
    const timer = setTimeout(() => {
      child.kill();
      // dcg is a guardrail rather than the command executor. A broken or slow
      // installation fails open so it cannot freeze every shell operation.
      finish({ deny: false, reason: '', ruleId: '', remediation: '' });
    }, DCG_TIMEOUT_MS);
    timer.unref?.();

    child.stdout?.on('data', (chunk: Buffer) => {
      if (stdout.length >= MAX_OUTPUT_BYTES) return;
      stdout += chunk.toString().slice(0, MAX_OUTPUT_BYTES - stdout.length);
    });
    child.on('error', () => {
      clearTimeout(timer);
      finish({ deny: false, reason: '', ruleId: '', remediation: '' });
    });
    child.on('close', (code) => {
      clearTimeout(timer);
      if (code !== 1) {
        finish({ deny: false, reason: '', ruleId: '', remediation: '' });
        return;
      }
      let parsed: any = {};
      try { parsed = JSON.parse(stdout); } catch { /* retain safe defaults */ }
      finish({
        deny: true,
        reason: text(parsed?.reason) || text(parsed?.explanation) || 'dcg 将此命令识别为危险命令。',
        ruleId: text(parsed?.rule_id),
        remediation: text(parsed?.remediation) || text(parsed?.suggestion),
      });
    });
  });
}

export default function codingToDCGGuard(pi: any) {
  pi.on('tool_call', async (event: any, ctx?: any) => {
    if (event?.toolName !== 'bash') return;
    // 本次对话关闭了命令拦截时直接放行，不修改任何 Agent 配置。
    if (sessionDcgDisabled()) return;
    const binary = text(process.env.CODINGTO_DCG_BIN);
    const command = text(event?.input?.command);
    if (!binary || !command) return;

    const decision = await dcgDecision(binary, command);
    if (!decision.deny) return;

    const details = [
      `危险命令：\n${commandPreview(command)}`,
      `检测原因：${decision.reason}`,
      decision.ruleId ? `规则：${decision.ruleId}` : '',
      decision.remediation ? `建议：${decision.remediation}` : '',
      '',
      '是否同意执行此命令？',
    ].filter(Boolean).join('\n\n');
    const approved = await ctx?.ui?.confirm?.(
      `${DCG_DIALOG_PREFIX}危险命令需要授权`,
      details,
    );
    if (approved === true) return;
    return {
      block: true,
      reason: `危险命令已被拒绝：${decision.reason}${decision.ruleId ? ` [${decision.ruleId}]` : ''}`,
    };
  });
}
