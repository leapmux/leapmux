import type { JsonValue } from '@bufbuild/protobuf'
import type { TodoItem as ProtoTodoItem } from '~/generated/proto/leapmux/v1/agent_pb'
import { fromJson } from '@bufbuild/protobuf'
import { TodoItemSchema, TodoStatus } from '~/generated/proto/leapmux/v1/agent_pb'

// ---------------------------------------------------------------------------
// The provider-neutral to-do list model.
//
// This module owns domain data, protobuf conversion, and semantic calculations.
// The chat normalizer owns provider wire conversion. The to-do component owns
// display ordering and labels.
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

/** Convert one protobuf to-do into the provider-neutral model. */
export function protoTodoToItem(todo: ProtoTodoItem, index: number): TodoItem {
  let status: TodoItem['status'] = 'pending'
  if (todo.status === TodoStatus.IN_PROGRESS)
    status = 'in_progress'
  else if (todo.status === TodoStatus.COMPLETED)
    status = 'completed'
  else if (todo.status === TodoStatus.DELETED)
    status = 'deleted'
  const id = todo.id || undefined
  const description = todo.description || undefined
  return {
    ...(id !== undefined ? { id } : {}),
    rowKey: todoRowKey(id, index, todo.content),
    content: todo.content,
    status,
    activeForm: todo.activeForm,
    ...(description !== undefined ? { description } : {}),
  }
}

/** Decode one protobuf JSON to-do into the provider-neutral model. */
export function protoJsonTodoToItem(value: unknown): TodoItem | null {
  if (!isJsonValue(value))
    return null
  try {
    return protoTodoToItem(fromJson(TodoItemSchema, value), 0)
  }
  catch {
    return null
  }
}

function isJsonValue(value: unknown): value is JsonValue {
  if (value === null || typeof value === 'string' || typeof value === 'boolean' || typeof value === 'number')
    return true
  if (Array.isArray(value))
    return value.every(isJsonValue)
  if (typeof value !== 'object')
    return false
  return Object.values(value).every(isJsonValue)
}

/**
 * A to-do reached a final state — eligible for cap-eviction on the backend and
 * for strike-through styling in the UI.
 */
export function isFinishedTodoStatus(status: TodoItem['status']): boolean {
  return status === 'completed' || status === 'deleted'
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
