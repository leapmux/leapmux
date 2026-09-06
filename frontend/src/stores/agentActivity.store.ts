import { createStore, produce } from 'solid-js/store'
import { AgentActivityState } from '~/generated/proto/leapmux/v1/agent_pb'

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
 * - hydration, from `AgentInfo.busy` on a list read. The only path that reaches a
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
interface AgentActivityStoreState {
  stateByAgent: Record<string, AgentActivityState>
}

/** An agent nothing has reported on yet. */
const UNKNOWN = AgentActivityState.IDLE

export function createAgentActivityStore() {
  const [state, setState] = createStore<AgentActivityStoreState>({ stateByAgent: {} })

  const stateOf = (agentId: string): AgentActivityState => state.stateByAgent[agentId] ?? UNKNOWN

  return {
    /**
     * Whether the agent is working, which is what the thinking indicator and the
     * Interrupt button read. An agent nothing has reported on yet is not
     * working: a spinner that appears before any evidence is worse than one that
     * appears a beat late.
     *
     * WAITING_FOR_USER answers false here on purpose. The user is looking
     * straight at the permission prompt, so spinning an indicator at them says
     * nothing -- but see interruptsWork, which the close guard reads instead.
     */
    isBusy(agentId: string): boolean {
      return stateOf(agentId) === AgentActivityState.WORKING
    },

    /**
     * Whether closing this tab would stop something. Differs from isBusy for an
     * agent blocked on a permission prompt: its turn is still in flight, and the
     * close kills it along with every background task under it.
     */
    interruptsWork(agentId: string): boolean {
      const current = stateOf(agentId)
      return current === AgentActivityState.WORKING || current === AgentActivityState.WAITING_FOR_USER
    },

    /**
     * Apply the worker's answer. Returns whether this write was the SETTLE edge
     * -- working -> not working, which the turn-end alert rings on.
     *
     * The edge, not the arrival of an idle report, and not merely a changed
     * value. The worker broadcasts on transition, but the same value still
     * reaches a client twice (a catch-up replay landing beside a live event, a
     * subprocess teardown dropping the worker's "already published" mark), and
     * an idle report can arrive for an agent this client never saw working (a
     * NOTIFY-mode tab that subscribed mid-turn). Ringing on either announces a
     * settle the user never saw start, or announces one settle twice.
     *
     * A move into WAITING_FOR_USER is a settle: the turn stops making progress
     * and the user is the one who must act, which is the alert that used to ride
     * on busy -> false.
     */
    apply(agentId: string, next: AgentActivityState): boolean {
      const settled = stateOf(agentId) === AgentActivityState.WORKING && next !== AgentActivityState.WORKING
      if (state.stateByAgent[agentId] !== next)
        setState('stateByAgent', agentId, next)
      return settled
    },

    /**
     * Drop one agent's state. Called when a tab retires and when the worker
     * goes offline, so the map holds only agents a tab still shows.
     */
    forget(agentId: string) {
      setState(produce((s) => {
        delete s.stateByAgent[agentId]
      }))
    },

  }
}

export type AgentActivityStore = ReturnType<typeof createAgentActivityStore>
