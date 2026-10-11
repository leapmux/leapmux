import { createStore, produce } from 'solid-js/store'
import { AgentActivityState } from '~/generated/proto/leapmux/v1/agent_pb'

/**
 * Store the activity level that the worker publishes for each agent.
 * The worker derives that level. This store reads no transcript or provider state.
 *
 * Use the published level for the display and its settle baseline.
 * The worker normally delays a settle publication for three seconds.
 * Its exact activity value bypasses that delay.
 * Using the exact value can hide the spinner before the delayed publication.
 * A resumed turn can then produce no new publication, so the spinner stays absent throughout that turn.
 *
 * Four writers supply the published level:
 * - AgentInfo hydration supplies an initial level, including for NOTIFY tabs.
 * - CatchUpStart supplies the FULL replay baseline before its message burst.
 * - AgentActivityChanged supplies each live publication.
 * - createTabBusyProbe supplies the published level from its current AgentInfo reply.
 *
 * Keep these values in memory. Each load asks the worker for its current published level.
 * A stopped process owns no turn. A restart does not resume a turn.
 * A stored value can display stale WORKING before the next worker reply.
 */
interface AgentActivityStoreState {
  /**
   * Keep one map for the published level and its settle baseline.
   * A seed changes both together, so the next live publication compares against the displayed level.
   */
  stateByAgent: Record<string, AgentActivityState>
}

/** Use IDLE until the worker supplies a published level. */
const DEFAULT_ACTIVITY_STATE = AgentActivityState.IDLE

/**
 * Decide whether closing the agent would interrupt its turn.
 * WORKING and WAITING_FOR_USER both require that protection.
 * WAITING_FOR_USER stops the spinner while its prompt still blocks the turn.
 * Closing the agent stops its turn and every active background task under it.
 *
 * The close probe applies this rule to the worker's exact activity value.
 * The display can retain WORKING during the worker's three second settle delay.
 * Reading that delayed level could require confirmation after the work already ended.
 * See createTabBusyProbe and AgentActivity.InterruptsWork.
 */
export function activityInterruptsWork(state: AgentActivityState): boolean {
  return state === AgentActivityState.WORKING || state === AgentActivityState.WAITING_FOR_USER
}

/** Accept only the three activity levels that the worker publishes. */
export function isPublishedActivityState(state: AgentActivityState): boolean {
  return state === AgentActivityState.IDLE
    || state === AgentActivityState.WORKING
    || state === AgentActivityState.WAITING_FOR_USER
}

export function createAgentActivityStore() {
  const [state, setState] = createStore<AgentActivityStoreState>({ stateByAgent: {} })

  const stateOf = (agentId: string): AgentActivityState => state.stateByAgent[agentId] ?? DEFAULT_ACTIVITY_STATE

  return {
    /**
     * Show the spinner only for WORKING.
     * An unreported agent uses the IDLE default.
     * WAITING_FOR_USER leaves the prompt visible without a spinner.
     */
    isBusy(agentId: string): boolean {
      return stateOf(agentId) === AgentActivityState.WORKING
    },

    /**
     * Return the last published level, or IDLE before the first report.
     * The Interrupt control distinguishes a WAITING_FOR_USER turn from an IDLE background prompt through this value.
     */
    publishedState(agentId: string): AgentActivityState {
      return stateOf(agentId)
    },

    /**
     * Apply a valid published level and report a transition out of WORKING.
     * The alert uses this return value.
     * An initial IDLE report or a repeated level reports no settle.
     * WAITING_FOR_USER reports a settle because the turn now requires the user's action.
     * An invalid level preserves the display and its settle baseline.
     */
    apply(agentId: string, next: AgentActivityState): boolean {
      if (!isPublishedActivityState(next))
        return false
      const settled = stateOf(agentId) === AgentActivityState.WORKING && next !== AgentActivityState.WORKING
      if (state.stateByAgent[agentId] !== next)
        setState('stateByAgent', agentId, next)
      return settled
    },

    /**
     * Restore a valid published level without reporting a settle.
     * Three sources share this write path:
     * - CatchUpStart.
     * - AgentInfo hydration.
     * - createTabBusyProbe.
     * The seed changes the display and its settle baseline together.
     * A WORKING seed preserves the later settle that the worker delays.
     * An IDLE seed clears a stale WORKING baseline from a lost connection.
     * An invalid level, including UNSPECIFIED, leaves the current level unchanged.
     */
    seedPublished(agentId: string, next: AgentActivityState): void {
      if (!isPublishedActivityState(next))
        return
      if (state.stateByAgent[agentId] !== next)
        setState('stateByAgent', agentId, next)
    },

    /** Remove the level and its settle baseline when the tab retires or its worker disconnects. */
    forget(agentId: string) {
      setState(produce((s) => {
        delete s.stateByAgent[agentId]
      }))
    },

  }
}

export type AgentActivityStore = ReturnType<typeof createAgentActivityStore>
