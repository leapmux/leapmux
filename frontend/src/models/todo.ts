import type { TodoItem as ProtoTodoItem } from '~/generated/proto/leapmux/v1/agent_pb'
import { TodoStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'

// ---------------------------------------------------------------------------
// The provider-neutral to-do list model, and the conversions onto it.
//
// A MODEL, not a store: the shape and the helpers that normalize the provider
// wire forms (Claude TodoWrite/Task*, Codex turn/plan, ACP sessionUpdate=plan)
// onto it. Three layers read them -- the chat store, the sidebar, and ten
// provider extractors -- and none of them is a store, so a store module is the
// wrong home. It lived in `stores/chatTodos.ts`, which made a provider
// extractor and the IR both import from the store layer for a type neither
// layer owns.
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
 *
 * A CANCELLED task reads as `deleted`, which is the same end state: the row stays
 * visible and stops being work. Cursor and OpenCode both spell it `cancelled`, and
 * the American spelling is here because the word travels as prose and no protocol
 * fixes it. The worker's own parser (`todoevents.StatusFromProviderWord`) reads the
 * same two words onto `StatusDeleted`, so the sidebar and the row agree.
 */
export function normalizeTodoStatus(raw: unknown): TodoItem['status'] {
  if (raw === 'completed')
    return 'completed'
  if (raw === 'in_progress' || raw === 'inProgress')
    return 'in_progress'
  if (raw === 'deleted' || raw === 'cancelled' || raw === 'canceled')
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
 * ListAgentMessages or AgentTodosChanged) into the model shape. Maps
 * the proto TodoStatus enum to the canonical string union.
 *
 * `index` is the row's position in the list, which {@link todoRowKey} needs for
 * a provider that sends no id.
 */
export function protoTodoToItem(t: ProtoTodoItem, index: number): TodoItem {
  let status: TodoItem['status'] = 'pending'
  if (t.status === TodoStatus.IN_PROGRESS)
    status = 'in_progress'
  else if (t.status === TodoStatus.COMPLETED)
    status = 'completed'
  else if (t.status === TodoStatus.DELETED)
    status = 'deleted'
  const id = t.id || undefined
  const description = t.description || undefined
  return {
    ...(id !== undefined ? { id } : {}),
    rowKey: todoRowKey(id, index, t.content),
    content: t.content,
    status,
    activeForm: t.activeForm,
    ...(description !== undefined ? { description } : {}),
  }
}

/**
 * One text field of a to-do row, read from an untyped provider wire value.
 *
 * Seven call sites feed `rawTodosToItems` a provider's own to-do array, so a
 * `content` that is an object or an array reaches here. `String()` turned one into
 * the literal "[object Object]", which the checklist and the Background-tasks rail
 * then drew verbatim.
 *
 * A PRIMITIVE keeps its text, because a provider that states a numeric step
 * ("0", "1") means that digit, and a plain `pickString` would erase it. Only the
 * shapes that stringify into a description of themselves become the empty string,
 * which states the truth: this build read no text for the row.
 */
function todoText(value: unknown): string {
  if (value === undefined || value === null)
    return ''
  if (typeof value === 'object' || typeof value === 'function')
    return ''
  return String(value)
}

/**
 * Coerce a raw `todos[]` array (Claude TodoWrite input or messageParser
 * extraction) into typed TodoItems. Returns an empty array for non-array
 * input.
 */
export function rawTodosToItems(raw: unknown): TodoItem[] {
  if (!Array.isArray(raw))
    return []
  return raw.flatMap((t, i) => {
    if (!isObject(t))
      return []
    const content = todoText(t.content)
    return [{
      rowKey: todoRowKey(undefined, i, content),
      content,
      status: normalizeTodoStatus(t.status),
      activeForm: todoText(t.activeForm),
    }]
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
