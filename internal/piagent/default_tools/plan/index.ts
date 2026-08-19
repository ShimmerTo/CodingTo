import type { ExtensionAPI } from '@earendil-works/pi-coding-agent';
import { readFile } from 'node:fs/promises';

const DEFAULT_PLAN_PROMPT = `# CodingTo Plan Policy

在执行任何会修改系统状态的操作之前，必须先征求用户确认。

- 涉及改动的操作包括但不限于：编辑或写入文件、执行会产生副作用的终端命令、git 写入，以及调用会改变外部状态的接口。
- 执行前先调用 codingto_plan_present，把完整有序的执行计划展示到计划面板，并等待 confirmed === true。
- 不要在聊天回复中重复输出编号计划；计划由工具统一渲染。
- confirmed === false 表示用户取消：立即停止，不继续执行，也不要自动再次展示计划。
- 每完成或重新打开一个步骤，调用 codingto_plan_update，使计划面板与实际进度一致。`;

async function planPrompt() {
  const file = String(process.env.CODINGTO_PLAN_PROMPT_PATH || '').trim();
  if (!file) return DEFAULT_PLAN_PROMPT;
  try { return (await readFile(file, 'utf8')).trim(); } catch { return DEFAULT_PLAN_PROMPT; }
}

// =============================================================================
// Plan Mode（CodingTo 内置扩展）
//
// 设计目标：让 AI 在执行任何改动前，先通过底部「计划面板」展示计划并等待用户
// 确认，执行过程中逐步把步骤标记为已完成（☑）。完全复用 CodingTo 前端已有的
// plan-todos widget 通道与 extension_ui 确认弹窗，无需改动前端。
//
// ------------------------- 计划数据模型（JSON） -----------------------------
// 单个步骤 PlanStep：
//   { "index": number, "text": string, "completed": boolean }
//
// ------------------------- 输出计划事件格式 --------------------------------
// plan_present 渲染时调用：
//   ctx.ui.setWidget("plan-todos", lines)
// 其中 lines[i] = (completed ? "☑ " : "☐ ") + text
// 该行的形状正是前端 updatePlanFromWidget() 期望的：
//   - 前缀 "☑" / "✓" 或包裹 ~~删除线~~ 表示已完成
//   - 其它（"☐" / "○" 或无前缀）表示待办
//
// ------------------------- 更新计划事件格式 --------------------------------
// plan_update 修改某步状态后，同样重新调用：
//   ctx.ui.setWidget("plan-todos", newLines)
// 仅翻转对应步骤的勾选框，其余保持不变。
// =============================================================================

interface PlanStep {
  index: number;
  text: string;
  completed: boolean;
}

// 模块级状态：在单个 Pi 进程（即单个会话）内保持，跨工具调用共享。
let plan: PlanStep[] = [];
// Host protocol marker: CodingTo strips this prefix before rendering and uses
// it to distinguish plan rejection from unrelated extension confirmations.
const PLAN_CONFIRM_DIALOG_PREFIX = '__CODINGTO_PLAN_CONFIRM__:';
// confirmed 标记计划是否已通过用户确认。
// 确认前：渲染到底部计划面板；确认后：隐藏底部面板，改在对话框上方显示执行条。
let confirmed = false;

function renderWidget(ctx: any) {
  if (!ctx?.ui) return;
  if (!plan.length) {
    ctx.ui.setWidget('plan-todos', undefined);
    ctx.ui.setWidget('plan-execution', undefined);
    return;
  }
  const lines = plan.map((s) => `${s.completed ? '☑ ' : '☐ '}${s.text}`);
  if (confirmed) {
    // 执行阶段：隐藏底部确认面板，改在对话框上方显示执行条
    ctx.ui.setWidget('plan-todos', undefined);
    ctx.ui.setWidget('plan-execution', lines);
  } else {
    // 确认阶段：渲染到底部计划面板
    ctx.ui.setWidget('plan-todos', lines);
  }
}

