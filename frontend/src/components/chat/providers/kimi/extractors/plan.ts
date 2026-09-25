import { KIMI_DISPLAY, KIMI_EVENT, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { pickString } from '~/lib/jsonPick'
import { kimiDisplay, kimiEventData } from '../protocol'

/**
 * The plan an `ExitPlanMode` call proposes, or null for any other row.
 *
 * The call's display states the whole plan the model wrote to its plan file, so the
 * row draws the plan itself rather than a tool card. The classifier and the row
 * extractor both read it through this one function, so the row the list measures and
 * the row drawn agree.
 */
export function kimiPlanText(parsed: unknown): string | null {
  const start = kimiEventData(parsed, KIMI_EVENT.ToolCallStarted)
  if (!start || pickString(start, 'name') !== KIMI_TOOL.ExitPlanMode)
    return null
  const display = kimiDisplay(start)
  if (pickString(display, 'kind') !== KIMI_DISPLAY.PlanReview)
    return null
  const plan = pickString(display, 'plan')
  return plan.trim() ? plan : null
}
