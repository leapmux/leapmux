import type { ToolKind } from '../../model/toolKind'

/**
 * The tool names Grok Build states in `_meta["x.ai/tool"].name`, as the words a branch
 * compares against.
 *
 * The kind table below keys on these, so one spelling serves both halves. A branch
 * that retyped the string kept compiling after a rename and simply stopped firing.
 *
 * Not in `contracts/grok-protocol.json`, which holds the identifiers BOTH programs
 * read. The worker reads `spawn_subagent` alone -- that one is in the contract, as
 * `GROK_TOOL` -- and none of the names below.
 */
export const GROK_TOOL_NAME = {
  AskUserQuestion: 'ask_user_question',
  EnterPlanMode: 'enter_plan_mode',
  ExitPlanMode: 'exit_plan_mode',
  GetOutput: 'get_command_or_subagent_output',
  Grep: 'grep',
  Kill: 'kill_command_or_subagent',
  ListDir: 'list_dir',
  Monitor: 'monitor',
  ReadFile: 'read_file',
  RunTerminalCommand: 'run_terminal_command',
  SchedulerCreate: 'scheduler_create',
  SchedulerDelete: 'scheduler_delete',
  SchedulerList: 'scheduler_list',
  SearchReplace: 'search_replace',
  SearchTool: 'search_tool',
  TodoWrite: 'todo_write',
  UseTool: 'use_tool',
  WebFetch: 'web_fetch',
  WebSearch: 'web_search',
  Workflow: 'workflow',
  Write: 'write',
} as const

/**
 * The kind of each tool Grok Build runs, by its name.
 *
 * `as const satisfies` keeps every value its own literal, so a branch builds at the
 * kind the table states for the name it matched, and `satisfies` still refuses a word
 * that is not a `ToolKind`.
 *
 * Three names that the adapter builds from branches of its own are absent:
 * `todo_write` (a checklist), `use_tool` (the call it wraps) and `workflow` (a run of
 * subagents). `spawn_subagent` is the contract's `GROK_TOOL.SpawnSubagent`, and the
 * adapter builds it the same way.
 *
 * `monitor` runs a command whose output lines become events, so it states the command
 * the way a shell call does. `search_tool` searches the tool registry, which is a
 * corpus the session holds, so it takes `search` and never `web_search`.
 */
export const GROK_TOOL_KINDS = {
  [GROK_TOOL_NAME.AskUserQuestion]: 'question',
  [GROK_TOOL_NAME.EnterPlanMode]: 'switch_mode',
  [GROK_TOOL_NAME.ExitPlanMode]: 'switch_mode',
  [GROK_TOOL_NAME.GetOutput]: 'task',
  [GROK_TOOL_NAME.Grep]: 'grep',
  [GROK_TOOL_NAME.Kill]: 'task',
  [GROK_TOOL_NAME.ListDir]: 'list',
  [GROK_TOOL_NAME.Monitor]: 'execute',
  [GROK_TOOL_NAME.ReadFile]: 'read',
  [GROK_TOOL_NAME.RunTerminalCommand]: 'execute',
  [GROK_TOOL_NAME.SchedulerCreate]: 'trigger',
  [GROK_TOOL_NAME.SchedulerDelete]: 'trigger',
  [GROK_TOOL_NAME.SchedulerList]: 'trigger',
  [GROK_TOOL_NAME.SearchReplace]: 'edit',
  [GROK_TOOL_NAME.SearchTool]: 'search',
  [GROK_TOOL_NAME.WebFetch]: 'fetch',
  [GROK_TOOL_NAME.WebSearch]: 'web_search',
  [GROK_TOOL_NAME.Write]: 'write',
} as const satisfies Record<string, ToolKind>

/**
 * Whether Grok's own table lists this tool.
 *
 * A type PREDICATE, so a caller's lookup answers the tool's own literal kind rather
 * than the whole union. `Object.hasOwn` and not `??`, because `name` comes off the
 * wire and one that spells an `Object.prototype` member is truthy.
 */
export function isGrokTool(name: string): name is keyof typeof GROK_TOOL_KINDS {
  return Object.hasOwn(GROK_TOOL_KINDS, name)
}
