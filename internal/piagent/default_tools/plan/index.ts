import type { ExtensionAPI } from '@earendil-works/pi-coding-agent';

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
      '在执行任何「会产生改动」的操作前，先向用户展示执行计划并等待确认。',
      '行为约束：在调用任何会修改状态的工具（文件编辑/写入、bash 命令、git 写入、接口写入等）之前，',
      '你必须先调用 codingto_plan_present 给出完整有序步骤，并等待用户确认（confirmed === true）。',
      '不要在聊天回复里用编号列表重复打印计划——工具会把它渲染到底部计划面板。',
      '仅在 confirmed === true 时继续逐步执行；若用户拒绝，应停下来询问修订后的需求。',
      '返回 { confirmed: boolean, plan: PlanStep[] }。',
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
      '某一步骤执行完成后，更新底部计划面板的完成状态。',
      '每完成（或重开）一个计划步骤都应调用本工具，使面板进度与实际情况一致。',
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
  // 每次请求（agent 运行）前自动注入行为约束：改动前必须出计划并经用户确认。
  // before_agent_start 在每轮用户输入触发 agent 前都会触发，因此提示词对每一次
  // 请求都生效，不依赖模型是否“主动”调用工具。
  // ---------------------------------------------------------------------------
  pi.on('before_agent_start', async () => {
    return {
      message: {
        customType: 'codingto-plan-policy',
        display: false,
        content: [
          '[计划策略 / Plan Policy]',
          '在执行任何会修改系统状态的操作之前，你必须先征求用户确认：',
          '- 涉及改动的操作包括但不限于：编辑或写入文件、执行 bash/终端命令、git 提交或推送、调用会产生副作用的接口等。',
          '- 执行前请先调用 codingto_plan_present 工具，把完整有序的执行计划展示到底部计划面板，并等待用户确认（confirmed === true）。',
          '- 切勿在未经确认的情况下直接执行改动；若 confirmed === false，表示用户取消了本次对话，必须立即停止，不得解释为继续，也不得再次展示计划。',
          '- 每完成一个计划步骤，调用 codingto_plan_update 将其标记为已完成（☑），保持底部计划面板进度与实际情况一致。',
        ].join('\n'),
      },
    };
  });
}
