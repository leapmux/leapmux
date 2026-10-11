import type { ToolKind } from '../../model/toolKind'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { PI_AGENT_TOOL, PI_POWERSHELL_TOOL, PI_SEARCH_TOOL } from './protocol'

/**
 * Read each Pi tool name as a shared tool kind.
 *
 * The kind determines the icon and label. It also determines the title and input summary.
 * Tools outside the table use the rich-content card that Pi extensions need.
 *
 * Every provider uses `switch_mode` for its plan tool. Pi usually routes a plan through
 * the shared `MarkdownPlanLayout` before this lookup. This entry covers a call that contains no plan.
 *
 * A Map rejects absent keys such as `constructor` and `toString`.
 * A plain object would read those keys from `Object.prototype`.
 */
const PI_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  [PI_TOOL.Codemode, 'mcp'],
  [PI_TOOL.Bash, 'execute'],
  [PI_POWERSHELL_TOOL, 'execute'],
  [PI_TOOL.Read, 'read'],
  [PI_TOOL.Write, 'write'],
  [PI_TOOL.Edit, 'edit'],
  [PI_TOOL.Todo, 'todo'],
  [PI_TOOL.Agent, 'agent'],
  [PI_TOOL.SubagentWorkflow, 'agent'],
  [PI_AGENT_TOOL.GetResult, 'agent'],
  [PI_AGENT_TOOL.Steer, 'agent'],
  [PI_SEARCH_TOOL.Grep, 'grep'],
  [PI_SEARCH_TOOL.Find, 'glob'],
  [PI_SEARCH_TOOL.List, 'list'],
  // rpiv-ask-user-question registers the native question tool.
  [PI_TOOL.AskUserQuestion, 'question'],
  // Plan and goal questions use the same question kind.
  // The goal extension supports one question or a complete questionnaire.
  [PI_TOOL.PlanQuestion, 'question'],
  [PI_TOOL.GoalQuestion, 'question'],
  [PI_TOOL.GoalQuestionnaire, 'question'],
  [PI_TOOL.PlanComplete, 'switch_mode'],
])

/**
 * Return the shared kind, or `unspecified` for a tool outside the table.
 *
 * `toolVocabulary.test.ts` checks this exact lookup for missing tools.
 * No transcript row retains `unspecified`. {@link piToolCall} gives an unknown tool
 * the `mcp` kind, which supports the rich content that extensions and native MCP return.
 */
export function piToolKind(toolName: string): ToolKind {
  return PI_TOOL_KINDS.get(toolName) ?? 'unspecified'
}
