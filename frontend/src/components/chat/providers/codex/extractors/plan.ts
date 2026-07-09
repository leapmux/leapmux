import type { TodoListSource } from '../../../todoListMessage'
import type { TodoItem } from '~/stores/chatTodos'
import { pickString } from '~/lib/jsonPick'
import { pluralize } from '~/lib/plural'
import { normalizeTodoStatus } from '~/stores/chatTodos'
import { CODEX_ITEM } from '~/types/toolMessages'

/** Convert a Codex plan array (from turn/plan/updated) to TodoItem[]. */
function codexPlanToTodos(plan: unknown[]): TodoItem[] {
  return plan.flatMap((entry) => {
    if (typeof entry !== 'object' || entry === null)
      return []
    const step = String((entry as Record<string, unknown>).step || '')
    if (!step)
      return []
    return [{
      content: step,
      status: normalizeTodoStatus((entry as Record<string, unknown>).status),
      activeForm: step,
    }]
  })
}

/**
 * Build a TodoListSource from Codex `turn/plan/updated` params. Returns null
 * when the params don't carry a `plan` array.
 */
export function codexTurnPlanFromParams(
  params: Record<string, unknown> | null | undefined,
): TodoListSource | null {
  if (!params)
    return null
  const plan = params.plan
  if (!Array.isArray(plan))
    return null

  const todos = codexPlanToTodos(plan)
  const explanation = pickString(params, 'explanation').trim()

  if (todos.length === 0) {
    return {
      toolName: 'Plan Update',
      title: '',
      todos: [],
    }
  }

  const label = `${pluralize(todos.length, 'task')}${explanation ? ` - ${explanation}` : ''}`
  return {
    toolName: 'Plan Update',
    title: label,
    todos,
  }
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
  if (item.type !== CODEX_ITEM.PLAN)
    return null
  const text = pickString(item, 'text')
  return text.length > 0 ? text : null
}
