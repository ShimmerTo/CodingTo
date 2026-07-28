# Dtool 内置工具创建指南

Pi Agent 通过 **extensions（扩展）** 机制支持自定义工具。每个工具是一个 TypeScript 文件，使用 `pi.registerTool()` 注册。

## 目录规范

在 `internal/pkg/p_piagent/default_tools/` 下，**每个子目录 = 一个内置工具**：

```
internal/pkg/p_piagent/default_tools/
├── create.md                    ← 本文件
└── <tool_dir>/                  ← 工具目录名（作为唯一标识）
    ├── meta.json                ← 工具元数据
    ├── index.ts                 ← TypeScript 实现脚本（会被物化到 Pi 运行时）
    ├── env.d.ts                ← （推荐）编辑器类型声明，消除 PCA 导入报红
    └── tsconfig.json           ← （推荐）独立 TS 项目配置，纳入声明文件
```

> `env.d.ts` / `tsconfig.json` 仅供编辑器静态检查，**不会**写进 Pi 运行时（运行时只物化 `index.ts`）。不加这两个文件，工具照样能跑，只是 VS Code 会报红。

## meta.json 字段说明

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `name` | string | ✅ | 工具显示名称（中文友好） |
| `tool_name` | string | ✅ | Pi `registerTool` 中的 `name`，函数唯一标识 |
| `description` | string | ✅ | 工具简要描述 |
| `tool_description` | string | ✅ | 发送给 LLM 的功能描述，影响 AI 调用准确度 |
| `parameters` | array | 可选 | 工具参数定义 |

### 版本号规则

内置工具统一使用 SemVer（`主版本.次版本.修订版本`）：

- 兼容性修复、提示词调整、内部实现优化：递增修订版本，例如 `5.0.0 → 5.0.1`；
- 向后兼容的新能力：递增次版本，例如 `5.0.1 → 5.1.0`；
- 只有工具名称、参数或返回结构发生不兼容变化时才递增主版本。

禁止把每次普通改动都直接升级为下一个 `N.0.0`。

### parameters 数组元素

| 字段 | 类型 | 说明 |
|------|------|------|
| `name` | string | 参数名 |
| `type` | string | 参数类型：`string` / `number` / `boolean` |
| `description` | string | 参数描述 |
| `required` | bool | 是否必填 |

## TypeScript 脚本模板

> **不要** `import { Type } from 'typebox'`。`typebox` 是 PCA 主包的**内部嵌套依赖**，不会暴露给扩展模块：既会让编辑器报红，运行时也可能解析失败。请直接用 **JSON Schema 字面量** 定义 `parameters`——`registerTool` 接收的本质就是 JSON Schema（`Type.Object` 生成的也是它）。

```typescript
import type { ExtensionAPI } from '@earendil-works/pi-coding-agent';

export default function (pi: ExtensionAPI) {
  pi.registerTool({
    name: '<tool_name>',      // 与 meta.json 中的 tool_name 一致
    description: '<发送给 LLM 的描述>',
    parameters: {
      type: 'object',
      properties: {
        param1: { type: 'string', description: '参数1描述（必填）' },
        param2: { type: 'number', description: '参数2描述（可选）' },
        param3: {
          type: 'array',
          description: '数组参数示例',
          items: { type: 'string' },
        },
      },
      // 只列真正必填的字段；有默认值/仅特定 action 使用的字段不要放进 required，
      // 否则模型调用时会被迫传一堆空值，降低调用准确度。
      required: ['param1'],
    },
    async execute(toolCallId, params, signal, onUpdate, ctx) {
      // === 在这里编写工具执行逻辑 ===
      // 返回值格式：
      return {
        content: [{ type: 'text', text: '返回给 LLM 的文本内容' }],
        details: {},
      };
    },
  });
}
```

## 消除编辑器报红（env.d.ts + tsconfig.json）

工具目录没有 `node_modules`，`import type { ExtensionAPI }` 会让 VS Code 报红。补两个文件即可（纯编辑器侧，不影响 Pi 运行时）：

