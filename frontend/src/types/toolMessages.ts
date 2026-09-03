/** Typed interfaces for known tool inputs from Claude Code agent messages. */

export interface BashInput {
  command?: string
  description?: string
  timeout?: number
  run_in_background?: boolean
}

export interface ReadInput {
  file_path?: string
  offset?: number
  limit?: number
  pages?: string
}

export interface WriteInput {
  file_path?: string
  content?: string
}

export interface EditInput {
  file_path?: string
  old_string?: string
  new_string?: string
  replace_all?: boolean
}

export interface GrepInput {
  pattern?: string
  path?: string
  glob?: string
  type?: string
  output_mode?: string
  head_limit?: number
}

export interface GlobInput {
  pattern?: string
  path?: string
}

export interface WebFetchInput {
  url?: string
  prompt?: string
}

export interface WebSearchInput {
  query?: string
}

export interface ToolSearchInput {
  query?: string
  max_results?: number
}

export interface TaskStopInput {
  task_id?: string
}

/**
 * SendMessage addresses another agent. `to` is the recipient: for a subagent of
 * this session it is the background-task row key (Claude keys its task registry
 * by agent id), and otherwise a display name, another session, or a
 * uds:/bridge:/did: address.
 *
 * `message` is a plain string for an ordinary message and an object for the
 * structured kinds (shutdown_request, plan_approval_response, ...).
 */
export interface SendMessageInput {
  to?: string
  message?: string | Record<string, unknown>
  summary?: string
}

export interface RemoteTriggerInput {
  action?: 'list' | 'get' | 'create' | 'update' | 'run'
  trigger_id?: string
  body?: Record<string, unknown>
}

/**
 * Canonical Claude tool name literals. Use these constants instead of bare
 * string literals when dispatching on tool name — typos become compile errors
 * and renaming touches one place.
 */
export const CLAUDE_TOOL = {
  BASH: 'Bash',
  READ: 'Read',
  WRITE: 'Write',
  EDIT: 'Edit',
  GREP: 'Grep',
  GLOB: 'Glob',
  TASK: 'Task',
  AGENT: 'Agent',
  WEB_FETCH: 'WebFetch',
  WEB_SEARCH: 'WebSearch',
  TODO_WRITE: 'TodoWrite',
  TASK_CREATE: 'TaskCreate',
  TASK_UPDATE: 'TaskUpdate',
  TASK_GET: 'TaskGet',
  TASK_LIST: 'TaskList',
  TASK_OUTPUT: 'TaskOutput',
  TASK_STOP: 'TaskStop',
  TOOL_SEARCH: 'ToolSearch',
  ASK_USER_QUESTION: 'AskUserQuestion',
  ENTER_PLAN_MODE: 'EnterPlanMode',
  EXIT_PLAN_MODE: 'ExitPlanMode',
  SKILL: 'Skill',
  REMOTE_TRIGGER: 'RemoteTrigger',
  SEND_MESSAGE: 'SendMessage',
  LIST_AGENTS: 'ListAgents',
} as const

export type ClaudeToolName = typeof CLAUDE_TOOL[keyof typeof CLAUDE_TOOL]

/**
 * Canonical ACP `sessionUpdate` literals (the discriminator used by the Agent
 * Client Protocol on incoming updates). Use these constants in classifiers and
 * routers so wire-format strings are typo-checked and centralized.
 */
export const ACP_SESSION_UPDATE = {
  AGENT_MESSAGE_CHUNK: 'agent_message_chunk',
  AGENT_THOUGHT_CHUNK: 'agent_thought_chunk',
  TOOL_CALL: 'tool_call',
  TOOL_CALL_UPDATE: 'tool_call_update',
  PLAN: 'plan',
  USAGE_UPDATE: 'usage_update',
  AVAILABLE_COMMANDS_UPDATE: 'available_commands_update',
  USER_MESSAGE_CHUNK: 'user_message_chunk',
  CONFIG_OPTION_UPDATE: 'config_option_update',
} as const

export type AcpSessionUpdate = typeof ACP_SESSION_UPDATE[keyof typeof ACP_SESSION_UPDATE]

