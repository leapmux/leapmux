import type { TodoItem as ProtoTodoItem } from '~/generated/proto/leapmux/v1/agent_pb'
import { TodoStatus } from '~/generated/proto/leapmux/v1/agent_pb'

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
  /**
   * The identity a UI reconciles this row by across a rebroadcast.
   *
   * `id` when the provider gives one. A snapshot-only provider gives none, and
   * for such a list POSITION is the identity -- the provider re-sends the whole
   * list and row 3 is row 3 -- so the key pairs the position with the content.
   * A row whose content is unchanged at its position then keeps its DOM through
   * a status change, which is the common update; a row whose content changed is
   * a different task and correctly replaces it.
   *
   * Derived, never sent: {@link todoRowKey} is its one writer.
   */
  rowKey: string
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
 * A to-do reached a final state — eligible for cap-eviction on the backend and
 * for strike-through styling in the UI.
 */
export function isFinishedTodoStatus(status: TodoItem['status']): boolean {
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
 * The reconciliation key for one to-do row; see {@link TodoItem.rowKey}.
 *
 * `index` is the row's position in the list the provider sent, which is the only
 * identity a snapshot-only provider offers.
 */
export function todoRowKey(id: string | undefined, index: number, content: string): string {
  return id || `${index}:${content}`
}

/**
 * Convert a server-authoritative proto TodoItem (delivered via
 * ListAgentMessages or AgentTodosChanged) into the store shape. Maps
 * the proto TodoStatus enum to the canonical string union.
 *
 * `index` is the row's position in the list, which {@link todoRowKey} needs for
 * a provider that sends no id.
 */
export function protoTodoToStore(t: ProtoTodoItem, index: number): TodoItem {
  let status: TodoItem['status'] = 'pending'
  if (t.status === TodoStatus.IN_PROGRESS)
    status = 'in_progress'
  else if (t.status === TodoStatus.COMPLETED)
    status = 'completed'
  else if (t.status === TodoStatus.DELETED)
    status = 'deleted'
  return {
    id: t.id || undefined,
    rowKey: todoRowKey(t.id || undefined, index, t.content),
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
  return raw.map((t: Record<string, unknown>, i: number) => {
    const content = String(t.content || '')
    return {
      rowKey: todoRowKey(undefined, i, content),
      content,
      status: normalizeTodoStatus(t.status),
      activeForm: String(t.activeForm || ''),
    }
  })
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
