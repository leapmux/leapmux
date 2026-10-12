import type { ToolKind } from '../../model/toolKind'

/**
 * The shared tool kind each CodeBuddy Code tool declares.
 *
 * CodeBuddy reports a tool by NAME alone, so this table is where the name
 * becomes the closed kind that drives the icon, the label, the title and the
 * input summary. A name the table does not list is a plugin tool, an MCP tool or
 * a later CodeBuddy, and it keeps the generic card.
 *
 * A Map rather than an object, because a plugin can register a tool called
 * `constructor` or `toString`.
 */
const CODEBUDDY_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
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
])

/**
 * The tool kind one CodeBuddy tool declares, or the unspecified kind for a name
 * it does not know.
 */
export function codebuddyToolKind(toolName: string): ToolKind {
  return CODEBUDDY_TOOL_KINDS.get(toolName) ?? 'unspecified'
}
