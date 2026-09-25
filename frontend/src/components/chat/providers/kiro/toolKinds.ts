import type { ToolKind } from '../../model/toolKind'
import { KIRO_TOOL_TITLE } from '~/generated/contracts/kiro-protocol'

/**
 * The titles Kiro gives its tool calls, as the words a branch compares against.
 *
 * Kiro states no tool NAME on a tool call. Each built-in tool has one fixed title,
 * so the title identifies the tool. Four kinds of call are the exception, and the
 * adapter identifies them another way:
 *
 *   - A shell command takes the model's own description as its title. Its wire kind
 *     `execute` identifies it.
 *   - A subagent takes `Sub-agent: <name>`. Its `_meta.kiro.kind` identifies it.
 *   - A question takes the question as its title. Its `_meta.kiro.toolId` identifies
 *     it.
 *   - A tool of a Model Context Protocol server takes `@server/tool`.
 *
 * Not in `contracts/kiro-protocol.json`, which holds the titles BOTH programs read.
 * The worker reads the to-do list and the plan switch -- those two are in the
 * contract, as `KIRO_TOOL_TITLE` -- and none of the titles below.
 */
export const KIRO_TOOL = {
  AppendToFile: 'Append to File',
  ControlProcess: 'Control Process',
  DeleteFile: 'Delete File',
  FetchUrl: 'Fetch URL',
  FileSearch: 'File Search',
  GrepSearch: 'Grep Search',
  KnowledgeSearch: 'Knowledge Search',
  ListDirectory: 'List Directory',
  Memory: 'Memory',
  ReadFile: 'Read File',
  ReplaceInFile: 'Replace in File',
  ReportProgress: 'Report Progress',
  ToolSearch: 'Tool Search',
  UpdateSessionInformation: 'Update Session Information',
  WriteFile: 'Write File',
} as const

/**
 * The kind of each tool Kiro runs, by its title.
 *
 * `as const satisfies` keeps every value its own literal, so a branch builds at the
 * kind the table states for the title it matched, and `satisfies` still refuses a
 * word that is not a `ToolKind`.
 *
 * `Append to File` adds text at the end of a file, which the edit card draws as an
 * insertion. `Update Session Information` and `Report Progress` both state what the
 * agent does now, which is the prose a report draws. `Knowledge Search` and
 * `Tool Search` query a corpus the session holds, so they take `search` and never
 * `grep`.
 */
export const KIRO_TOOL_KINDS = {
  [KIRO_TOOL.AppendToFile]: 'edit',
  [KIRO_TOOL.ControlProcess]: 'task',
  [KIRO_TOOL.DeleteFile]: 'delete',
  [KIRO_TOOL.FetchUrl]: 'fetch',
  [KIRO_TOOL.FileSearch]: 'glob',
  [KIRO_TOOL.GrepSearch]: 'grep',
  [KIRO_TOOL.KnowledgeSearch]: 'search',
  [KIRO_TOOL.ListDirectory]: 'list',
  [KIRO_TOOL.Memory]: 'memory',
  [KIRO_TOOL.ReadFile]: 'read',
  [KIRO_TOOL.ReplaceInFile]: 'edit',
  [KIRO_TOOL.ReportProgress]: 'report',
  [KIRO_TOOL.ToolSearch]: 'search',
  [KIRO_TOOL.UpdateSessionInformation]: 'report',
  [KIRO_TOOL.WriteFile]: 'write',
  [KIRO_TOOL_TITLE.TaskList]: 'todo',
  [KIRO_TOOL_TITLE.SwitchToExecution]: 'switch_mode',
} as const satisfies Record<string, ToolKind>

/**
 * Whether Kiro's own table lists this title.
 *
 * A type PREDICATE, so a caller's lookup answers the tool's own literal kind rather
 * than the whole union. `Object.hasOwn` and not `??`, because `title` comes off the
 * wire and one that spells an `Object.prototype` member is truthy.
 */
export function isKiroTool(title: string): title is keyof typeof KIRO_TOOL_KINDS {
  return Object.hasOwn(KIRO_TOOL_KINDS, title)
}
