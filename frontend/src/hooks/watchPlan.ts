import type {
  WatchAgentEntry,
  WatchRejection,
  WatchTerminalEntry,
} from '~/generated/proto/leapmux/v1/workspace_pb'
import type { AgentTab, Tab } from '~/stores/tab.types'
import { WatchReplayMode } from '~/generated/proto/leapmux/v1/agent_pb'
import {
  TabType,
  WatchMode,
  WatchRejectionReason,
} from '~/generated/proto/leapmux/v1/workspace_pb'
import { isSubagentTab, rootAgentIdFor } from '~/stores/tab.helpers'
import { isPayloadBackedTabType } from '~/stores/tab.types'

/**
 * State the channel's complete current interest for one worker.
 * FULL includes content. NOTIFY keeps notifications for a background tab without chat deltas or terminal bytes.
 */
export interface WatchPlan {
  agents: WatchAgentEntry[]
  terminals: WatchTerminalEntry[]
  /**
   * Terminals that need a full snapshot because incremental catch-up cannot restore missing bytes.
   * Keep this local set outside the entries that the request serializes.
   * watchPlanKey includes this set so a resync change sends a new request.
   */
  terminalResync: Set<string>
}

/**
 * Use FULL when the tab belongs to the active workspace and is its tile's active tab.
 * Each visible tile receives content in a split layout. A background tab receives notifications only.
 * Document visibility does not demote a tab. Returning to the window therefore requires no new catch-up or git batch.
 * A tab in another workspace or behind another tile key still uses NOTIFY.
 */
export function tabWatchMode(
  tab: Tab,
  activeWorkspaceId: string | null,
  activeKeyForTile: (tileId: string) => string | null,
): WatchMode {
  return isTabOnScreen(tab, activeWorkspaceId, activeKeyForTile) ? WatchMode.FULL : WatchMode.NOTIFY
}

/**
 * Require the active workspace and the active tab in its placed tile.
 * tabWatchMode and isAgentTabOnScreen share this rule with the terminal notification handlers.
 * A separate activeKeyForWorkspace fallback would let the terminal and agent decisions differ.
 */
// Tab rows carry explicit undefined fields. Accept a complete Tab without rebuilding it at each caller.
export function isTabOnScreen(
  tab: { tileId?: string | undefined, workspaceId?: string | undefined, type: TabType, id: string } | undefined,
  activeWorkspaceId: string | null,
  activeKeyForTile: (tileId: string) => string | null,
): boolean {
  if (!tab || !tab.tileId)
    return false
  if (!activeWorkspaceId || tab.workspaceId !== activeWorkspaceId)
    return false
  return activeKeyForTile(tab.tileId) === `${tab.type}:${tab.id}`
}

/**
 * Build a WatchEvents agent entry from its resume cursor and mode.
 * A zero cursor requests LATEST.
 * The worker uses cursor and replay for each new FULL lifetime.
 * windowTailSeq gives the loaded window tail, which can precede the cursor.
 * The worker measures the gap from that tail, which the browser uses to restore its position.
 */
export function agentWatchEntry(
  agentId: string,
  resumeSeq: bigint,
  windowTailSeq: bigint,
  mode: WatchMode,
): WatchAgentEntry {
  const base = resumeSeq > 0n
    ? { agentId, replay: WatchReplayMode.AFTER_CURSOR_OR_NONE, cursorSeq: resumeSeq, windowTailSeq, mode }
    : { agentId, replay: WatchReplayMode.LATEST, cursorSeq: BigInt(0), mode }
  return base as WatchAgentEntry
}

/**
 * Describe a terminal with no tab placement, such as a quake shell in a working directory's panel.
 * The caller selects its mode from the panel's open state and the focused tab's working directory.
 * buildWatchPlans therefore needs only the supplied tabs and cursors.
 */
export interface DetachedTerminalWatch {
  terminalId: string
  workerId: string
  mode: WatchMode
}

/**
 * Build one plan per worker with a placed tab or a detached terminal. Exclude payload-backed tabs.
 * A child transcript also needs its root owner's BackgroundTasksChanged and TodosChanged notifications.
 * Add a NOTIFY root entry when no actual root tab supplies an entry.
 * An actual root tab supplies its own mode and cursors in either tab order.
 * Keep one entry per agent because the worker accepts the first entry for each agent ID.
 * Supply optional inputs through opts so callers cannot transpose positional cursor handlers that share one type.
 */
export interface BuildWatchPlansOpts {
  agentResumeSeq?: (agentId: string) => bigint
  agentWindowTailSeq?: (agentId: string) => bigint
  terminalAfterOffset?: (terminalId: string) => bigint | number
  terminalNeedsResync?: (terminalId: string) => boolean
  /**
   * Resolve a child agent to its root. An absent lookup disables the extra root entry.
   */
  getAgentTab?: (agentId: string) => AgentTab | undefined
  /**
   * Include terminals with no tab placement, such as quake shells in panels.
   * Without these entries, the worker sends no output and the panel stays blank while the shell runs.
   */
  detachedTerminals?: readonly DetachedTerminalWatch[]
}

