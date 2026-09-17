import type { TodoItem } from '~/models/todo'
import { CODEX_ITEM } from '~/generated/contracts/codex-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { normalizeTodoStatus, todoRowKey } from '~/models/todo'

/** Convert a Codex plan array (from turn/plan/updated) to TodoItem[]. */
function codexPlanToTodos(plan: unknown[]): TodoItem[] {
  return plan.flatMap((entry, i) => {
    if (!isObject(entry))
      return []
    const step = String(entry.step || '')
    if (!step)
      return []
    return [{
      // Codex re-sends the whole plan with no ids, so position plus content is
      // the identity -- see TodoItem.rowKey. The index is the entry's place in
      // the RAW plan, which is stable across a re-send even where a skipped
      // entry makes it differ from the output index.
      rowKey: todoRowKey(undefined, i, step),
      content: step,
      status: normalizeTodoStatus(entry.status),
      activeForm: step,
    }]
  })
}

/**
 * The checklist a Codex `turn/plan/updated` notification carries, or null when it
 * carries no `plan` array at all.
 *
 * Null rather than an empty list: a frame with no plan is one the row cannot draw,
 * while an empty ARRAY is a cleared plan, which the shared checklist header states in
 * its own words.
 */
export function codexTurnPlanTodos(params: Record<string, unknown> | null | undefined): TodoItem[] | null {
  const plan = params?.plan
  return Array.isArray(plan) ? codexPlanToTodos(plan) : null
}

/**
 * The half of a `turn/plan/updated` frame that carries the plan.
 *
 * Its own `params`, or the frame itself for a stored row that was unwrapped. The
 * classifier and the extractor both read it THROUGH this, because they must reach the
 * same answer: a classifier that claimed a tool row the extractor then refused left
 * the transcript drawing the raw notification JSON in a measured row.
 */
export function codexTurnPlanParams(notification: Record<string, unknown>): Record<string, unknown> {
  return pickObject(notification, 'params') ?? notification
}

/**
 * Pull markdown body text from a Codex `plan` item (proposed plan). Returns
 * null when the item is not a plan or carries no text.
 */
export function codexPlanItemMarkdown(
  item: Record<string, unknown> | null | undefined,
): string | null {
  if (!item)
    return null
  if (item.type !== CODEX_ITEM.Plan)
    return null
  const text = pickString(item, 'text')
  return text.length > 0 ? text : null
}
