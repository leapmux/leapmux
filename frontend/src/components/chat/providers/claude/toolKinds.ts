import type { ToolIconHint } from '../../ir/toolCall'
import type { ToolKind } from '../../ir/toolKind'
import { CLAUDE_TOOL_ALIAS, CLAUDE_TOOL_NAMES } from './toolNames'

/**
 * The one name every table below this reads.
 *
 * The command line interface registers several tools under two names, and a
 * transcript recorded on an older release still carries the older one. Folding
 * them here -- ONCE, at the extraction boundary -- is what lets each table below
 * hold one entry for each tool. Both names used to be spelled at each site that
 * remembered to, and the result side remembered `Agent` alone: a `Task`-named
 * span therefore drew a subagent card on its request row and a generic text row
 * for its report.
 *
 * A name this does not know passes through unchanged, because an MCP call and a
 * tool from a release after this one are both valid and neither is an alias.
 */
export function canonicalClaudeToolName(name: string): string {
  return CLAUDE_TOOL_ALIAS.get(name) ?? name
}

/**
 * The shared tool kind each Claude tool declares.
 *
 * Claude reports a tool by NAME alone, so this table is where the name becomes
 * the closed kind that drives the icon, the label, the title and the input
 * summary. A name absent from the table takes the empty kind, which draws the
 * uncategorized row every provider gives an unknown tool.
 *
 * A Map rather than an object, for the reason Pi's table gives: a tool may be
 * called `constructor` or `toString`, and a plain object answers those two names
 * from `Object.prototype` instead of reporting that it holds no entry.
 */
const CLAUDE_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  [CLAUDE_TOOL_NAMES.BASH, 'execute'],
  [CLAUDE_TOOL_NAMES.POWERSHELL, 'execute'],
  [CLAUDE_TOOL_NAMES.READ, 'read'],
  [CLAUDE_TOOL_NAMES.WRITE, 'write'],
  [CLAUDE_TOOL_NAMES.EDIT, 'edit'],
  [CLAUDE_TOOL_NAMES.MULTI_EDIT, 'edit'],
  [CLAUDE_TOOL_NAMES.NOTEBOOK_EDIT, 'edit'],
  [CLAUDE_TOOL_NAMES.GREP, 'grep'],
  [CLAUDE_TOOL_NAMES.GLOB, 'glob'],
  [CLAUDE_TOOL_NAMES.AGENT, 'agent'],
  [CLAUDE_TOOL_NAMES.WEB_FETCH, 'fetch'],
  [CLAUDE_TOOL_NAMES.WEB_SEARCH, 'web_search'],
  [CLAUDE_TOOL_NAMES.TODO_WRITE, 'todo'],
  [CLAUDE_TOOL_NAMES.TASK_CREATE, 'todo'],
  [CLAUDE_TOOL_NAMES.TASK_UPDATE, 'todo'],
  [CLAUDE_TOOL_NAMES.TASK_GET, 'todo'],
  [CLAUDE_TOOL_NAMES.TASK_LIST, 'todo'],
  [CLAUDE_TOOL_NAMES.ASK_USER_QUESTION, 'question'],
  // The deferred-tool probe searches the tool REGISTRY. `search` is a query against a
  // corpus the session holds, and the file tree is one corpus of several, so the kind
  // fits (`ir/toolKind.ts`). The matches are TOOL NAMES, so the reader leaves the two
  // file fields of `SearchResult` empty -- see `extractors/toolCall.ts`.
  [CLAUDE_TOOL_NAMES.TOOL_SEARCH, 'search'],
  // A Model Context Protocol RESOURCE, which is a document the server holds rather
  // than a tool it runs. Reading one is a read, and listing them is a listing.
  [CLAUDE_TOOL_NAMES.LIST_MCP_RESOURCES, 'list'],
  [CLAUDE_TOOL_NAMES.READ_MCP_RESOURCE, 'read'],
  [CLAUDE_TOOL_NAMES.READ_MCP_RESOURCE_DIR, 'read'],
  // The three calls that change WHERE or HOW the session works. Entering and
  // leaving plan mode is the switch the Agent Client Protocol spells, and a
  // worktree move changes the directory the same session then works in -- which
  // is the same statement about the session rather than about a file.
  [CLAUDE_TOOL_NAMES.ENTER_PLAN_MODE, 'switch_mode'],
  [CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE, 'switch_mode'],
  [CLAUDE_TOOL_NAMES.ENTER_WORKTREE, 'switch_mode'],
  [CLAUDE_TOOL_NAMES.EXIT_WORKTREE, 'switch_mode'],
  // Reading a background task's output and stopping it both act on a task the
  // transcript already opened, rather than on the workspace.
  [CLAUDE_TOOL_NAMES.TASK_OUTPUT, 'task'],
  [CLAUDE_TOOL_NAMES.TASK_STOP, 'task'],
  // A message to a peer or to the reader. The row leads with the addressee,
  // which the call payload carries.
  [CLAUDE_TOOL_NAMES.SEND_MESSAGE, 'message'],
  [CLAUDE_TOOL_NAMES.SEND_USER_MESSAGE, 'message'],
  // The agent roster: who is reachable, and which teams group them.
  [CLAUDE_TOOL_NAMES.LIST_AGENTS, 'agents'],
  [CLAUDE_TOOL_NAMES.TEAM_CREATE, 'agents'],
  [CLAUDE_TOOL_NAMES.TEAM_DELETE, 'agents'],
  // A trigger that fires later: a cron entry, or a remote endpoint the agent
  // creates, lists and runs.
  [CLAUDE_TOOL_NAMES.CRON_CREATE, 'trigger'],
  [CLAUDE_TOOL_NAMES.CRON_DELETE, 'trigger'],
  [CLAUDE_TOOL_NAMES.CRON_LIST, 'trigger'],
  [CLAUDE_TOOL_NAMES.REMOTE_TRIGGER, 'trigger'],
  [CLAUDE_TOOL_NAMES.SKILL, 'skill'],
  [CLAUDE_TOOL_NAMES.SLEEP, 'wait'],
  // The turn's own structured answer. It reports state and touches nothing.
  [CLAUDE_TOOL_NAMES.STRUCTURED_OUTPUT, 'report'],
])