export function buildWatchPlans(
  tabs: readonly Tab[],
  activeWorkspaceId: string | null,
  activeKeyForTile: (tileId: string) => string | null,
  opts: BuildWatchPlansOpts = {},
): Map<string, WatchPlan> {
  const {
    agentResumeSeq = () => 0n,
    agentWindowTailSeq = () => 0n,
    terminalAfterOffset = () => 0,
    terminalNeedsResync = () => false,
    getAgentTab,
    detachedTerminals = [],
  } = opts
  const plans = new Map<string, WatchPlan>()
  // Index each worker's agent entries so an actual tab can replace an earlier child-derived root entry.
  const agentIndices = new Map<string, Map<string, number>>()
  for (const tab of tabs) {
    // A tab that holds a payload runs no agent and needs no watch.
    if (isPayloadBackedTabType(tab.type))
      continue
    if (!tab.workerId || !tab.tileId)
      continue
    const mode = tabWatchMode(tab, activeWorkspaceId, activeKeyForTile)
    const workerId = tab.workerId
    let plan = plans.get(workerId)
    if (!plan) {
      plan = { agents: [], terminals: [], terminalResync: new Set() }
      plans.set(workerId, plan)
    }
    if (tab.type === TabType.AGENT) {
      let indices = agentIndices.get(workerId)
      if (!indices) {
        indices = new Map()
        agentIndices.set(workerId, indices)
      }
      const entry = agentWatchEntry(tab.id, agentResumeSeq(tab.id), agentWindowTailSeq(tab.id), mode)
      const index = indices.get(tab.id)
      if (index === undefined) {
        indices.set(tab.id, plan.agents.length)
        plan.agents.push(entry)
      }
      else {
        plan.agents[index] = entry
      }
      // A child needs the root's notifications. Reuse an actual root or an entry that another child supplied.
      if (getAgentTab && isSubagentTab(tab)) {
        const rootId = rootAgentIdFor(getAgentTab, tab.id)
        if (rootId !== tab.id && !indices.has(rootId)) {
          indices.set(rootId, plan.agents.length)
          plan.agents.push(agentWatchEntry(rootId, 0n, 0n, WatchMode.NOTIFY))
        }
      }
    }
    else if (tab.type === TabType.TERMINAL) {
      pushTerminal(plan, tab.id, mode)
    }
  }
  // Add detached terminals after placed tabs. The shared helper applies the same recovery rules to both.
  for (const detached of detachedTerminals) {
    if (!detached.workerId || !detached.terminalId)
      continue
    let plan = plans.get(detached.workerId)
    if (!plan) {
      plan = { agents: [], terminals: [], terminalResync: new Set() }
      plans.set(detached.workerId, plan)
    }
    // The independent detached list can include a terminal that the tab list already supplied.
    if (plan.terminals.some(t => t.terminalId === detached.terminalId))
      continue
    pushTerminal(plan, detached.terminalId, detached.mode)
  }
  return plans

  /**
   * Add one terminal with the same resync and cursor rules for both loops.
   * A terminal that needs resync requests afterOffset zero. The worker then sends the full snapshot that restores missing bytes.
   * A separate rule for detached terminals could make a quake shell and a placed terminal recover differently.
   */
  function pushTerminal(plan: WatchPlan, terminalId: string, mode: WatchMode): void {
    const resync = terminalNeedsResync(terminalId)
    const after = resync ? 0 : terminalAfterOffset(terminalId)
    if (resync)
      plan.terminalResync.add(terminalId)
    plan.terminals.push({
      terminalId,
      afterOffset: typeof after === 'bigint' ? after : BigInt(after),
      mode,
    } as WatchTerminalEntry)
  }
}

/**
 * Compare these interest fields:
 * - Entity IDs.
 * - Modes.
 * - Terminal resync state.
 * Exclude cursors because each message changes them.
 * Including cursors would send an interest update for each chat frame.
 * A resync change sends the plan with afterOffset zero, then sends the normal cursor when resync clears.
 */
export function watchPlanKey(plan: WatchPlan): string {
  const agents = plan.agents
    .map(a => `${a.agentId}:${a.mode}`)
    .toSorted()
    .join(',')
  const terminals = plan.terminals
    .map(t => `${t.terminalId}:${t.mode}${plan.terminalResync.has(t.terminalId) ? ':r' : ''}`)
    .toSorted()
    .join(',')
  return `a:${agents}|t:${terminals}`
}

/**
 * Retry LOOKUP_FAILED only while a local tab still exists.
 * A durable or unknown rejection settles the request. A later real interest change can request that entity again.
 * Settling an unknown reason can leave a stale tab until that change. Retrying it could repeat forever.
 */
export function shouldRetryRejection(r: WatchRejection, tabExists: boolean): boolean {
  return tabExists && r.reason === WatchRejectionReason.LOOKUP_FAILED
}
