import type { JsonValue } from '@bufbuild/protobuf'
import type { TodoItem as ProtoTodoItem } from '~/generated/proto/leapmux/v1/agent_pb'
import type { TodoItem } from '~/models/todo'
import { fromJson } from '@bufbuild/protobuf'
import { TodoItemSchema, TodoStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { shallowEqualArraysDeep } from '~/lib/shallowEqual'
import { todoRowKey } from '~/models/todo'
import { createPerAgentListStore } from './chatPerAgentStore'

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

// ---------------------------------------------------------------------------
// To-do list slice
//
// The latest server-authoritative to-do list per agent (delivered via the
// cold-start ListAgentMessages page and AgentTodosChanged broadcasts). Wraps the
// provider-neutral to-do model in a reactive slice; independent of the
// windowing invariants.
// ---------------------------------------------------------------------------

export function createTodoStore() {
  // A LIST store: the rows are rendered as rows, so they reconcile by key. A
  // plain replace handed `<For>` all-new objects on every broadcast, which tore
  // down and rebuilt every row -- closing the tooltip under the pointer and
  // restarting the in-progress row's animation, exactly as the background-task
  // rows did before `setReconciled`.
  const base = createPerAgentListStore<TodoItem>('rowKey')
  return {
    get: base.get,
    /** Lookup a todo by id within an agent's list, or undefined if none matches. */
    getById(agentId: string, taskId: string): TodoItem | undefined {
      return base.get(agentId).find(t => t.id === taskId)
    },
    clear: base.clear,
    /** Drop the agent's to-do list entirely (agent close). */
    remove: base.remove,
    /**
     * Replace the agent's to-do list with the server-authoritative value.
     * Converts proto-shape items to the store shape in one place; a structurally
     * identical re-broadcast (KindDetail / no-op patch) is skipped so reactive
     * consumers (sidebar list, badges) don't re-run on identical content. A first
     * set (no prior list) always goes through -- byAgent[agentId] is undefined,
     * not the empty array `get` would report.
     */
    replace(agentId: string, protoTodos: ProtoTodoItem[]) {
      const next = protoTodos.map(protoTodoToItem)
      const prev = base.byAgent[agentId]
      if (prev && shallowEqualArraysDeep(prev, next))
        return
      base.setReconciled(agentId, next)
    },
  }
}
