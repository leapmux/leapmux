import type { TodoRequest, TodoResult } from '../../../model/tools/todo'
import type { MiMoToolPart } from './toolCommon'
import type { TodoItem } from '~/models/todo'
import { MIMO_TASK_ACTION, MIMO_TASK_STATUS } from '~/generated/contracts/mimo-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { todoRowKey } from '~/models/todo'

/**
 * MiMo's to-do tool is `task`: each call carries ONE operation on one work item, or
 * reads the items. The row draws the item the call acted on, the way Claude Code's
 * `Task*` rows do, and the worker folds each change into the agent's to-do list
 * (`todo.go` in the worker's `mimo` package).
 */

/** The neutral status of one MiMo work item. A blocked item is still work that remains. */
export function mimoTaskStatus(status: string): TodoItem['status'] {
  switch (status) {
    case MIMO_TASK_STATUS.InProgress:
      return 'in_progress'
    case MIMO_TASK_STATUS.Done:
      return 'completed'
    case MIMO_TASK_STATUS.Abandoned:
      return 'deleted'
    default:
      return 'pending'
  }
}

/** The operation a `task` call carries, or an empty object for input of another shape. */
function taskOperation(input: Record<string, unknown>): Record<string, unknown> {
  return isObject(input.operation) ? input.operation : {}
}

function taskItem(id: string, content: string, status: TodoItem['status']): TodoItem {
  return { ...(id ? { id } : {}), rowKey: todoRowKey(id || undefined, 0, content), content, status, activeForm: '' }
}

/** `T1 in_progress — Write the parser`, one line of a list call's output. */
const LIST_LINE = /^(T\d+(?:\.\d+)*) ([a-z_]+) — (.*)$/

/**
 * The item one `task` call ACTED on, from its arguments alone.
 *
 * A create states the item's text; a rename states its new text; every other change
 * states the item's id and nothing else, so the id stands in for the text until the
 * call answers. A list and a get state no item before they answer.
 */
export function mimoTaskRequest(input: Record<string, unknown>): TodoRequest {
  const operation = taskOperation(input)
  const action = pickString(operation, 'action')
  const id = pickString(operation, 'id')
  const note = pickString(operation, 'event_summary')
  const notes = note ? { note } : {}
  switch (action) {
    case MIMO_TASK_ACTION.Create:
      return { items: [taskItem('', pickString(operation, 'summary'), 'pending')] }
    case MIMO_TASK_ACTION.Rename:
      return { items: id ? [taskItem(id, pickString(operation, 'summary'), 'pending')] : [] }
    case MIMO_TASK_ACTION.Start:
    case MIMO_TASK_ACTION.Block:
    case MIMO_TASK_ACTION.Unblock:
    case MIMO_TASK_ACTION.Done:
    case MIMO_TASK_ACTION.Abandon:
      return { items: id ? [taskItem(id, id, 'pending')] : [], ...notes }
    default:
      return { items: [] }
  }
}

/**
 * The items one COMPLETED `task` call answered with, or null when the answer states
 * none in a form this build reads.
 *
 * A change answers with the item's id and its status after the change, in the
 * metadata. A list answers with one line per item, and a get with the item as JSON.
 */
export function mimoTaskResult(part: MiMoToolPart, request: TodoRequest): TodoResult | null {
  const operation = taskOperation(part.input)
  const action = pickString(operation, 'action')
  if (action === MIMO_TASK_ACTION.List) {
    if (part.output.trim() === 'No tasks.')
      return { items: [], emptyText: 'No tasks.' }
    const items = part.output.split('\n').flatMap((line) => {
      const match = LIST_LINE.exec(line.trim())
      return match ? [taskItem(match[1] ?? '', match[3] ?? '', mimoTaskStatus(match[2] ?? ''))] : []
    })
    return items.length > 0 ? { items } : null
  }
  if (action === MIMO_TASK_ACTION.Get) {
    let record: unknown
    try {
      record = JSON.parse(part.output)
    }
    catch {
      return null
    }
    if (!isObject(record) || !pickString(record, 'id'))
      return null
    return { items: [taskItem(pickString(record, 'id'), pickString(record, 'summary'), mimoTaskStatus(pickString(record, 'status')))] }
  }
  const id = pickString(part.metadata, 'id')
  const status = pickString(part.metadata, 'status')
  const requested = request.items[0]
  if (!id || !status || !requested)
    return null
  // The text the request stated. A status change states the item's id alone.
  return { items: [taskItem(id, requested.content || id, mimoTaskStatus(status))], ...(request.note !== undefined ? { note: request.note } : {}) }
}
