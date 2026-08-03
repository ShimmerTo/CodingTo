declare module '@earendil-works/pi-coding-agent' {
  export type SubagentAction = 'list' | 'run' | 'status';

  export interface SubagentParams {
    action: SubagentAction;
    key?: string;
    task?: string;
    runId?: string;
  }

  export interface SubagentUpdate {
    content?: Array<{ type: string; text: string }>;
    details?: unknown;
    [key: string]: unknown;
  }

  export interface RunFile {
    path: string;
    change: string;
    kind?: string;
    bytes?: number;
  }

  export interface RunResult {
    runId: string;
    agentKey: string;
    parentNodeId: string;
    status: 'running' | 'completed' | 'failed' | 'aborted' | 'timeout' | string;
    files: RunFile[];
    text?: string;
    error?: string;
    transcript: string;
    [key: string]: unknown;
  }

  export interface BridgeNotificationEvent {
    type?: string;
    runId?: string;
    agentKey?: string;
    [key: string]: unknown;
  }

  export interface DetachedSubagentPayload {
    kind: 'subagent_event';
    runId: string;
    agentKey: string;
    parentNodeId: string;
    toolCallId: string;
    status: string;
    detached: true;
    event?: BridgeNotificationEvent;
  }

  export interface ExtensionAPI {
    registerTool(config: {
      name: string;
      description: string;
      promptSnippet?: string;
      promptGuidelines?: string[];
      parameters: unknown;
      execute: (
        toolCallId: string,
        params: SubagentParams,
        signal?: AbortSignal,
        onUpdate?: (update: SubagentUpdate) => void,
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
    appendEntry(customType: string, data?: DetachedSubagentPayload): void;
  }
}

declare namespace NodeJS {
  interface ProcessEnv {
    CODINGTO_SUBAGENT_KEYS?: string;
    CODINGTO_SUBAGENT_BRIDGE_BIN?: string;
    CODINGTO_SUBAGENT_CONFIG?: string;
    CODINGTO_SUBAGENT_START_TIMEOUT_MS?: string;
    CODINGTO_SUBAGENT_IDLE_TIMEOUT_MS?: string;
    CODINGTO_SESSION_DIR?: string;
    CODINGTO_WORK_DIR?: string;
  }
}