/** The tool kind a canonical Claude tool name declares. */
export function claudeToolKind(toolName: string): ToolKind {
  return CLAUDE_TOOL_KINDS.get(toolName) ?? ''
}

/**
 * The icon of a Claude tool whose KIND states less than the tool does.
 *
 * Only a tool that fits no kind, or whose kind icon names something wider than
 * the tool, belongs here. Every other tool takes its kind's own icon, so a Bash
 * call on Claude and a shell call on any other provider draw the same glyph.
 */
const CLAUDE_TOOL_ICONS: ReadonlyMap<string, ToolIconHint> = new Map<string, ToolIconHint>([
  [CLAUDE_TOOL_NAMES.TASK_GET, 'checklist'],
  // A stop is not the wait its kind draws.
  [CLAUDE_TOOL_NAMES.TASK_STOP, 'stop'],
  [CLAUDE_TOOL_NAMES.ENTER_PLAN_MODE, 'plan-enter'],
  [CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE, 'plan-exit'],
  // A webhook fires on a request rather than on a clock.
  [CLAUDE_TOOL_NAMES.REMOTE_TRIGGER, 'webhook'],
  // A worktree move is a branch, which the mode-switch glyph does not say.
  [CLAUDE_TOOL_NAMES.ENTER_WORKTREE, 'branch'],
  [CLAUDE_TOOL_NAMES.EXIT_WORKTREE, 'branch'],
  // The turn's answer is JSON, which the clipboard of `report` does not say.
  [CLAUDE_TOOL_NAMES.STRUCTURED_OUTPUT, 'json'],
])

/** The icon a Claude tool overrides its kind's own icon with, if any. */
export function claudeToolIcon(toolName: string): ToolIconHint | undefined {
  return CLAUDE_TOOL_ICONS.get(toolName)
}

/**
 * The tools whose rows Claude suppresses, and on which side.
 *
 * `ToolSearch` is a deferred-tool discovery probe with nothing for a reader.
 * `TaskList` reads back state that the persistent to-do sidebar already shows.
 * The three remaining `Task*` calls draw ONE row: the request states the task,
 * and it reads the result beside it, so the result row would repeat it.
 */
const HIDDEN_REQUEST_TOOLS: ReadonlySet<string> = new Set<string>([
  CLAUDE_TOOL_NAMES.TOOL_SEARCH,
  CLAUDE_TOOL_NAMES.TASK_LIST,
])

const HIDDEN_RESULT_TOOLS: ReadonlySet<string> = new Set<string>([
  CLAUDE_TOOL_NAMES.TOOL_SEARCH,
  CLAUDE_TOOL_NAMES.TASK_LIST,
  CLAUDE_TOOL_NAMES.TASK_CREATE,
  CLAUDE_TOOL_NAMES.TASK_UPDATE,
  CLAUDE_TOOL_NAMES.TASK_GET,
  // `EnterPlanMode` carries no `tool_result` block at all (plugin.ts), so its
  // result row would draw nothing regardless.
  CLAUDE_TOOL_NAMES.ENTER_PLAN_MODE,
])

/** Whether this side of a Claude tool span draws no row at all. */
export function claudeToolRowHidden(toolName: string, side: 'request' | 'result'): boolean {
  return side === 'request' ? HIDDEN_REQUEST_TOOLS.has(toolName) : HIDDEN_RESULT_TOOLS.has(toolName)
}
