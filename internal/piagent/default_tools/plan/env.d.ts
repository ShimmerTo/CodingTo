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
      ) => Promise<{
        content: Array<{ type: string; text: string }>;
        details: any;
      }>;
    }): void;
    exec?(command: string, args: string[], options?: any): Promise<{
      code: number;
      stdout: string;
      stderr: string;
      killed?: boolean;
    }>;
  }
  export type ToolCallEventType = any;
  export function isToolCallEventType(event: any): boolean;
}
