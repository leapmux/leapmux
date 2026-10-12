import type { ToolKind } from '../../model/toolKind'

/**
 * The shared tool kind each Qoder CLI tool declares.
 *
 * Qoder reports a tool by NAME alone, so this table is where the name
 * becomes the closed kind that drives the icon, the label, the title and the
 * input summary. A name the table does not list is a plugin tool, an MCP tool or
 * a later Qoder, and it keeps the generic card.
 *
 * A Map rather than an object, because a plugin can register a tool called
 * `constructor` or `toString`.
 */
const QODER_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  ['Bash', 'execute'],
  ['Edit', 'edit'],
  ['Write', 'write'],
  ['Read', 'read'],
  ['Grep', 'grep'],
  ['Glob', 'glob'],
  ['WebSearch', 'web_search'],
  ['WebFetch', 'fetch'],
  ['NotebookEdit', 'edit'],
  ['Agent', 'agent'],
  ['AskUserQuestion', 'question'],
  ['EnterPlanMode', 'switch_mode'],
  ['ExitPlanMode', 'switch_mode'],
  ['TaskCreate', 'task'],
  ['TaskGet', 'task'],
  ['TaskList', 'task'],
  ['TaskUpdate', 'task'],
  ['TaskStop', 'task'],
  ['TaskOutput', 'task'],
  ['Workflow', 'task'],
  ['Monitor', 'wait'],
  ['REPL', 'execute'],
  ['SendUserMessage', 'message'],
  ['Skill', 'skill'],
  ['CreateGoal', 'task'],
  ['UpdateGoal', 'task'],
  ['GetGoal', 'task'],
  ['WriteTodos', 'todo'],
  ['ScheduleWakeup', 'wait'],
  ['CronCreate', 'task'],
  ['CronDelete', 'task'],
  ['CronList', 'task'],
  ['EnterWorktree', 'switch_mode'],
  ['ExitWorktree', 'switch_mode'],
])

/**
 * The tool kind one Qoder tool declares, or the unspecified kind for a name
 * it does not know.
 */
export function qoderToolKind(toolName: string): ToolKind {
  return QODER_TOOL_KINDS.get(toolName) ?? 'unspecified'
}
