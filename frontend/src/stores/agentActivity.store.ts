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
 * - hydration, from `AgentInfo.activity_state` on a list read. The only path that reaches a
 *   tab watching in NOTIFY mode, which gets no catch-up replay at all.
 * - catch-up replay, on the transition into FULL, so a tab renders the right
 *   spinner BEFORE the message burst rather than after it.
 * - the live `AgentActivityChanged` event.
 *
 * NOT persisted, because there is nothing worth persisting. Every load asks the
 * Worker (`AgentInfo.activity_state`), and every process boundary resets the answer: an
 * agent whose process was terminated owns no turn, and the Worker says so the
 * moment a new one takes over. A value written to disk could therefore only be
 * stale -- right by luck, or a spinner shown for the gap before the first
 * hydration reply corrects it.
 *
 * Nothing here restores state either. A restart does not resume a turn.
 */
interface AgentActivityStoreState {
  stateByAgent: Record<string, AgentActivityState>
  /**
   * The last state the WORKER pushed as a transition, which is the baseline the
   * settle edge is measured against.
   *
   * Separate from `stateByAgent` because a LEVEL also writes that one, and a
   * level must not consume an edge. The Worker debounces a settle for three
   * seconds. A level read in between, such as a
   * list hydration, can therefore carry the
   * settled value while the transition
   * announcing it is still on its way. Measured against the display state, that transition then moves
   * nothing and the completion sound never rings.
   */
  pushedByAgent: Record<string, AgentActivityState>
}

/** An agent nothing has reported on yet. */
const UNKNOWN = AgentActivityState.IDLE

/**
 * Whether closing a tab in this state would stop something.
 *
 * It differs from "busy" for an agent blocked on a permission prompt: its turn
 * is still in flight, and the close kills it along with every background task
 * under it. The Worker spells the same rule as AgentActivity.InterruptsWork.
 *
 * A free function, not a store method, because the close guard must NOT read
 * this store. The Worker debounces a settle for three seconds, so the pushed
 * state says "busy" for that long after the work finished, and a guard reading
 * it would refuse a close the CLI already allows. The guard fetches the exact
 * state and applies this rule to it -- see createTabBusyProbe.
 */
export function activityInterruptsWork(state: AgentActivityState): boolean {
  return state === AgentActivityState.WORKING || state === AgentActivityState.WAITING_FOR_USER
}

export function createAgentActivityStore() {
  const [state, setState] = createStore<AgentActivityStoreState>({ stateByAgent: {}, pushedByAgent: {} })

  const stateOf = (agentId: string): AgentActivityState => state.stateByAgent[agentId] ?? UNKNOWN
  const pushedFor = (agentId: string): AgentActivityState => state.pushedByAgent[agentId] ?? UNKNOWN

  return {
    /**
     * Whether the agent is working, which is what the thinking indicator and the
     * Interrupt button read. An agent nothing has reported on yet is not
     * working: a spinner that appears before any evidence is worse than one that
     * appears a beat late.
     *
     * WAITING_FOR_USER answers false here on purpose. The user is looking
     * straight at the permission prompt, so spinning an indicator at them says
     * nothing -- but see activityInterruptsWork, which the close guard applies
     * to the exact state it fetches instead of to this debounced one.
     */
    isBusy(agentId: string): boolean {
      return stateOf(agentId) === AgentActivityState.WORKING
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
     * The edge is measured against `pushedByAgent`, the last TRANSITION, and not
     * against what is on screen. A level can move the screen without moving that
     * baseline -- see seed.
     *
     * A move into WAITING_FOR_USER is a settle: the turn stops making progress
     * and the user is the one who must act, which is the alert that used to ride
     * on busy -> false.
     */
    apply(agentId: string, next: AgentActivityState): boolean {
      const settled = pushedFor(agentId) === AgentActivityState.WORKING && next !== AgentActivityState.WORKING
      if (state.stateByAgent[agentId] !== next)
        setState('stateByAgent', agentId, next)
      if (state.pushedByAgent[agentId] !== next)
        setState('pushedByAgent', agentId, next)
      return settled
    },

    /**
     * Seed from the level the Worker PUBLISHED -- the catch-up baseline on
     * CatchUpStart -- and raise nothing.
     *
     * A level says what the agent is doing right now. It is not news the user
     * asked for. An agent that
     * settled while this client
     * was away did not finish
     * in front of them, and
     * ringing for each one on
     * every reconnect is the
     * burst this split
     * prevents. So this returns nothing, rather than an edge each
     * caller has to remember to discard.
     *
     * It writes the edge baseline too, because this value IS that baseline. The
     * Worker sends what it last broadcast, so a settle still waiting out its
     * window reads WORKING here and the transition announcing it still rings.
     *
     * Writing it also CLEARS a baseline this client can no longer stand behind:
     * a WORKING left over from a link that dropped with no offline sweep. The
     * next transition is then measured against the Worker's own answer, and not
     * against a value from a session that ended.
     *
     * UNSPECIFIED is "no opinion", not a state: a worker that sends one must not
     * overwrite an answer this client already holds.
     */
    seedPublished(agentId: string, next: AgentActivityState): void {
      if (next === AgentActivityState.UNSPECIFIED)
        return
      if (state.stateByAgent[agentId] !== next)
        setState('stateByAgent', agentId, next)
      if (state.pushedByAgent[agentId] !== next)
        setState('pushedByAgent', agentId, next)
    },

    /**
     * Seed from the EXACT derivation -- AgentInfo on a list read -- and raise
     * nothing.
     *
     * It may ARM the settle edge and may never SPEND it, which is what tells it
     * apart from seedPublished. This value ignores the Worker's debounce window,
     * so a read taken inside that window already carries the settled state while
     * the transition announcing it is still on its way. Writing that to the
     * baseline would make the transition compare equal, and the settle would ring
     * for nobody. A level that says WORKING is safe to write, and arms the edge
     * for a settle that lands moments later.
     *
     * UNSPECIFIED is "no opinion", not a state.
     */
    seedSnapshot(agentId: string, next: AgentActivityState): void {
      if (next === AgentActivityState.UNSPECIFIED)
        return
      if (state.stateByAgent[agentId] !== next)
        setState('stateByAgent', agentId, next)
      if (next === AgentActivityState.WORKING && state.pushedByAgent[agentId] !== next)
        setState('pushedByAgent', agentId, next)
    },

    /**
     * Drop one agent's state. Called when a tab retires and when the worker
     * goes offline, so the map holds only agents a tab still shows.
     */
    forget(agentId: string) {
      setState(produce((s) => {
        delete s.stateByAgent[agentId]
        delete s.pushedByAgent[agentId]
      }))
    },

  }
}

export type AgentActivityStore = ReturnType<typeof createAgentActivityStore>
