import { createPerAgentValueStore } from './chatPerAgentStore'

/** The compaction that currently runs for one transcript. */
export interface LiveCompaction { trigger?: string }

/** Keep live compactions outside browser storage. The worker restores them after a reconnect. */
export function createLiveCompactionStore() {
  const state = createPerAgentValueStore<LiveCompaction>()
  return {
    set(agentId: string, value: LiveCompaction) {
      state.setReconciled(agentId, { ...value })
    },
    get(agentId: string): LiveCompaction | undefined {
      return state.get(agentId)
    },
    clear(agentId: string) {
      state.clear(agentId)
    },
  }
}
