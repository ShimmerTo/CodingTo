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
    CODINGTO_DB_BRIDGE_BIN?: string;
    CODINGTO_DB_CONFIG_PATH?: string;
    CODINGTO_SESSION_DIR?: string;
    CODINGTO_WORK_DIR?: string;
  }
}
