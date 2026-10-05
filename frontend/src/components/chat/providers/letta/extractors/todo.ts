import type { ToolRequestByKind, ToolResultByKind } from '../../../model/tools'
import type { TodoItem } from '~/models/todo'
import { normalizeTodoStatus, rawTodosToItems } from '~/components/chat/normalizers/todo'
import { LETTA_TOOL } from '~/generated/contracts/letta-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { todoRowKey } from '~/models/todo'

/**
 * The to-do lists of one Letta Code task call.
 *
 * `request` is the task or the list that the call acts on. `result` is the list
 * that the native answer states, or null when the answer states none.
 */
export interface LettaTodoLists {
  request: ToolRequestByKind['todo']
  result: ToolResultByKind['todo'] | null
}

/**
 * The to-do lists of one `TaskCreate`, `TaskUpdate`, `TaskGet`, `TaskList` or
 * `UpdatePlan` call. Null when neither the arguments nor the answer state a task,
 * so that the row keeps the generic card instead of an empty checklist.
 *
 * Each tool states its task in a different half of the call:
 *
 * - `TaskCreate` states the whole task in its arguments, before an ID exists.
 * - `TaskUpdate` sends a patch, and `TaskGet` sends an ID alone.
 * - `TaskList` sends nothing.
 *
 * Every task tool answers with the complete native record: `TaskList` with
 * `{tasks: [...]}`, and each of the others with the one task. The answer is thus
 * the saved state, and it wins over the arguments. `UpdatePlan` answers with a
 * message alone, so the plan that it sent is the plan that it saved.
 *
 * `answer` is the native `tool_return` of a successful call. It is undefined
 * while the call runs and for a call that failed.
 */
export function lettaTodoLists(toolName: string, args: Record<string, unknown>, answer: unknown): LettaTodoLists | null {
  const requested = lettaRequestedTodos(toolName, args)
  const answered = answer === undefined ? null : lettaAnsweredTodos(toolName, answer)
  const request = answered ?? requested
  if (!request)
    return null
  const result = answered ?? (answer !== undefined && toolName === LETTA_TOOL.UpdatePlan ? requested : null)
  return { request, result }
}

/** The task or the plan that the arguments of one call state. Null when they state none. */
function lettaRequestedTodos(toolName: string, args: Record<string, unknown>): ToolRequestByKind['todo'] | null {
  if (toolName === LETTA_TOOL.TaskCreate)
    return lettaSingleTask(lettaTaskItem({ ...args, status: 'pending' }, 0))
  if (toolName === LETTA_TOOL.UpdatePlan && Array.isArray(args.plan)) {
    const items = rawTodosToItems(args.plan.filter(isObject).map(step => ({
      content: pickString(step, 'step'),
      status: pickString(step, 'status'),
    })))
    const note = pickString(args, 'explanation').trim()
    return { items, ...(note ? { note } : {}) }
  }
  return null
}

/** The tasks that the native answer of one call states. Null when it states none. */
function lettaAnsweredTodos(toolName: string, answer: unknown): ToolResultByKind['todo'] | null {
  const record = lettaAnswerRecord(answer)
  if (!record)
    return null
  switch (toolName) {
    case LETTA_TOOL.TaskList: {
      if (!Array.isArray(record.tasks))
        return null
      const items = record.tasks.map((task, index) => lettaTaskItem(task, index))
      return items.every(item => item !== null) ? { items } : null
    }
    case LETTA_TOOL.TaskCreate:
    case LETTA_TOOL.TaskUpdate:
    case LETTA_TOOL.TaskGet:
      return lettaSingleTask(lettaTaskItem(record, 0))
    default:
      return null
  }
}

/**
 * One call's single task as a checklist. The description goes beside the
 * checklist, not on the item, so that the row shows it instead of a hover.
 */
function lettaSingleTask(item: TodoItem | null): ToolRequestByKind['todo'] | null {
  if (!item)
    return null
  const { description, ...rest } = item
  return { items: [rest], ...(description !== undefined ? { note: description } : {}) }
}

/** One native task record as a to-do item. Null when it states no subject. */
function lettaTaskItem(task: unknown, index: number): TodoItem | null {
  if (!isObject(task))
    return null
  const content = pickString(task, 'subject')
  if (!content)
    return null
  const id = pickString(task, 'taskId') || undefined
  const description = pickString(task, 'description')
  return {
    ...(id !== undefined ? { id } : {}),
    rowKey: todoRowKey(id, index, content),
    content,
    status: normalizeTodoStatus(task.status),
    activeForm: pickString(task, 'activeForm'),
    ...(description ? { description } : {}),
  }
}

/** The native answer as an object. Letta serializes each task answer as JSON text. */
function lettaAnswerRecord(answer: unknown): Record<string, unknown> | null {
  if (isObject(answer))
    return answer
  if (typeof answer !== 'string')
    return null
  try {
    const parsed: unknown = JSON.parse(answer)
    return isObject(parsed) ? parsed : null
  }
  catch {
    return null
  }
}
