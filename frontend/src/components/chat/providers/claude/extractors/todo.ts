import type { ToolCallSpecVariant, ToolFailureResult } from '../../../model/toolCall'
import type { ToolRequestByKind } from '../../../model/tools'
import type { ClaudeRowContext, ClaudeToolRow } from './toolCommon'
import type { TodoItem } from '~/models/todo'
import { rawTodosToItems } from '~/components/chat/normalizers/todo'
import { createLogger } from '~/lib/logger'
import { failedResult, unparsedResult } from '../../../model/toolCall'
import { CLAUDE_TOOL_NAMES } from '../toolNames'
import { claudeToolFailureResult } from './failure'
import { buildTaskCreateItem, buildTaskGetItem, buildTaskUpdateItem } from './taskCard'

const logger = createLogger('claudeTaskUpdate')
const reportedSnapshotDiagnostics = new Set<string>()

/**
 * The checklist a Claude `TodoWrite` tool_use input carries. Null when the input
 * is not a recognizable TodoWrite payload.
 *
 * The list alone: the row's own title states the size, and a second copy beside
 * it could word it differently.
 */
export function claudeTodoItems(
  input: Record<string, unknown> | null | undefined,
): TodoItem[] | null {
  if (!input || typeof input !== 'object')
    return null
  if (!Array.isArray(input.todos))
    return null
  return rawTodosToItems(input.todos)
}

/** The result supplies the saved list. The matching request supplies omitted items. */
export function claudeTodoItemsFromResult(
  result: Record<string, unknown> | undefined,
  input: Record<string, unknown> | undefined,
): TodoItem[] | null {
  return claudeTodoItems(Array.isArray(result?.newTodos)
    ? { todos: result.newTodos }
    : input)
}

/**
 * The to-do request of one Claude call: the list `TodoWrite` asks to save, or the
 * SINGLE task a `Task*` call acts on.
 *
 * The three `Task*` tools each answer from a different half of the span -- `TaskGet`
 * sends an id alone -- so the whole task exists only once the answer lands. The RESULT
 * row reads it from its own message (`result`). The REQUEST row has no result and
 * states what its own arguments carry: the subject a `TaskCreate` asks for, and no
 * task for a `TaskUpdate` patch or a `TaskGet` id.
 *
 * One call states one of the two, never both: `claudeTaskItem` answers null for every
 * name outside the three, and no `Task*` call carries a `todos` array.
 */
export function claudeTodoRequest(
  toolName: string,
  input: Record<string, unknown>,
  result: ClaudeToolRow | undefined,
  context: ClaudeRowContext,
): ToolRequestByKind['todo'] {
  const item = claudeTaskItem(toolName, input, result?.toolUseResult, context.todoSnapshot)
  if (toolName === CLAUDE_TOOL_NAMES.TASK_UPDATE && !item && context.todoSnapshotDiagnostic && claudeTaskUpdateStoresSnapshot(result)) {
    const taskId = typeof input.taskId === 'string'
      ? input.taskId
      : ''
    const warningKey = `${taskId}:${context.todoSnapshotDiagnostic}`
    if (!reportedSnapshotDiagnostics.has(warningKey)) {
      reportedSnapshotDiagnostics.add(warningKey)
      logger.warn('Cannot render TaskUpdate snapshot', { taskId, diagnostic: context.todoSnapshotDiagnostic })
    }
  }
  if (!item)
    return { items: claudeTodoItems(input) ?? [] }
  // The description goes beside the checklist rather than on the item, so the row
  // shows it rather than hiding it in a hover.
  const { description, ...rest } = item
  return {
    items: [rest],
    ...(description !== undefined
      ? { note: description }
      : {}),
  }
}

/**
 * The todo pair of a `TodoWrite` call: the list it asked to save, and the list
 * the result reports. A failed call keeps the list it carried and states its
 * error text alone.
 */
