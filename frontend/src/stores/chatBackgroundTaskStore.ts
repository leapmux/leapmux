import type { BackgroundTaskItem } from './chatBackgroundTasks'
import type { BackgroundTaskItem as ProtoBackgroundTaskItem } from '~/generated/proto/leapmux/v1/agent_pb'
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
//
// Plus one thing the to-do slice has no need of: whether the last LOAD FAILED.
// The registry is the only one of the two whose section is hidden when it is
// empty, so without that flag a worker-side failure and "this agent has run no
// background tasks" render identically -- and a schema-drifted database hid the
// whole section with nothing on screen to say why.
// ---------------------------------------------------------------------------

export function createBackgroundTaskStore() {
  const base = createPerAgentStore<BackgroundTaskItem[]>([])
  const failed = createPerAgentStore<boolean>(false)
  return {
    get: base.get,
    clear(agentId: string) {
      base.clear(agentId)
      failed.clear(agentId)
    },
    remove(agentId: string) {
      base.remove(agentId)
      failed.remove(agentId)
    },
    /**
     * Whether the last attempt to load this agent's registry failed.
     *
     * Set from the cold-start page's `background_tasks_loaded=false`, which the
     * worker sends for exactly one reason: the query errored. A child agent
     * reports loaded=true with an empty list, so an empty registry is never
     * mistaken for a failure.
     */
    loadFailed: failed.get,
    /**
     * Replace the agent's background-task registry with the server-
     * authoritative value. Converts proto-shape items to the store shape in
     * one place; a structurally identical re-broadcast (a no-op status
     * refresh) is skipped so reactive consumers (sidebar list, badges) don't
     * re-run on identical content. A first set (no prior list) always goes
     * through -- byAgent[agentId] is undefined, not the empty array `get`
     * would report.
     *
     * A successful load clears the failure flag, and that write happens even
     * when the rows are unchanged: a retry that returns the same (empty) list
     * is still the answer that the registry is reachable again.
     *
     * The write RECONCILES by row key rather than replacing the array, so a row
     * whose fields did not change keeps its identity and the sidebar leaves its
     * DOM alone. The broadcast that carries one subagent's new activity string
     * carries the whole registry with it, so a plain replace rebuilt every row
     * on screen: the tooltip under the pointer closed and reopened, and every
     * status dot restarted its pulse. See `PerAgentStore.setReconciled`.
     */
    replace(agentId: string, protoTasks: ProtoBackgroundTaskItem[]) {
      if (failed.get(agentId))
        failed.clear(agentId)
      const next = protoTasks.map(protoBackgroundTaskToStore)
      const prev = base.byAgent[agentId]
      if (prev && shallowEqualArraysDeep(prev, next))
        return
      base.setReconciled(agentId, next, 'rowKey')
    },
    /** Record that the worker could not answer for this agent's registry. */
    markLoadFailed(agentId: string) {
      failed.set(agentId, true)
    },
  }
}
