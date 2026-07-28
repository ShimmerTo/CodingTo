declare module '@earendil-works/pi-coding-agent' {
  export interface ExtensionAPI {
    registerTool(config: {
      name: string;
      description: string;
      parameters: any;
      execute: (
        toolCallId: string,
        params: any,
        signal?: AbortSignal,
        onUpdate?: any,
        ctx?: any,
      ) => Promise<any>;
    }): void;
  }
}

declare namespace NodeJS {
  interface ProcessEnv {
    CODINGTO_SUBAGENT_KEYS?: string;
    CODINGTO_SUBAGENT_BRIDGE_BIN?: string;
    CODINGTO_SUBAGENT_CONFIG?: string;
    CODINGTO_SESSION_DIR?: string;
    CODINGTO_WORK_DIR?: string;
  }
}
