import type { TodoItem as ProtoTodoItem } from '~/generated/leapmux/v1/agent_pb'
import { TodoStatus } from '~/generated/leapmux/v1/agent_pb'

// ---------------------------------------------------------------------------
// Provider-neutral to-do list model + conversions
//
// The store-shape TodoItem and the helpers that normalize the various provider
// wire forms (Claude TodoWrite/Task*, Codex turn/plan, ACP sessionUpdate=plan)
// into it. A leaf module -- it imports only the generated proto types -- so the
// chat store, the sidebar, and the provider extractors share one to-do shape
// without routing the conversions through the window store.
// ---------------------------------------------------------------------------

export interface TodoItem {
  /**
   * Stable identifier for incremental providers (Claude TaskCreate /
   * TaskUpdate / TaskGet target rows by this). Snapshot-only providers
   * (TodoWrite, Codex turn/plan/updated, ACP sessionUpdate=plan) leave
   * this undefined.
   */
  id?: string
  content: string
  status: 'pending' | 'in_progress' | 'completed' | 'deleted'
  activeForm: string
  /** Long-form description from Claude Task* tools; absent elsewhere. */
  description?: string
}

/**
 * Normalize a raw todo `status` value into the canonical TodoItem status.
 * Accepts the snake_case wire form used by Claude/ACP (`'in_progress'`) and
 * the camelCase form emitted by Codex (`'inProgress'`); anything else falls
 * through to `'pending'`.
 */
export function normalizeTodoStatus(raw: unknown): TodoItem['status'] {
  if (raw === 'completed')
    return 'completed'
  if (raw === 'in_progress' || raw === 'inProgress')
    return 'in_progress'
  if (raw === 'deleted')
    return 'deleted'
  return 'pending'
}

/**
 * A todo is in a terminal state — eligible for cap-eviction on the
 *  backend and for strike-through styling in the UI.
 */
export function isTerminalTodoStatus(status: TodoItem['status']): boolean {
  return status === 'completed' || status === 'deleted'
}

// Display order rank for a todo status. Lower sorts first.
function todoStatusRank(status: TodoItem['status']): number {
  switch (status) {
    case 'in_progress':
      return 0
    case 'pending':
      return 1
    case 'completed':
      return 2
    default:
      // deleted: dropped from the plan entirely, so it sorts below what was
      // actually finished.
      return 3
  }
}

/**
 * Order todos for display: what is being worked on, then what is left, then
 * what is finished, then what was dropped. Returns a NEW array.
 *
 * Stable, so within one group the list keeps the order it arrived in -- the
 * store holds todos in the agent's own seq order, which is creation order, so
 * that means oldest first.
 */
export function sortTodos(todos: TodoItem[]): TodoItem[] {
  return todos.toSorted((a, b) => todoStatusRank(a.status) - todoStatusRank(b.status))
}

/**
 * Pick the visible label for a todo: the present-continuous `activeForm`
 *  while in_progress (when set), the imperative `content` otherwise.
 */
export function todoDisplayLabel(todo: { status: TodoItem['status'], content: string, activeForm?: string }): string {
  if (todo.status === 'in_progress' && todo.activeForm)
    return todo.activeForm
  return todo.content
}

/**
 * Convert a server-authoritative proto TodoItem (delivered via
 * ListAgentMessages or AgentTodosChanged) into the store shape. Maps
 * the proto TodoStatus enum to the canonical string union.
 */
export function protoTodoToStore(t: ProtoTodoItem): TodoItem {
  let status: TodoItem['status'] = 'pending'
  if (t.status === TodoStatus.IN_PROGRESS)
    status = 'in_progress'
  else if (t.status === TodoStatus.COMPLETED)
    status = 'completed'
  else if (t.status === TodoStatus.DELETED)
    status = 'deleted'
  return {
    id: t.id || undefined,
    content: t.content,
    status,
    activeForm: t.activeForm,
    description: t.description || undefined,
  }
}

/**
 * Coerce a raw `todos[]` array (Claude TodoWrite input or messageParser
 * extraction) into typed TodoItems. Returns an empty array for non-array
 * input.
 */
export function rawTodosToItems(raw: unknown): TodoItem[] {
  if (!Array.isArray(raw))
    return []
  return raw.map((t: Record<string, unknown>) => ({
    content: String(t.content || ''),
    status: normalizeTodoStatus(t.status),
    activeForm: String(t.activeForm || ''),
  }))
}

/**
 * Count the non-deleted todos and the completed ones, returning `{done, total}`
 * for the rail badge / ThinkingIndicator todos chip. Deleted todos are excluded
 * from both counts (a deleted row is not work-done and must not inflate total).
 */
export function todoProgress(todos: TodoItem[]): { done: number, total: number } {
  let done = 0
  let total = 0
  for (const t of todos) {
    if (t.status === 'deleted')
      continue
    total++
    if (t.status === 'completed')
      done++
  }
  return { done, total }
}
