import { KIMI_CONVERSATION_FIELD, KIMI_CONVERSATION_ITEM_KIND, KIMI_DISPLAY, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { pickString } from '~/lib/jsonPick'
import { kimiConversationRow } from '../conversation'
import { kimiDisplay } from '../protocol'

/**
 * The plan an `ExitPlanMode` call proposes, or null for any other row.
 *
 * The call's display states the whole plan the model wrote to its plan file, so the
 * row draws the plan itself rather than a tool card. The classifier and the row
 * extractor both read it through this one function, so the row the list measures and
 * the row drawn agree.
 */
export function kimiPlanText(parsed: unknown): string | null {
  const row = kimiConversationRow(parsed)
  if (row?.kind !== 'item' || row.item[KIMI_CONVERSATION_FIELD.Type] !== KIMI_CONVERSATION_ITEM_KIND.ToolCall
    || row.item[KIMI_CONVERSATION_FIELD.Name] !== KIMI_TOOL.ExitPlanMode) {
    return null
  }
  const display = kimiDisplay(row.item)
  if (pickString(display, 'kind') !== KIMI_DISPLAY.PlanReview)
    return null
  const plan = pickString(display, 'plan')
  return plan.trim()
    ? plan
    : null
}
