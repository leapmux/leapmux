import type { BackgroundTaskItem } from './chatBackgroundTasks'
import type { BackgroundTaskItem as ProtoBackgroundTaskItem } from '~/generated/leapmux/v1/agent_pb'
import { shallowEqualArraysDeep } from '~/lib/shallowEqual'
import { protoBackgroundTaskToStore } from './chatBackgroundTasks'
import { createPerAgentStore } from './chatPerAgentStore'

// ---------------------------------------------------------------------------
// Background-task registry slice
//
// A byte-for-byte structural mirror of createTodoStore: a per-agent (keyed by
// ROOT owner agent id) store over BackgroundTaskItem[], with a replace() that
// converts proto-shape items in one place and skips a structurally identical
// re-broadcast so reactive consumers (sidebar, badges) don't re-run on
// identical content.
// ---------------------------------------------------------------------------

export function createBackgroundTaskStore() {
  const base = createPerAgentStore<BackgroundTaskItem[]>([])
  return {
    get: base.get,
    clear: base.clear,
    remove: base.remove,
    /**
     * Replace the agent's background-task registry with the server-
     * authoritative value. Converts proto-shape items to the store shape in
     * one place; a structurally identical re-broadcast (a no-op status
     * refresh) is skipped so reactive consumers (sidebar list, badges) don't
     * re-run on identical content. A first set (no prior list) always goes
     * through -- byAgent[agentId] is undefined, not the empty array `get`
     * would report.
     */
    replace(agentId: string, protoTasks: ProtoBackgroundTaskItem[]) {
      const next = protoTasks.map(protoBackgroundTaskToStore)
      const prev = base.byAgent[agentId]
      if (prev && shallowEqualArraysDeep(prev, next))
        return
      base.set(agentId, next)
    },
  }
}
