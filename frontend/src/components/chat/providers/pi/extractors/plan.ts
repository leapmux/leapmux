import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { pickString } from '~/lib/jsonPick'
import { piExtractTool } from './toolCommon'

/**
 * What the closing row of a plan call says.
 *
 * The plan itself is on the REQUEST row, which carries it from the moment the call
 * opens. This row marks the end of the proposal and nothing more.
 */
const PLAN_READY_NOTICE = 'Plan ready for review.'

/**
 * What one `plan_mode_complete` row states: the plan itself, or the line that says
 * the plan is ready.
 *
 * ONE reader, because BOTH layers of the pipeline ask the question and they must
 * agree: `classify` answers `assistant_plan` or `assistant_text` from this, and
 * `piExtractRow` draws the same row. While the classifier said `tool_use` and the
 * extractor said `assistant-plan`, the virtual list measured a tool row and the
 * transcript painted a plan card into it.
 *
 * It reads the row's OWN bytes and never the sibling beside it. The reader it
 * replaced consulted the paired request to decide whether the result row should
 * repeat the plan, which made the row's kind depend on whether the store had
 * resolved that pair yet -- so the same row drew a plan card before the pair landed
 * and a one-line notice afterwards.
 *
 * The precedence follows where Pi puts the words:
 *
 *   - The START event carries the plan in its arguments. That row IS the plan, and
 *     it is the row a reader approves from, so it must draw while the call runs.
 *   - A FAILED end states why the call was refused. The plan it ignored is not the
 *     answer to that.
 *   - An end that repeats the plan states the NOTICE instead. Pi writes its result
 *     text as `**Proposed Plan**` followed by the whole plan again, so drawing
 *     either the plan or that text would print the plan a second time, right under
 *     the card the request row already drew.
 *   - Every other end states its own result text.
 *
 * Returns null for every other tool and for a `plan_mode_complete` row that states
 * nothing. The caller then draws the ordinary tool row.
 */
export function piPlanStatement(
  payload: Record<string, unknown> | null | undefined,
): { kind: 'plan' | 'text', text: string } | null {
  const tool = piExtractTool(payload)
  if (!tool || tool.toolName !== PI_TOOL.PlanComplete)
    return null
  const argsPlan = pickString(tool.args, 'plan').trim()
  if (argsPlan)
    return { kind: 'plan', text: argsPlan }
  const resultText = (tool.result?.text ?? tool.partialResult?.text ?? '').trim()
  if (tool.isError)
    return resultText ? { kind: 'text', text: resultText } : null
  if (pickString(tool.result?.details, 'plan').trim())
    return { kind: 'text', text: PLAN_READY_NOTICE }
  return resultText ? { kind: 'text', text: resultText } : null
}