**env.d.ts**：

```typescript
// 编辑器类型声明（仅供 VS Code / tsc 静态检查，不影响 Pi 运行时）。
// Pi 加载扩展时会注入 @earendil-works/pi-coding-agent，运行时无需本文件。
declare module '@earendil-works/pi-coding-agent' {
  export interface ExtensionAPI {
    registerTool(config: {
      name: string;
      description: string;
      parameters: any;
      execute: (
        toolCallId: string,
        params: any,
        signal?: any,
        onUpdate?: any,
        ctx?: any,
      ) => Promise<{ content: Array<{ type: string; text: string }>; details: any }>;
    }): void;
    exec?(command: string, args: string[], options?: any): Promise<{
      code: number; stdout: string; stderr: string; killed?: boolean;
    }>;
  }
}
```

**tsconfig.json**：

```json
{
  "compilerOptions": {
    "target": "ESNext",
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "strict": false,
    "skipLibCheck": true,
    "noEmit": true,
    "types": []
  },
  "include": ["*.ts"]
}
```

> 改完仍报红时，在 VS Code 执行 `TypeScript: Restart TS Server` 刷新即可。

## 注意事项

1. **Node.js 内置模块可直接使用**（如 `fs`, `path`, `child_process`）。
2. **尽量避免引入额外 npm 依赖**；若确实需要使用，在工具目录下添加 `package.json` 并执行 `npm install`。注意 `typebox` 不可用（它是 PCA 的内部依赖，不要在扩展里 import）。
3. **错误处理**: `execute` 抛出错误会被标记为 `isError: true`，不要返回错误字符串。
4. **工作目录**: `ctx` 对象中包含当前工作空间信息。
5. **输出限制**: 建议返回内容不超过 50KB / 2000 行，避免超出上下文窗口。

## 完整示例

以下是一个"读取文件内容"工具的完整实现：

**目录**: `internal/pkg/p_piagent/default_tools/read_file/`

**meta.json**:

```json
{
  "name": "读取文件",
  "tool_name": "dtool_read_file",
  "description": "读取指定文件的内容并返回",
  "tool_description": "Read the content of a file at the given path. Returns the file content as text.",
  "parameters": [
    {
      "name": "filePath",
      "type": "string",
      "description": "文件的绝对或相对路径",
      "required": true
    },
    {
      "name": "maxLines",
      "type": "number",
      "description": "最大返回行数，默认 500",
      "required": false
    }
  ]
}
```

**index.ts**:

```typescript
import type { ExtensionAPI } from '@earendil-works/pi-coding-agent';
import * as fs from 'fs';
import * as path from 'path';

export default function (pi: ExtensionAPI) {
  pi.registerTool({
    name: 'dtool_read_file',
    description: 'Read the content of a file at the given path. Returns the file content as text.',
    parameters: {
      type: 'object',
      properties: {
        filePath: { type: 'string', description: '文件路径' },
        maxLines: { type: 'number', description: '最大返回行数，默认 500' },
      },
      required: ['filePath'],
    },
    async execute(toolCallId, params, signal, onUpdate, ctx) {
      const filePath = params.filePath;
      const maxLines = params.maxLines || 500;

      if (!fs.existsSync(filePath)) {
        throw new Error(`文件不存在: ${filePath}`);
      }

      const content = fs.readFileSync(filePath, 'utf-8');
      const lines = content.split('\n');
      const truncated = lines.slice(0, maxLines).join('\n');
      const suffix = lines.length > maxLines ? `\n\n...(共 ${lines.length} 行，已截断至 ${maxLines} 行)` : '';

      return {
        content: [{ type: 'text', text: truncated + suffix }],
        details: {
          path: filePath,
          totalLines: lines.length,
          size: content.length,
        },
      };
    },
  });
}
```

## 相关文档

- Pi Extensions 官方文档: https://pi-doc.com/docs/latest/extensions.html
