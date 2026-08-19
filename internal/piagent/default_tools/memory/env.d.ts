declare module '@earendil-works/pi-coding-agent' {
  export interface ExtensionAPI {
    registerTool(config: {
      name: string;
      description: string;
      parameters: any;
      execute: (toolCallId: string, params: any, signal?: any, onUpdate?: any, ctx?: any) => Promise<{ content: Array<{ type: string; text: string }>; details: any }>;
    }): void;
    on(event: 'before_agent_start', handler: (event: any, ctx?: any) => Promise<any> | any): void;
  }
}

declare const process: {
  env: Record<string, string | undefined>;
  cwd(): string;
};

declare const Buffer: { byteLength(value: string): number };

declare module 'node:fs/promises' {
  export function mkdir(path: string, options?: any): Promise<void>;
  export function readFile(path: string, encoding: string): Promise<string>;
  export function readdir(path: string, options?: any): Promise<any[]>;
  export function rm(path: string, options?: any): Promise<void>;
  export function writeFile(path: string, data: string, options?: any): Promise<void>;
}

declare module 'node:path' {
  const path: {
    resolve(...paths: string[]): string;
    join(...paths: string[]): string;
    dirname(path: string): string;
  };
  export default path;
}
