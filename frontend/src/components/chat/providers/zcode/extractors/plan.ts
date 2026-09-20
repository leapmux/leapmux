import { ZCODE_INTERACTION, ZCODE_METHOD, ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { zcodeExtractTool } from './toolCommon'

/** Read a plan whose only persisted source is its native approval request. */
export function zcodeControlPlanText(parsed: unknown): string | null {
  if (!isObject(parsed) || parsed.method !== ZCODE_METHOD.RequestUserInput)
    return null
  const params = pickObject(parsed, 'params')
  if (pickObject(params, 'schema')?.interaction !== ZCODE_INTERACTION.PlanApproval)
    return null
  for (const content of [pickObject(params, 'input'), pickObject(params, 'context')]) {
    const plan = pickString(content, 'plan')
    if (plan.trim())
      return plan
  }
  return pickString(params, 'prompt')
}

/**
 * The plan one row proposes, whichever of the two shapes carries it.
 *
 * ONE reader, because BOTH layers of the pipeline ask the question and they must
 * agree: `classify` answers `assistant_plan` for a frame this finds a plan in, and
 * `zcodeExtractRow` draws that plan. While the classifier said `tool_use` and the
 * extractor said `assistant-plan`, the virtual list measured a tool row and the
 * transcript painted a plan card into it.
 *
 * It reads the row's OWN bytes and never the sibling beside it, so the answer cannot
 * change as the store resolves a pair. `ExitPlanMode` carries its plan in the
 * arguments of the `scheduled` row, which is the only row that proposes one; the
 * result row states the approval instead and draws the ordinary tool row.
 */
export function zcodePlanText(parsed: unknown, spanType?: string, supplemental?: unknown): string | null {
  const control = zcodeControlPlanText(parsed)
  if (control !== null)
    return control
  const update = zcodeExtractTool(parsed)
  if (!update)
    return null
  const toolName = update.toolName || spanType || ''
  if (toolName !== ZCODE_TOOL.ExitPlanMode)
    return null
  // The plan rides the SUPPLEMENTAL stream input when the daemon persisted the
  // arguments outside the frame -- the same stream every other argument of a
  // `scheduled` call arrives through.
  const streamed = pickString(zcodeExtractTool(supplemental ?? undefined)?.input ?? {}, 'plan')
  const plan = pickString(update.input, 'plan').trim() || (typeof streamed === 'string' ? streamed.trim() : '')
  return plan || null
}
