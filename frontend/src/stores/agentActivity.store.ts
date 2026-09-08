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
 * Every writer carries the level the Worker PUBLISHED, and never the exact
 * derivation that rides beside it. The Worker holds a settle for three seconds
 * so the completion sound does not ring for work that resumes, and the exact
 * value ignores that window. A level written from the exact value drops the
 * spinner early; if the work then resumes, the Worker derives a state equal to
 * what it already published and broadcasts nothing, so that tab shows no spinner
 * for the rest of the turn. The exact value answers one question, and it is not
 * a level -- see activityInterruptsWork.
 *
 * Four writers. The first three match how background tasks are already fed:
 *
 * - hydration, from `AgentInfo.published_activity_state` on a list read. The
 *   only path that reaches a tab watching in NOTIFY mode, which gets no
 *   catch-up replay at all.
 * - catch-up replay, on the transition into FULL, so a tab renders the right
 *   spinner BEFORE the message burst rather than after it.
 * - the live `AgentActivityChanged` event.
 *
 * The fourth repairs rather than feeds: the close guard's `listAgents` round
 * trip already carries the Worker's own answer, so it corrects a display that
 * drifted -- a WORKING left over from a link that dropped with no offline
 * sweep. See createTabBusyProbe.
 *
 * NOT persisted, because there is nothing worth persisting. Every load asks the
 * Worker (`AgentInfo.published_activity_state`), and every process boundary
 * resets the answer: an agent whose process was terminated owns no turn, and the
 * Worker says so the moment a new one takes over. A value written to disk could
 * therefore only be stale -- right by luck, or a spinner shown for the gap
 * before the first hydration reply corrects it.
 *
 * Nothing here restores state either. A restart does not resume a turn.
 */
interface AgentActivityStoreState {
  /**
   * The level the Worker last PUBLISHED for each agent.
   *
   * One map, because the display and the settle baseline hold the same value.
   * Every writer takes what the Worker broadcast, so a seed moves the baseline
   * with the screen. That is what keeps a settle still held in the Worker's
   * window ringing when it lands, and what stops a WORKING this client can no
   * longer stand behind from ringing a settle that already happened.
   */
  stateByAgent: Record<string, AgentActivityState>
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
 * this store. The Worker debounces a settle for three seconds, so the level this
 * store holds says "busy" for that long after the work finished, and a guard
 * reading it would refuse a close the CLI already allows. The guard fetches the
 * exact state and applies this rule to it -- see createTabBusyProbe.
 */
export function activityInterruptsWork(state: AgentActivityState): boolean {
  return state === AgentActivityState.WORKING || state === AgentActivityState.WAITING_FOR_USER
}

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
     * The edge is measured against the level this store already holds, which is
     * always what the Worker last published -- see AgentActivityStoreState.
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
     * Seed from the level the Worker PUBLISHED -- the catch-up baseline on
     * CatchUpStart, or `AgentInfo.published_activity_state` on a list read --
     * and raise nothing.
     *
     * A level says what the agent is doing right now. It is not news the user
     * asked for. An agent that settled while this client was away did not finish
     * in front of them, and ringing for each one on every reconnect is the burst
     * this split prevents. So this returns nothing, rather than an edge each
     * caller has to remember to discard.
     *
     * It moves the settle baseline with the display, because this value IS that
     * baseline. The Worker sends what it last broadcast, so a settle still
     * waiting out its window reads WORKING here and the transition announcing it
     * still rings.
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
