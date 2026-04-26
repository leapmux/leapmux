/**
 * Per-message UI state keys consumed via `getMessageUiState`/`setMessageUiState`
 * (or `useSharedExpandedState`). Centralized so renderers can't collide on a
 * hand-typed string and so adding a new flag has one obvious home.
 */
export const MESSAGE_UI_KEY = {
  TOOL_RESULT_EXPANDED: 'tool-result-expanded',
  TOOL_USE_LAYOUT: 'tool-use-layout',
  AGENT_PROMPT: 'agent-prompt',
  THINKING: 'thinking',
  PLAN_EXECUTION: 'plan-execution',
  CODEX_MCP_TOOL_CALL: 'codex-mcp-tool-call',
  CODEX_COMMAND_EXECUTION: 'codex-command-execution',
  CODEX_WEB_SEARCH: 'codex-web-search',
  CODEX_COLLAB_AGENT_TOOL_CALL: 'codex-collab-agent-tool-call',
  CODEX_REASONING: 'codex-reasoning',
  OPENCODE_TOOL_CALL_UPDATE: 'opencode-tool-call-update',
} as const

export type MessageUiKey = typeof MESSAGE_UI_KEY[keyof typeof MESSAGE_UI_KEY]
