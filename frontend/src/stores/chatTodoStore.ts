import type { TodoItem } from './chatTodos'
import type { TodoItem as ProtoTodoItem } from '~/generated/leapmux/v1/agent_pb'
import { shallowEqualArraysDeep } from '~/lib/shallowEqual'
import { createPerAgentStore } from './chatPerAgentStore'
import { protoTodoToStore } from './chatTodos'

// ---------------------------------------------------------------------------
// To-do list slice
//
// The latest server-authoritative to-do list per agent (delivered via the
// cold-start ListAgentMessages page and AgentTodosChanged broadcasts). Wraps the
// provider-neutral chatTodos model in a reactive slice; independent of the
// windowing invariants.
// ---------------------------------------------------------------------------

export function createTodoStore() {
  const base = createPerAgentStore<TodoItem[]>([])
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
      const next = protoTodos.map(protoTodoToStore)
      const prev = base.byAgent[agentId]
      if (prev && shallowEqualArraysDeep(prev, next))
        return
      base.set(agentId, next)
    },
  }
}
