declare module '@earendil-works/pi-coding-agent' {
  export interface ExtensionAPI {
    registerTool(config: {
      name: string;
      description: string;
      promptSnippet?: string;
      promptGuidelines?: string[];
      parameters: unknown;
      execute: (
        toolCallId: string,
        params: Record<string, unknown>,
        signal?: AbortSignal,
        onUpdate?: (update: { content?: Array<{ type: string; text: string }>; details?: unknown }) => void,
        ctx?: unknown,
      ) => Promise<unknown>;
    }): void;
    sendMessage(message: {
      customType: string;
      content: string;
      display: boolean;
      details?: unknown;
    }, options?: {
      triggerTurn?: boolean;
      deliverAs?: 'steer' | 'followUp' | 'nextTurn';
    }): void;
    on(event: string, handler: (event?: unknown) => unknown): void;
  }
}

declare namespace NodeJS {
  interface ProcessEnv {
    CODINGTO_STEWARD_RPC_URL?: string;
    CODINGTO_STEWARD_RPC_TOKEN?: string;
  }
}
