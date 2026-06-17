import { createPerAgentStore } from './chatPerAgentStore'

// ---------------------------------------------------------------------------
// Streaming-text slice
//
// The assistant's in-progress response text per agent -- it accumulates between
// tool-span persists and clears when the turn's text is folded into a message.
// A self-contained sub-store: no coupling to the windowing invariants, so it
// owns its own reactive slice (over the shared per-agent spine) rather than the
// window store's.
// ---------------------------------------------------------------------------

export function createStreamingTextStore() {
  const base = createPerAgentStore<string>('')
  return {
    /** The current streaming buffer for an agent ('' when none). */
    get: base.get,
    set: base.set,
    clear(agentId: string) {
      // Skip the write when the buffer is already empty so reactive consumers
      // aren't woken on every tool-span persist (the typical case). base.get
      // returns '' for an unset agent, so this also covers never-streamed agents.
      if (!base.get(agentId))
        return
      base.set(agentId, '')
    },
    /** Drop the agent's streaming buffer entirely (agent close). */
    remove: base.remove,
  }
}
