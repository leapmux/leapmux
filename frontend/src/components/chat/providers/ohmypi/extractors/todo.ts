import type { TodoItem } from '~/models/todo'
import { OH_MY_PI_TODO_STATUS } from '~/generated/contracts/ohmypi-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { todoRowKey } from '~/models/todo'

/**
 * The checklist one `todo` call states, after the operation it ran.
 *
 * Every operation -- `init`, `start`, `done`, `drop`, `append` and the rest -- returns
 * the WHOLE list in `details.phases`, grouped in phases, so each finished call is a
 * snapshot. A task has no id: omp matches it by its text.
 */
export interface OhMyPiTodoSource {
  items: TodoItem[]
  /** The operation the call ran, which heads the row. */
  op: string
}

/** omp's task status in the neutral vocabulary. */
function todoStatus(status: string): TodoItem['status'] {
  switch (status) {
    case OH_MY_PI_TODO_STATUS.InProgress:
      return 'in_progress'
    case OH_MY_PI_TODO_STATUS.Completed:
      return 'completed'
    // An abandoned task stops being work but stays visible, which is what the neutral
    // deleted status states.
    case OH_MY_PI_TODO_STATUS.Abandoned:
      return 'deleted'
    // A blocked task is still work to do; its blocker rides in the description.
    case OH_MY_PI_TODO_STATUS.Blocked:
    case OH_MY_PI_TODO_STATUS.Pending:
    default:
      return 'pending'
  }
}

/**
 * The checklist in one `todo` result's `details`, or null when the result states no
 * phases (a failed call, or a result this build cannot read).
 */
export function ohMyPiTodoSource(args: Record<string, unknown>, details: Record<string, unknown> | undefined): OhMyPiTodoSource | null {
  const phases = details?.phases
  if (!Array.isArray(phases))
    return null
  const items: TodoItem[] = []
  for (const phase of phases) {
    if (!isObject(phase) || !Array.isArray(phase.tasks))
      continue
    const phaseName = pickString(phase, 'name')
    for (const task of phase.tasks) {
      if (!isObject(task))
        continue
      const content = pickString(task, 'content').trim()
      if (!content)
        continue
      const blocker = pickString(task, 'blocker').trim()
      const description = [phaseName, blocker ? `Blocked: ${blocker}` : ''].filter(Boolean).join('\n')
      const status = todoStatus(pickString(task, 'status'))
      items.push({
        rowKey: todoRowKey(undefined, items.length, content),
        content,
        status,
        activeForm: status === 'in_progress' ? content : '',
        ...(description ? { description } : {}),
      })
    }
  }
  return { items, op: pickString(args, 'op') }
}
