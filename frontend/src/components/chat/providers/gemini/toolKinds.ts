import type { ToolKind } from '../../model/toolKind'
import { GEMINI_TOOL } from '~/generated/contracts/gemini-protocol'

/**
 * The kind of each Gemini CLI tool that the shared build reads at a kind of its own.
 *
 * Gemini CLI states the tool name in the call identifier and in the stored session
 * record, and the wire kind cannot separate the tools: it sends the edit kind for both
 * `write_file` and `replace`. The live reader and the stored reader both build these
 * tools at the kind that this table states, so the two readers cannot disagree.
 *
 * `as const satisfies` keeps every value its own literal, so a reader builds at the kind
 * that the table states for the name that it matched.
 *
 * The other names of the contract are absent on purpose:
 *
 * - `write_todos` and `invoke_agent` build from reader branches of their own. A to-do
 *   update that carries no list is not a checklist, and it takes the shared build at the
 *   wire kind.
 * - `enter_plan_mode`, `exit_plan_mode` and `complete_task` take the shared build at the
 *   wire kind until the stored session record of the call arrives. The stored reader
 *   reads a finished `complete_task` as a report.
 */
export const GEMINI_TOOL_KINDS = {
  [GEMINI_TOOL.RunShellCommand]: 'execute',
  [GEMINI_TOOL.ReadFile]: 'read',
  [GEMINI_TOOL.WriteFile]: 'write',
  [GEMINI_TOOL.Replace]: 'edit',
} as const satisfies Record<string, ToolKind>

/**
 * Whether Gemini's own table lists this tool.
 *
 * A type PREDICATE, so a caller's lookup answers the tool's own literal kind rather than
 * the whole union. `Object.hasOwn` and not `??`, because the name comes off the wire, and
 * a name that spells an `Object.prototype` member is truthy.
 */
export function isGeminiTool(name: string): name is keyof typeof GEMINI_TOOL_KINDS {
  return Object.hasOwn(GEMINI_TOOL_KINDS, name)
}