export default function (pi: ExtensionAPI) {
  // ---------------------------------------------------------------------------
  // codingto_plan_present —— 输出计划并请求用户确认
  // ---------------------------------------------------------------------------
  pi.registerTool({
    name: 'codingto_plan_present',
    description: [
      '展示有序计划并等待用户确认。',
      '返回 { confirmed: boolean, plan: PlanStep[] }；confirmed === false 时还会返回 cancelled: true。',
    ].join(' '),
    parameters: {
      type: 'object',
      properties: {
        title: { type: 'string', description: '简短计划标题，例如「实现登录接口」' },
        steps: {
          type: 'array',
          description: '有序执行步骤列表；index 从 1 开始连续编号',
          items: {
            type: 'object',
            properties: {
              index: { type: 'number', description: '步骤序号，从 1 开始' },
              text: { type: 'string', description: '步骤描述' },
            },
            required: ['index', 'text'],
          },
        },
        confirm_prompt: { type: 'string', description: '确认弹窗标题，默认「确认执行以上计划？」' },
      },
      required: ['steps'],
    },
    async execute(_id: string, params: any, _signal?: any, onUpdate?: (u: any) => void, ctx?: any) {
      if (!Array.isArray(params.steps) || params.steps.length === 0) {
        return {
          content: [{ type: 'text', text: JSON.stringify({ ok: false, error: 'steps 必须是非空数组' }) }],
          details: {},
        };
      }

      confirmed = false;
      plan = params.steps.map((s: any, i: number) => ({
        index: Number(s.index ?? i + 1),
        text: String(s.text ?? '').trim(),
        completed: false,
      }));

      renderWidget(ctx);
      onUpdate?.({
        content: [{ type: 'text', text: `已展示计划（${plan.length} 步），等待用户确认` }],
        details: { status: 'awaiting_confirm', plan },
      });

      const title = `${PLAN_CONFIRM_DIALOG_PREFIX}${params.confirm_prompt || '确认执行以上计划？'}`;
      // 消息尾部内嵌结构化计划步骤：setWidget('plan-todos') 与确认弹窗是两条独立
      // UI 事件，前端收到弹窗时计划 Widget 可能尚未渲染（事件队列乱序）。内嵌步骤
      // 让确认弹窗自带完整计划，前端可直接解析渲染，彻底消除该竞态。
      const message = [
        `${params.title ? `计划：${params.title}\n` : ''}共 ${plan.length} 步，请于底部计划面板核对后确认。`,
        `__CODINGTO_PLAN_STEPS__${JSON.stringify(plan.map((s) => ({ index: s.index, text: s.text, completed: s.completed })))}`,
      ].join('');
      const userConfirmed = await ctx?.ui?.confirm(title, message);

      if (!userConfirmed) {
        const cancelledPlan = plan;
        plan = [];
        renderWidget(ctx);
        ctx?.ui?.notify('计划未确认，已暂停执行', 'warning');
        return {
          content: [{
            type: 'text',
            text: JSON.stringify({
              ok: true,
              confirmed: false,
              cancelled: true,
              instruction: '用户已取消本次对话。立即停止，不要继续执行，也不要再次展示计划。',
              plan: cancelledPlan,
            }),
          }],
          details: { confirmed: false, cancelled: true, plan: cancelledPlan },
        };
      }

      confirmed = true;
      renderWidget(ctx);
      return {
        content: [{ type: 'text', text: JSON.stringify({ ok: true, confirmed: true, plan }) }],
        details: { confirmed: true, plan },
      };
    },
  });

  // ---------------------------------------------------------------------------
  // codingto_plan_update —— 更新计划完成状态
  // ---------------------------------------------------------------------------
  pi.registerTool({
    name: 'codingto_plan_update',
    description: [
      '更新已有计划中一个步骤的完成状态。',
      'status 传 "done" 标记该步为已完成（☑），传 "pending" 重新置为待办（☐）。',
    ].join(' '),
    parameters: {
      type: 'object',
      properties: {
        index: { type: 'number', description: '要更新的步骤序号，需与 codingto_plan_present 给出的 index 一致' },
        status: { type: 'string', description: '"done" | "pending"' },
      },
      required: ['index', 'status'],
    },
    async execute(_id: string, params: any, _signal?: any, onUpdate?: (u: any) => void, ctx?: any) {
      const idx = Number(params.index);
      const step = plan.find((s) => s.index === idx);
      if (!step) {
        return {
          content: [{ type: 'text', text: JSON.stringify({ ok: false, error: `未找到序号 ${idx} 的计划步骤，请先调用 codingto_plan_present` }) }],
          details: {},
        };
      }

      step.completed = String(params.status) === 'done';
      renderWidget(ctx);
      onUpdate?.({
        content: [{ type: 'text', text: `计划步骤 ${step.index} 已标记为 ${step.completed ? '已完成' : '待办'}` }],
        details: { updated: step, plan },
      });

      return {
        content: [{ type: 'text', text: JSON.stringify({ ok: true, updated: { index: step.index, text: step.text, completed: step.completed } }) }],
        details: { plan },
      };
    },
  });

  // ---------------------------------------------------------------------------
  // 每次请求前读取全局行为提示词；覆盖文件不存在时使用内置默认值。
  // 工具参数和返回格式仍固定在上面的 schema / execute 中，用户配置不会破坏协议。
  // ---------------------------------------------------------------------------
  pi.on('before_agent_start', async () => {
	const prompt = await planPrompt();
	if (!prompt) return;
    return {
      message: {
        customType: 'codingto-plan-policy',
        display: false,
        content: prompt,
      },
    };
  });
}