/**
 * Canonical ACP tool-call `kind` literals. ACP groups all agent tools into a
 * small set of behavioral kinds; renderers and extractors switch on these.
 */
export const ACP_TOOL_KIND = {
  EXECUTE: 'execute',
  EDIT: 'edit',
  WRITE: 'write',
  READ: 'read',
  SEARCH: 'search',
  FETCH: 'fetch',
  THINK: 'think',
} as const

export type AcpToolKind = typeof ACP_TOOL_KIND[keyof typeof ACP_TOOL_KIND]

/**
 * Canonical Codex `item.type` literals. Codex emits structured items inside
 * `item/completed` notifications; classifiers and renderers dispatch on these
 * type strings, so they're centralized here to typo-check call sites.
 */
export const CODEX_ITEM = {
  AGENT_MESSAGE: 'agentMessage',
  COMMAND_EXECUTION: 'commandExecution',
  FILE_CHANGE: 'fileChange',
  MCP_TOOL_CALL: 'mcpToolCall',
  DYNAMIC_TOOL_CALL: 'dynamicToolCall',
  COLLAB_AGENT_TOOL_CALL: 'collabAgentToolCall',
  WEB_SEARCH: 'webSearch',
  /**
   * The `image_gen` tool's result. `result` is a base64 PNG -- Codex builds
   * `data:image/png;base64,{result}` from it for the model, so the format is
   * not negotiable per call.
   */
  IMAGE_GENERATION: 'imageGeneration',
  /** The `view_image` tool. Carries the file's `path` and no pixels. */
  IMAGE_VIEW: 'imageView',
  REASONING: 'reasoning',
  PLAN: 'plan',
  USER_MESSAGE: 'userMessage',
  CONTEXT_COMPACTION: 'contextCompaction',
} as const

export type CodexItemType = typeof CODEX_ITEM[keyof typeof CODEX_ITEM]

/**
 * Canonical Codex `status` literals. Codex emits `status` on tool-call items
 * (`commandExecution`, `fileChange`, `mcpToolCall`, `collabAgentToolCall`);
 * classifiers and renderers branch on these strings, centralized here so
 * call sites can reference them by name and TypeScript catches typos.
 */
export const CODEX_STATUS = {
  COMPLETED: 'completed',
  FAILED: 'failed',
  IN_PROGRESS: 'inProgress',
} as const

export type CodexStatus = typeof CODEX_STATUS[keyof typeof CODEX_STATUS]

/**
 * Codex tool/category labels that don't ride on `item.type`. `TURN_PLAN`
 * dispatches off `parent.method === 'turn/plan/updated'` rather than an
 * `item.type`, so it lives here next to {@link CODEX_ITEM} so dispatch
 * tables can reference both without a typo risk.
 */
export const CODEX_INTERNAL_TOOL = {
  TURN_PLAN: 'turnPlan',
} as const

export type CodexInternalTool = typeof CODEX_INTERNAL_TOOL[keyof typeof CODEX_INTERNAL_TOOL]

/**
 * Canonical Codex JSON-RPC method names (excluding `account/rateLimits/updated`,
 * which already lives in `lib/rateLimitUtils.ts` so `messageParser` can consume
 * it without pulling in chat-component code).
 */
export const CODEX_METHOD = {
  THREAD_STARTED: 'thread/started',
  TURN_STARTED: 'turn/started',
  THREAD_STATUS_CHANGED: 'thread/status/changed',
  THREAD_NAME_UPDATED: 'thread/name/updated',
  THREAD_SETTINGS_UPDATED: 'thread/settings/updated',
  TURN_PLAN_UPDATED: 'turn/plan/updated',
  THREAD_TOKEN_USAGE_UPDATED: 'thread/tokenUsage/updated',
  MCP_SERVER_STARTUP_STATUS_UPDATED: 'mcpServer/startupStatus/updated',
  SKILLS_CHANGED: 'skills/changed',
  REMOTE_CONTROL_STATUS_CHANGED: 'remoteControl/status/changed',
  HOOK_STARTED: 'hook/started',
  HOOK_COMPLETED: 'hook/completed',
} as const

export type CodexMethod = typeof CODEX_METHOD[keyof typeof CODEX_METHOD]
