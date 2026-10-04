import type { ToolKind } from '../../model/toolKind'
import { QWEN_TOOL } from '~/generated/contracts/qwen-protocol'

/**
 * The tool names Qwen Code states in `_meta.toolName`, as the words a branch compares
 * against.
 *
 * Qwen's titles are display prose (`Shell: touch x (Create a marker file)`), so the
 * name in `_meta` is the one stable identity of a call. The kind table below keys on
 * these, so one spelling serves both halves.
 *
 * Not in `contracts/qwen-protocol.json`, which holds the identifiers BOTH programs
 * read. The six names the worker reads too -- `agent`, `workflow`, `todo_write`,
 * `ask_user_question`, `exit_plan_mode` and `run_shell_command` -- are there, as
 * `QWEN_TOOL`, and the adapter reads those from the contract.
 */
export const QWEN_TOOL_NAME = {
  CronCreate: 'cron_create',
  CronDelete: 'cron_delete',
  CronList: 'cron_list',
  Edit: 'edit',
  EnterPlanMode: 'enter_plan_mode',
  EnterWorktree: 'enter_worktree',
  ExitWorktree: 'exit_worktree',
  Glob: 'glob',
  GrepSearch: 'grep_search',
  ImageGen: 'image_gen',
  ListAgents: 'list_agents',
  ListDirectory: 'list_directory',
  LoopWakeup: 'loop_wakeup',
  Lsp: 'lsp',
  Monitor: 'monitor',
  NotebookEdit: 'notebook_edit',
  ReadFile: 'read_file',
  ReportFindings: 'report_findings',
  SendMessage: 'send_message',
  Skill: 'skill',
  TaskStop: 'task_stop',
  ToolSearch: 'tool_search',
  WebFetch: 'web_fetch',
  WebSearch: 'web_search',
  WriteFile: 'write_file',
  ZoomImage: 'zoom_image',
} as const

/**
 * The kind of each tool Qwen Code runs, by its name.
 *
 * `as const satisfies` keeps every value its own literal, so a branch builds at the
 * kind the table states for the name it matched.
 *
 * Three names of the contract are absent on purpose: `agent` and `workflow` (subagent
 * runs) and `todo_write` (a checklist), which the adapter builds from branches of its
 * own. `loop_wakeup` schedules the next turn after a delay, which is a scheduled job,
 * so it takes `trigger` beside the cron tools.
 */
export const QWEN_TOOL_KINDS = {
  [QWEN_TOOL.AskUserQuestion]: 'question',
  [QWEN_TOOL.ExitPlanMode]: 'switch_mode',
  [QWEN_TOOL.RunShellCommand]: 'execute',
  [QWEN_TOOL_NAME.CronCreate]: 'trigger',
  [QWEN_TOOL_NAME.CronDelete]: 'trigger',
  [QWEN_TOOL_NAME.CronList]: 'trigger',
  [QWEN_TOOL_NAME.Edit]: 'edit',
  [QWEN_TOOL_NAME.EnterPlanMode]: 'switch_mode',
  [QWEN_TOOL_NAME.EnterWorktree]: 'switch_mode',
  [QWEN_TOOL_NAME.ExitWorktree]: 'switch_mode',
  [QWEN_TOOL_NAME.Glob]: 'glob',
  [QWEN_TOOL_NAME.GrepSearch]: 'grep',
  [QWEN_TOOL_NAME.ImageGen]: 'image',
  [QWEN_TOOL_NAME.ListAgents]: 'agents',
  [QWEN_TOOL_NAME.ListDirectory]: 'list',
  [QWEN_TOOL_NAME.LoopWakeup]: 'trigger',
  [QWEN_TOOL_NAME.Lsp]: 'search',
  [QWEN_TOOL_NAME.Monitor]: 'execute',
  [QWEN_TOOL_NAME.NotebookEdit]: 'edit',
  [QWEN_TOOL_NAME.ReadFile]: 'read',
  [QWEN_TOOL_NAME.ReportFindings]: 'report',
  [QWEN_TOOL_NAME.SendMessage]: 'message',
  [QWEN_TOOL_NAME.Skill]: 'skill',
  [QWEN_TOOL_NAME.TaskStop]: 'task',
  [QWEN_TOOL_NAME.ToolSearch]: 'search',
  [QWEN_TOOL_NAME.WebFetch]: 'fetch',
  [QWEN_TOOL_NAME.WebSearch]: 'web_search',
  [QWEN_TOOL_NAME.WriteFile]: 'write',
  [QWEN_TOOL_NAME.ZoomImage]: 'read',
} as const satisfies Record<string, ToolKind>

/**
 * Whether Qwen's own table lists this tool.
 *
 * A type PREDICATE, so a caller's lookup answers the tool's own literal kind rather
 * than the whole union. `Object.hasOwn` and not `??`, because `name` comes off the
 * wire and one that spells an `Object.prototype` member is truthy.
 */
export function isQwenTool(name: string): name is keyof typeof QWEN_TOOL_KINDS {
  return Object.hasOwn(QWEN_TOOL_KINDS, name)
}
