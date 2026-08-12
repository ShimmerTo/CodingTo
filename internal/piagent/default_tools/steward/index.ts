import type { ExtensionAPI } from '@earendil-works/pi-coding-agent';

// =============================================================================
// Steward（CodingTo 管家）内置扩展
//
// 管家 Agent 专用工具集：所有操作通过本地 HTTP RPC 直达 CodingTo 管家服务
// （internal/steward），不暴露 Wails 边界给模型。
//
// 环境变量（由 CodingTo 在启动管家 Agent 时注入）：
//   CODINGTO_STEWARD_RPC_URL   http://127.0.0.1:<port>
//   CODINGTO_STEWARD_RPC_TOKEN <随机令牌>
// =============================================================================

type RPCResult = { ok: boolean; result?: unknown; error?: string };

async function rpc(tool: string, args: Record<string, unknown>): Promise<unknown> {
  const url = process.env.CODINGTO_STEWARD_RPC_URL;
  const token = process.env.CODINGTO_STEWARD_RPC_TOKEN;
  if (!url || !token) {
    throw new Error('Steward RPC 未配置；请启用管家后重启当前 Agent（缺少 CODINGTO_STEWARD_RPC_URL/TOKEN）。');
  }
  const resp = await fetch(`${url}/rpc`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    body: JSON.stringify({ tool, args }),
  });
  if (!resp.ok) {
    throw new Error(`Steward RPC 失败：HTTP ${resp.status}`);
  }
  const payload = (await resp.json()) as RPCResult;
  if (!payload.ok) {
    throw new Error(payload.error || 'Steward RPC 失败');
  }
  return payload.result;
}

function textResult(value: unknown): { content: { type: 'text'; text: string }[]; details: Record<string, unknown> } {
  // `value ?? null` guarantees JSON.stringify always yields a string: with
  // `undefined` it returns undefined, which drops the text field when the
  // object is serialized, producing `[{"type":"text"}]` tool results that
  // crash pi's token estimator (estimate.js `block.text.length`).
  return {
    content: [{ type: 'text', text: JSON.stringify(value ?? null) }],
    details: { data: value },
  };
}

