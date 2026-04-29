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

export interface TaskInput {
  description?: string
  prompt?: string
  subagent_type?: string
}

export interface AgentInput {
  description?: string
  prompt?: string
  subagent_type?: string
}

export interface WebFetchInput {
  url?: string
  prompt?: string
}

export interface WebSearchInput {
  query?: string
}

export interface TodoWriteInput {
  todos?: Array<{
    content?: string
    status?: string
    activeForm?: string
  }>
}

export interface TaskOutputInput {
  task_id?: string
  block?: boolean
  timeout?: number
}

export interface ToolSearchInput {
  query?: string
  max_results?: number
}

export interface TaskStopInput {
  task_id?: string
}

export interface AskUserQuestionInput {
  questions?: Array<{
    question?: string
    header?: string
    options?: Array<{
      label?: string
      description?: string
    }>
    multiSelect?: boolean
  }>
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
  TASK_OUTPUT: 'TaskOutput',
  TASK_STOP: 'TaskStop',
  TOOL_SEARCH: 'ToolSearch',
  ASK_USER_QUESTION: 'AskUserQuestion',
  ENTER_PLAN_MODE: 'EnterPlanMode',
  EXIT_PLAN_MODE: 'ExitPlanMode',
  SKILL: 'Skill',
} as const

export type ClaudeToolName = typeof CLAUDE_TOOL[keyof typeof CLAUDE_TOOL]

/** Discriminated union of all known tool input types keyed by tool name. */
export type KnownToolInput
  = | { toolName: typeof CLAUDE_TOOL.BASH, input: BashInput }
    | { toolName: typeof CLAUDE_TOOL.READ, input: ReadInput }
    | { toolName: typeof CLAUDE_TOOL.WRITE, input: WriteInput }
    | { toolName: typeof CLAUDE_TOOL.EDIT, input: EditInput }
    | { toolName: typeof CLAUDE_TOOL.GREP, input: GrepInput }
    | { toolName: typeof CLAUDE_TOOL.GLOB, input: GlobInput }
    | { toolName: typeof CLAUDE_TOOL.TASK, input: TaskInput }
    | { toolName: typeof CLAUDE_TOOL.AGENT, input: AgentInput }
    | { toolName: typeof CLAUDE_TOOL.WEB_FETCH, input: WebFetchInput }
    | { toolName: typeof CLAUDE_TOOL.WEB_SEARCH, input: WebSearchInput }
    | { toolName: typeof CLAUDE_TOOL.TODO_WRITE, input: TodoWriteInput }
    | { toolName: typeof CLAUDE_TOOL.TASK_OUTPUT, input: TaskOutputInput }
    | { toolName: typeof CLAUDE_TOOL.TOOL_SEARCH, input: ToolSearchInput }
    | { toolName: typeof CLAUDE_TOOL.TASK_STOP, input: TaskStopInput }
    | { toolName: typeof CLAUDE_TOOL.ASK_USER_QUESTION, input: AskUserQuestionInput }

/** All known tool names (subset of ClaudeToolName that have typed inputs). */
export type KnownToolName = KnownToolInput['toolName']

const KNOWN_TOOLS = new Set<string>([
  CLAUDE_TOOL.BASH,
  CLAUDE_TOOL.READ,
  CLAUDE_TOOL.WRITE,
  CLAUDE_TOOL.EDIT,
  CLAUDE_TOOL.GREP,
  CLAUDE_TOOL.GLOB,
  CLAUDE_TOOL.TASK,
  CLAUDE_TOOL.AGENT,
  CLAUDE_TOOL.WEB_FETCH,
  CLAUDE_TOOL.WEB_SEARCH,
  CLAUDE_TOOL.TASK_OUTPUT,
  CLAUDE_TOOL.TODO_WRITE,
  CLAUDE_TOOL.TOOL_SEARCH,
  CLAUDE_TOOL.TASK_STOP,
  CLAUDE_TOOL.ASK_USER_QUESTION,
])

/** Type guard: returns true if the tool name is a known tool. */
export function isKnownTool(name: string): name is KnownToolName {
  return KNOWN_TOOLS.has(name)
}

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
  TURN_PLAN_UPDATED: 'turn/plan/updated',
  THREAD_TOKEN_USAGE_UPDATED: 'thread/tokenUsage/updated',
  MCP_SERVER_STARTUP_STATUS_UPDATED: 'mcpServer/startupStatus/updated',
} as const

export type CodexMethod = typeof CODEX_METHOD[keyof typeof CODEX_METHOD]
