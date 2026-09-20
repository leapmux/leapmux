import type { ToolKind } from '../../model/toolKind'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { PI_AGENT_TOOL, PI_POWERSHELL_TOOL, PI_SEARCH_TOOL } from './protocol'

/**
 * The shared tool kind each Pi tool declares.
 *
 * Pi reports a tool by NAME alone, so this table is where the name becomes the closed
 * kind that drives the icon, the label, the title and the input summary. A name that
 * is absent from the table keeps the rich-content row, which is what Pi's extensions
 * return.
 *
 * `plan_mode_complete` takes `switch_mode`, the word the Agent Client Protocol
 * spells for leaving one mode for another, and every provider's plan-mode tool
 * answers it. The row usually leaves the tool path before the kind is read -- a
 * plan draws through `MarkdownPlanLayout`, which every provider shares -- so the
 * kind serves the call that carried no plan.
 *
 * A Map rather than an object, here and below: an extension may be called
 * `constructor` or `toString`, and a plain object answers those two names from
 * `Object.prototype` instead of reporting that it holds no entry.
 */
const PI_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
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
  // The question tool rpiv-ask-user-question registers. A question is the agent
  // stopping to reason with the reader, which is the kind every provider's own
  // question tool takes.
  [PI_TOOL.AskUserQuestion, 'question'],
  // The two other surfaces that stop and ask. The plan asks before it commits to a
  // plan; the goal extension asks one question or a whole questionnaire. They are the
  // same act as the tool above, so they take the same kind rather than falling to the
  // uncategorized row, which is what they did while the contract did not list them.
  [PI_TOOL.PlanQuestion, 'question'],
  [PI_TOOL.GoalQuestion, 'question'],
  [PI_TOOL.GoalQuestionnaire, 'question'],
  [PI_TOOL.PlanComplete, 'switch_mode'],
])

/**
 * The tool kind one Pi tool declares, or the unspecified kind for a name it does not know.
 *
 * The empty answer is what `toolVocabulary.test.ts` reads to find a tool the table
 * forgot, so it stays the literal table lookup. No ROW carries it: an unnamed tool
 * is a Pi extension or a Model Context Protocol bridge, and {@link piToolCall}
 * gives it the `mcp` kind that matches the card it draws.
 */
export function piToolKind(toolName: string): ToolKind {
  return PI_TOOL_KINDS.get(toolName) ?? 'unspecified'
}