export default function (pi: ExtensionAPI) {
  // 仅当管家 RPC 环境变量存在时才注册工具与行为约束。这两个环境变量只由
  // CodingTo 在启动驻留管家 Agent 会话时注入（internal/steward AgentEnvironment）；
  // 普通对话、同一 Agent 的手动对话以及子 Agent 运行都没有它们。若在未配置时仍
  // 注册工具，模型会误用 codingto_steward_*（如 codingto_steward_list_environments）
  // 触发「RPC 未配置」报错，甚至被管家行为约束劫持，放弃原本无关的真实任务。
  const rpcURL = process.env.CODINGTO_STEWARD_RPC_URL;
  const rpcToken = process.env.CODINGTO_STEWARD_RPC_TOKEN;
  if (!rpcURL || !rpcToken) {
    console.log('[codingto-steward] RPC 未配置（CODINGTO_STEWARD_RPC_URL/TOKEN），跳过管家工具注册与行为约束');
    return;
  }

  const register = (name: string, description: string, properties: Record<string, unknown>, required: string[]) => {
    pi.registerTool({
      name,
      description,
      parameters: { type: 'object', properties, required },
      async execute(_id: string, params: any) {
        const result = await rpc(name.replace(/^codingto_steward_/, 'steward_'), params || {});
        return textResult(result);
      },
    });
  };

  register('codingto_steward_reply', '回复机器人用户（管家所有对外回复都必须走此工具；审批通知传 permissionRequestId 发送精确审批卡，普通回复传 text）。', {
    text: { type: 'string', description: '要发送的回复文本' },
    permissionRequestId: { type: 'string', description: '待审批请求 ID；传入时发送对应审批卡，不要同时改写审批内容' },
  }, []);

  register('codingto_steward_list_environments', '查看所有环境列表。', {}, []);
  register('codingto_steward_create_environment', '创建（或更新同名）环境。', {
    name: { type: 'string', description: '环境名称' },
    path: { type: 'string', description: '本地目录路径' },
    description: { type: 'string', description: '描述（可选）' },
  }, ['name', 'path']);
  register('codingto_steward_remove_environment', '删除环境（破坏性，请先确认）。', {
    id: { type: 'string', description: '环境 ID 或名称' },
  }, ['id']);

  register('codingto_steward_start_task', '在指定环境下创建新对话并启动任务；调用成功后必须继续调用 codingto_steward_reply 告知用户会话已创建。', {
    envId: { type: 'string', description: '环境 ID（可选，留空用默认）' },
    title: { type: 'string', description: '对话标题（可选，默认取任务前 30 字）' },
    task: { type: 'string', description: '任务内容（会作为对话的第一条用户消息）' },
  }, ['task']);
  register('codingto_steward_stop_task', '结束一个进行中的对话。', {
    sessionId: { type: 'string', description: '会话 ID' },
  }, ['sessionId']);
  register('codingto_steward_list_running', '查看当前真正进行中的对话；结果包含实时累计时长、当前回合开始时间和会话日志中的最近活动。', {}, []);
  register('codingto_steward_list_sessions', '查看所有对话。', {}, []);
  register('codingto_steward_continue_task', '继续指定的已有对话；适合基于该对话结果追问、修改或执行下一步，不要为同一工作重复新建对话。', {
    sessionId: { type: 'string', description: '要继续的会话 ID' },
    task: { type: 'string', description: '发送给该会话的后续要求' },
  }, ['sessionId', 'task']);
  register('codingto_steward_list_permissions', '列出当前 IM 用户和线程中的全部待审批/选择/输入项。用户消息可能在回答待办时必须先调用。', {}, []);
  register('codingto_steward_answer_permissions', '根据用户真实意图实际回答一个或多个待办。调用成功才代表已批准、拒绝、选择或输入；禁止只在文字里声称已处理。', {
    answers: {
      type: 'array',
      description: '决定列表 [{requestId, answer}]；answer 使用批准/拒绝、选项值或用户输入原文',
      items: {
        type: 'object',
        properties: {
          requestId: { type: 'string', description: 'list_permissions 返回的准确请求 ID' },
          answer: { type: 'string', description: '对该请求的答案' },
        },
        required: ['requestId', 'answer'],
      },
    },
  }, ['answers']);
  register('codingto_steward_delete_session', '删除对话（破坏性，请先确认）。', {
    sessionId: { type: 'string', description: '会话 ID' },
  }, ['sessionId']);

  register('codingto_steward_ask_confirm', '向机器人用户发起确认或选择题，等待用户回复。', {
    title: { type: 'string', description: '确认标题' },
    body: { type: 'string', description: '说明' },
    options: { type: 'array', items: { type: 'object' }, description: '选项 [{label, value}]（可选，给则变为选择题）' },
  }, ['title', 'body']);

  // 注入管家行为约束。
  pi.on('before_agent_start', async () => {
    return {
      message: {
        customType: 'codingto-steward-policy',
        display: false,
        content: [
          '你是 CodingTo 的管家 Agent：',
          '- 每一轮都必须调用至少一个 codingto_steward_reply 工具输出总结。',
          '- 回复用户必须通过 codingto_steward_reply 工具；不要假设消息会自动送达。',
          '- 执行操作优先使用管家工具，包括环境、对话、任务和审批工具。',
          '- 用户消息可能是在回答计划审批、危险命令、选择题或输入框时，先调用 codingto_steward_list_permissions 获取当前上下文的准确待办，再根据自然语言含义调用 codingto_steward_answer_permissions；可以一次处理多项。只有工具成功后才能说已经批准、拒绝或选择。',
          '- 用户要求基于已有结果继续、修改或追问时，调用 codingto_steward_continue_task 继续原对话；要求创建、启动、派发独立新任务时调用 codingto_steward_start_task。工具返回后继续调用 codingto_steward_reply 告知会话 ID / 启动状态。',
          '- 不得自行替用户做审批决定；可以理解“都批准”“第二个拒绝”“按你建议的选项”等自然表达，意图仍不清楚时再澄清。',
          '- 破坏性操作（删除环境、删除对话）必须先调用 codingto_steward_ask_confirm 获得用户确认。',
          '- 模糊请求必须通过 codingto_steward_reply 提出澄清；无法执行也必须通过 codingto_steward_reply 说明原因。',
        ].join('\n'),
      },
    };
  });
}
