import type { ToolCallSpecVariant } from '../../../model/toolCall'
import type { ToolRequestByKind } from '../../../model/tools'
import type { ClaudeRowContext, ClaudeToolRow } from './toolCommon'
import type { TodoItem } from '~/models/todo'
import { rawTodosToItems } from '~/components/chat/normalizers/todo'
import { createLogger } from '~/lib/logger'
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
  return claudeTodoItems(Array.isArray(result?.newTodos) ? { todos: result.newTodos } : input)
}

/**
 * The to-do request of one Claude call: the list `TodoWrite` asks to save, or the
 * SINGLE task a `Task*` call acts on.
 *
 * The `Task*` half reads the paired RESULT, which no other Claude request does, and
 * `CLAUDE_TOOL_REQUEST_OVERRIDES` supplies it for that reason. The three tools each
 * answer from a different half of the span -- `TaskGet` sends an id alone -- so the task
 * a reader sees exists only once the answer lands. The result row of all three is
 * hidden, so the REQUEST row is what draws it.
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
  const item = claudeTaskItem(toolName, input, result?.toolUseResult ?? context.pairedResult, context.todoSnapshot)
  if (toolName === CLAUDE_TOOL_NAMES.TASK_UPDATE && !item && context.todoSnapshotDiagnostic) {
    const taskId = typeof input.taskId === 'string' ? input.taskId : ''
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
    ...(description !== undefined ? { note: description } : {}),
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
  return { kind: 'todo', request, result: { items: saved } }
}

/**
 * Whether a `TaskGet` row states no task at all yet.
 *
 * `TaskGet` carries no input of its own -- it asks for a task by id, and the task
 * itself arrives in the result -- so an unresolved one has nothing to draw. Its empty
 * checklist drew the to-do body's own "To-do list cleared" under a bare "Task" header,
 * which is a sentence about a list the call never touched. The row waits instead.
 *
 * `TaskCreate` and `TaskUpdate` DO carry input, so each of them draws while it runs.
 */
export function claudeTaskGetUnresolved(args: ClaudeToolRow, result: ClaudeToolRow | undefined, context: ClaudeRowContext): boolean {
  return args.toolName === CLAUDE_TOOL_NAMES.TASK_GET && !result && !context.pairedResult
}

/**
 * The todo pair of a `Task*` call: ONE item, because the call acts on one task.
 *
 * A `Task*` result row is HIDDEN, so the request row draws the item: when the
 * paired result has landed, its task rides the result slot of this one row.
 */
export function claudeTaskTodoSpec(request: ToolRequestByKind['todo'], result: ClaudeToolRow | undefined, context: ClaudeRowContext, title: string, toolName: string): ToolCallSpecVariant<'todo'> {
  if (toolName === CLAUDE_TOOL_NAMES.TASK_UPDATE && context.todoSnapshotDiagnostic) {
    return {
      kind: 'todo',
      request,
      title,
      statusOverride: 'completed',
      result: { unparsed: true, text: context.todoSnapshotDiagnostic },
    }
  }
  // Nothing to draw: the call states no task yet, or no answer has landed beside it.
  // An empty list is what {@link claudeTodoRequest} answers for the first of those.
  if (request.items.length === 0 || (!result && !context.pairedResult))
    return { kind: 'todo', request, title }
  const failure = claudeToolFailureResult(result)
  if (failure)
    return { kind: 'todo', request, title, result: failure }
  // The paired result may draw no row of its own, so THIS row carries its item.
  // Without a result the call still read the task back, which is what `completed`
  // states here.
  return {
    kind: 'todo',
    request,
    title,
    ...(result ? {} : { statusOverride: 'completed' }),
    result: {
      items: request.items,
      ...(request.note !== undefined ? { note: request.note } : {}),
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
