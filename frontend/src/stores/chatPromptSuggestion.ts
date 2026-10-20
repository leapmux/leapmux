import { createPerAgentValueStore } from './chatPerAgentStore'

/** The next message that an agent suggests after a turn. */
export interface PromptSuggestion { text: string }

/** Keep suggestions in memory. The worker replays the current value after a reconnect. */
export function createPromptSuggestionStore() {
  const state = createPerAgentValueStore<PromptSuggestion>()
  return {
    set(agentId: string, value: PromptSuggestion) {
      state.setReconciled(agentId, { ...value })
    },
    get(agentId: string): PromptSuggestion | undefined {
      return state.get(agentId)
    },
    clear(agentId: string) {
      state.clear(agentId)
    },
  }
}
