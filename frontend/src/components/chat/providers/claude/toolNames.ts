/**
 * Claude's own tool-name vocabulary.
 *
 * A LEAF module: data alone, with no icon and no component import, so a test that
 * runs without a DOM can read the table. `toolKinds.ts` pairs these names with
 * their kinds and their icons, and pulls in `lucide-solid` to do it -- which fails
 * to load in the `node` environment two extractor suites run in.
 */

/**
 * CANONICAL Claude tool name literals. Use these constants instead of bare
 * string literals when dispatching on tool name -- typos become compile errors
 * and renaming touches one place.
 *
 * Canonical means: the name every table below the extractor keys on. The command
 * line interface ships several tools under two names at once, and
 * {@link CLAUDE_TOOL_ALIAS} maps each second name onto the entry here. No table
 * spells an alias, because a table that spelled one had to spell BOTH -- and the
 * result side used to spell only `Agent`, so a `Task`-named span drew a request
 * card and a generic result.
 */
export const CLAUDE_TOOL_NAMES = {
  BASH: 'Bash',
  POWERSHELL: 'PowerShell',
  READ: 'Read',
  WRITE: 'Write',
  EDIT: 'Edit',
  MULTI_EDIT: 'MultiEdit',
  NOTEBOOK_EDIT: 'NotebookEdit',
  GREP: 'Grep',
  GLOB: 'Glob',
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
  SEND_USER_MESSAGE: 'SendUserMessage',
  LIST_AGENTS: 'ListAgents',
  ENTER_WORKTREE: 'EnterWorktree',
  EXIT_WORKTREE: 'ExitWorktree',
  TEAM_CREATE: 'TeamCreate',
  TEAM_DELETE: 'TeamDelete',
  CRON_CREATE: 'CronCreate',
  CRON_DELETE: 'CronDelete',
  CRON_LIST: 'CronList',
  SLEEP: 'Sleep',
  STRUCTURED_OUTPUT: 'StructuredOutput',
  LIST_MCP_RESOURCES: 'ListMcpResourcesTool',
  READ_MCP_RESOURCE: 'ReadMcpResourceTool',
  READ_MCP_RESOURCE_DIR: 'ReadMcpResourceDirTool',
} as const

export type ClaudeToolName = typeof CLAUDE_TOOL_NAMES[keyof typeof CLAUDE_TOOL_NAMES]

/**
 * The second name each of these tools also answers to.
 *
 * The command line interface registers one tool under two names for
 * compatibility with an older release, and a transcript recorded then still
 * carries the old one. `canonicalClaudeToolName` folds each key into its value,
 * ONCE, at the extraction boundary.
 *
 * A Map rather than an object, for the reason `CLAUDE_TOOL_KINDS` gives: the name
 * this reads is an OPEN vocabulary, because an agent and a Model Context Protocol
 * server both choose their own tool names. A tool called `constructor` or
 * `toString` answers from `Object.prototype`, so a plain object would return a
 * function as the canonical name instead of reporting that it holds no entry.
 */
export const CLAUDE_TOOL_ALIAS: ReadonlyMap<string, ClaudeToolName> = new Map<string, ClaudeToolName>([
  ['Task', CLAUDE_TOOL_NAMES.AGENT],
  ['BashOutput', CLAUDE_TOOL_NAMES.TASK_OUTPUT],
  ['AgentOutput', CLAUDE_TOOL_NAMES.TASK_OUTPUT],
  ['KillShell', CLAUDE_TOOL_NAMES.TASK_STOP],
  ['KillBash', CLAUDE_TOOL_NAMES.TASK_STOP],
  ['ListPeers', CLAUDE_TOOL_NAMES.LIST_AGENTS],
  ['Brief', CLAUDE_TOOL_NAMES.SEND_USER_MESSAGE],
])