export function claudeTodoSpec(request: ToolRequestByKind['todo'], args: ClaudeToolRow, result: ClaudeToolRow | undefined): ToolCallSpecVariant<'todo'> {
  if (!result)
    return { kind: 'todo', request }
  const failure = claudeToolFailureResult(result)
  if (failure)
    return { kind: 'todo', request, result: failure }
  const saved = claudeTodoItemsFromResult(result.toolUseResult, args.input) ?? []
  return {
    kind: 'todo',
    request,
    result: { items: saved },
  }
}

/**
 * The failure that a `Task*` answer states: the tool result marked as an error, or a
 * `tool_use_result` that reports `success: false`, in its own `error` words when it
 * states them.
 */
function claudeTaskFailureResult(result: ClaudeToolRow): ToolFailureResult | undefined {
  const marked = claudeToolFailureResult(result)
  if (marked)
    return marked
  const answer = result.toolUseResult
  if (answer?.success !== false)
    return undefined
  const error = typeof answer.error === 'string'
    ? answer.error.trim()
    : ''
  return failedResult(error || result.resultContent || 'The task tool failed.')
}

/**
 * Whether the Worker stores a to-do snapshot beside this `TaskUpdate` answer. It does for
 * an update that succeeded and states its task (`claudeTaskUpdateEvent` in the Worker),
 * so only such an answer can miss its snapshot.
 */
function claudeTaskUpdateStoresSnapshot(result: ClaudeToolRow | undefined): boolean {
  const answer = result?.toolUseResult
  return answer?.success === true && typeof answer.taskId === 'string' && answer.taskId !== ''
}

/**
 * The todo pair of a `Task*` call: ONE item, because the call acts on one task.
 *
 * The request row states the task its own arguments carry, and no result: it never
 * reads its result. The result row states the task the answer carries -- the created
 * task with its id, the snapshot after an update, the task a read returned -- as both
 * its request item and its result.
 */
export function claudeTaskTodoSpec(request: ToolRequestByKind['todo'], result: ClaudeToolRow | undefined, context: ClaudeRowContext, title: string, toolName: string): ToolCallSpecVariant<'todo'> {
  if (!result)
    return { kind: 'todo', request, title }
  // A failed call states its own error, under a failed status also when only its
  // `success: false` says so. It updated nothing, so it misses no snapshot.
  const failure = claudeTaskFailureResult(result)
  if (failure) {
    return {
      kind: 'todo',
      request,
      title,
      statusOverride: 'failed',
      result: failure,
    }
  }
  if (toolName === CLAUDE_TOOL_NAMES.TASK_UPDATE && context.todoSnapshotDiagnostic && claudeTaskUpdateStoresSnapshot(result)) {
    return {
      kind: 'todo',
      request,
      title,
      statusOverride: 'completed',
      result: { unparsed: true, text: context.todoSnapshotDiagnostic },
    }
  }
  // An answer that states no task: the result row shows the words the tool returned.
  if (request.items.length === 0) {
    return {
      kind: 'todo',
      request,
      title,
      result: unparsedResult(result.resultContent),
    }
  }
  return {
    kind: 'todo',
    request,
    title,
    result: {
      items: request.items,
      ...(request.note !== undefined
        ? { note: request.note }
        : {}),
    },
  }
}

/** The single task a `Task*` call acts on, or null when the payload states none. */
function claudeTaskItem(
  toolName: string,
  input: Record<string, unknown>,
  payload: Record<string, unknown> | undefined,
  todoSnapshot: ClaudeRowContext['todoSnapshot'],
): TodoItem | null {
  switch (toolName) {
    case CLAUDE_TOOL_NAMES.TASK_CREATE:
      return buildTaskCreateItem(input, payload)
    case CLAUDE_TOOL_NAMES.TASK_UPDATE:
      return buildTaskUpdateItem(todoSnapshot)
    case CLAUDE_TOOL_NAMES.TASK_GET:
      return buildTaskGetItem(payload)
    default:
      return null
  }
}
