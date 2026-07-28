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
    on(event: 'before_agent_start', handler: (event: any, ctx?: any) => Promise<any> | any): void;
    on(event: 'tool_call', handler: (event: any, ctx?: any) => Promise<any> | any): void;
  }
}

declare const process: {
  env: Record<string, string | undefined>;
  cwd(): string;
  platform: string;
};
declare const Buffer: { byteLength(value: string): number };
declare const fetch: (url: string, options?: any) => Promise<{
  json(): Promise<any>;
  status: number;
}>;
declare const AbortSignal: { timeout(milliseconds: number): any };

declare module 'node:fs/promises' {
  export function mkdir(path: string, options?: any): Promise<any>;
  export function readFile(path: string, encoding: string): Promise<string>;
  export function readdir(path: string): Promise<string[]>;
  export function realpath(path: string): Promise<string>;
  export function rm(path: string, options?: any): Promise<void>;
  export function stat(path: string): Promise<{ isFile(): boolean; size: number }>;
  export function writeFile(path: string, data: string, options?: any): Promise<void>;
}

declare module 'node:path' {
  const path: {
    sep: string;
    resolve(...paths: string[]): string;
    join(...paths: string[]): string;
    dirname(path: string): string;
    basename(path: string): string;
    extname(path: string): string;
  };
  export default path;
}
