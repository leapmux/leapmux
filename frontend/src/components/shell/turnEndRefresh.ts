/**
 * Which turn end earns a working-tree refresh, and how many refreshes a burst
 * of them costs.
 *
 * A turn end bumps ONE global trigger, and two readers watch it: the git status
 * of the active tab's repository, and the directory tree the sidebar shows.
 * Both read a single worker -- the one `getCurrentTabContext` resolves. The
 * client watches every agent that holds an open tab, in every workspace and on
 * every worker. Without a gate, a turn end on another worker costs one git RPC
 * plus one tree reload. That tree did not change.
 *
 * Three rules, and no test for "is this a subagent":
 *
 *   1. A tab that closes has nothing to refresh for. Drop it. The turn-end
 *      sound drops a closing agent's settle for the same reason.
 *   2. A different worker changes nothing here. Drop it.
 *   3. Every other turn end refreshes, at most twice per window.
 *
 * Rule 3 keeps the subagents deliberately. A subagent runs on its parent's
 * worker inside its parent's checkout, so its edits ARE in the tree the user
 * sees. The worker broadcasts a child's turn end under the CHILD's agent id, so
 * this client hears it only once the user opens that child's tab -- and from
 * then on it is the only signal the tree gets for a child the parent started in
 * the BACKGROUND: the parent's own turn ended long before, and the settle edge
 * deliberately refreshes nothing. Dropping it by lineage would leave the files
 * that subagent writes missing from the tree until the next turn.
 *
 * The window is a throttle with a trailing edge, not a debounce. The first turn
 * end refreshes IMMEDIATELY. That is the common case, and the user notices a
 * delay there. Whatever arrives while the window runs shares one more refresh
 * at its end, so a parent and five subagents cost two refreshes rather than
 * six, and the last one still sees everything the burst wrote.
 */

import { leadingThrottle } from '~/lib/throttle'

/**
 * The coalescing window. Long enough to hold a parent's turn end together with
 * the subagent completions around it, short enough that the tree never looks
 * stuck.
 */
export const TURN_END_REFRESH_WINDOW_MS = 300

export interface TurnEndRefreshGateDeps {
  /**
   * The worker whose working tree the git panel and the file tree show, or ''
   * when no tab resolves one.
   */
  shownWorkerId: () => string
  /** The worker an agent runs on, or '' when this client knows no such tab. */
  workerIdForAgent: (agentId: string) => string
  /** Whether this agent's tab closes now. */
  isAgentClosing: (agentId: string) => boolean
  /** Runs one refresh. */
  refresh: () => void
  /** Coalescing window. Defaults to {@link TURN_END_REFRESH_WINDOW_MS}. */
  windowMs?: number
}

export interface TurnEndRefreshGate {
  /** Report that `agentId` ended a turn. */
  notify: (agentId: string) => void
  /** Drop a pending trailing refresh, and refuse every later notify. */
  dispose: () => void
}

export function createTurnEndRefreshGate(deps: TurnEndRefreshGateDeps): TurnEndRefreshGate {
  let disposed = false
  /**
   * The agents admitted since the last refresh. The trailing edge asks them
   * again, because up to a window passes between the admission and the refresh
   * it pays for, and both answers can change inside it: the user closes the
   * tab, or moves to a tab on another worker.
   */
  const admitted = new Set<string>()

  /**
   * Whether a turn end by `agentId` can show in what the user sees.
   *
   * FAILS OPEN on an unknown id, in both directions. A tab this client did not
   * hydrate yet reports no worker, and so does a panel with no active tab. A
   * refresh nobody needed costs one RPC; a refresh that never happens leaves
   * the user reading a stale tree with nothing to tell them so.
   *
   * One case fails open that carries no benefit: an agent with NO tab at all,
   * which a root reaches when the client watches it only through a child tab's
   * NOTIFY entry. The narrower test costs a scan of every tab for one that
   * claims this root, and it buys one RPC in a rare window, so this deliberately
   * keeps the wider rule.
   */
  const admits = (agentId: string): boolean => {
    if (deps.isAgentClosing(agentId))
      return false
    const shown = deps.shownWorkerId()
    const ending = deps.workerIdForAgent(agentId)
    return shown === '' || ending === '' || shown === ending
  }

  const refresh = leadingThrottle(() => {
    // The leading edge asks the one agent that just passed `admits`, so it
    // always refreshes. The trailing edge asks the whole burst again, and stays
    // quiet when every member of it went stale.
    const live = [...admitted].some(admits)
    admitted.clear()
    if (live) {
      deps.refresh()
      return
    }
    // Nothing was spent, so close the window rather than serve it out. The next
    // turn end then refreshes IMMEDIATELY, which is what the first one of a
    // burst promises.
    refresh.cancel()
  }, deps.windowMs ?? TURN_END_REFRESH_WINDOW_MS)

  return {
    notify(agentId: string) {
      // A gate whose host went away must stay quiet, the way an aborted
      // AbortController stays aborted. Without this a turn end still in flight
      // would refresh into a torn-down owner and arm a timer nobody can cancel.
      if (disposed)
        return
      if (!admits(agentId))
        return
      admitted.add(agentId)
      refresh()
    },
    dispose() {
      disposed = true
      refresh.cancel()
      admitted.clear()
    },
  }
}
