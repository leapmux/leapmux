import { createStore, produce } from 'solid-js/store'

/**
 * Whether each agent is working, as the WORKER derives it.
 *
 * This replaced a client-side derivation that assembled the answer on every
 * render from six inputs -- a backwards scan of the transcript, live streaming
 * text, the Codex turn id smuggled through ephemeral session info, the
 * background-task registry, pending control requests and agent status. The
 * worker owns all of those, and four of its five provider families already kept
 * a turn flag privately, so the heuristic existed only because nothing published
 * it. Now one boolean arrives on the wire and this store holds it.
 *
 * Three writers, matching how background tasks are already fed:
 *
 * - hydration, from `AgentInfo.busy` on a list read. The only leg that reaches a
 *   tab watching in NOTIFY mode, which gets no catch-up replay at all.
 * - catch-up replay, on the transition into FULL, so a tab renders the right
 *   spinner BEFORE the message burst rather than after it.
 * - the live `AgentActivityChanged` event.
 *
 * NOT persisted, because there is nothing worth persisting. Every load asks the
 * Worker (`AgentInfo.busy`), and every process boundary resets the answer: an
 * agent whose process was terminated owns no turn, and the Worker says so the
 * moment a new one takes over. A value written to disk could therefore only be
 * stale -- right by luck, or a spinner shown for the gap before the first
 * hydration reply corrects it.
 *
 * Nothing here restores state either. A restart does not resume a turn.
 */
interface AgentActivityState {
  busyByAgent: Record<string, boolean>
}

export function createAgentActivityStore() {
  const [state, setState] = createStore<AgentActivityState>({ busyByAgent: {} })

  return {
    /**
     * Whether the agent is working. An agent nothing has reported on yet is not
     * busy: a spinner that appears before any evidence is worse than one that
     * appears a beat late.
     */
    isBusy(agentId: string): boolean {
      return state.busyByAgent[agentId] === true
    },

    /**
     * Apply the worker's answer. Returns whether this write was the busy -> idle
     * EDGE -- the settle the turn-end alert rings on.
     *
     * The edge, not the arrival of an idle report, and not merely a changed
     * value. The worker broadcasts on transition, but the same value still
     * reaches a client twice (a catch-up replay landing beside a live event, a
     * subprocess teardown dropping the worker's "already published" mark), and
     * an idle report can arrive for an agent this client never saw working (a
     * NOTIFY-mode tab that subscribed mid-turn). Ringing on either announces a
     * settle the user never saw start, or announces one settle twice.
     */
    setBusy(agentId: string, busy: boolean): boolean {
      const settled = state.busyByAgent[agentId] === true && !busy
      if (state.busyByAgent[agentId] !== busy)
        setState('busyByAgent', agentId, busy)
      return settled
    },

    /** Drop one agent's state, on tab close. */
    forget(agentId: string) {
      setState(produce((s) => {
        delete s.busyByAgent[agentId]
      }))
    },

  }
}

export type AgentActivityStore = ReturnType<typeof createAgentActivityStore>
