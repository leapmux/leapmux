import type {
  WatchAgentEntry,
  WatchRejection,
  WatchTerminalEntry,
} from '~/generated/leapmux/v1/workspace_pb'
/**
 * What this client asks each worker to watch, and how much of each entity's
 * traffic it wants.
 *
 * A WatchEvents request states the channel's WHOLE current interest. Modes
 * select content vs notification-class traffic so a background tab still
 * badges and rings without paying for chat deltas / terminal bytes.
 */
import type { AgentTab, Tab } from '~/stores/tab.types'
import { WatchReplayMode } from '~/generated/leapmux/v1/agent_pb'
import {
  TabType,
  WatchMode,
  WatchRejectionReason,
} from '~/generated/leapmux/v1/workspace_pb'
import { rootAgentIdFor } from '~/stores/tab.helpers'

export interface WatchPlan {
  agents: WatchAgentEntry[]
  terminals: WatchTerminalEntry[]
}

/**
 * FULL iff the tab's content is actually on screen: the tab is in the active
 * workspace and it is its tile's active tab. A split layout therefore feeds
 * every on-screen tile, and a background pane costs notifications only.
 *
 * Document visibility does NOT demote: hiding the window keeps FULL so
 * return-to-tab does not re-pay catch-up + git batch. Per-tab NOTIFY when a
 * tab is off-screen (other workspace / other tile key) is unchanged.
 */
export function tabWatchMode(
  tab: Tab,
  activeWorkspaceId: string | null,
  activeKeyForTile: (tileId: string) => string | null,
): WatchMode {
  return isTabOnScreen(tab, activeWorkspaceId, activeKeyForTile) ? WatchMode.FULL : WatchMode.NOTIFY
}

/**
 * True when `tab`'s content is actually on screen: it is in the active
 * workspace, it has a tile placement, and it is its tile's active tab. This is
 * the single source of the "on-screen / FULL" rule that tabWatchMode,
 * isAgentTabOnScreen, and the terminal badge/notify paths all need — keep them
 * routed through here so they cannot drift (they had: the terminal path carried
 * an activeKeyForWorkspace fallback the agent path lacked).
 */
export function isTabOnScreen(
  tab: { tileId?: string, workspaceId?: string, type: TabType, id: string } | undefined,
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
 * Build a WatchEvents agent entry from a resume cursor and mode. A resume seq
 * of 0n means nothing has been observed yet, so subscribe fresh (LATEST).
 * cursor/replay are only meaningful on the transition INTO FULL.
 */
export function agentWatchEntry(
  agentId: string,
  resumeSeq: bigint,
  mode: WatchMode,
): WatchAgentEntry {
  const base = resumeSeq > 0n
    ? { agentId, replay: WatchReplayMode.AFTER_CURSOR, cursorSeq: resumeSeq, mode }
    : { agentId, replay: WatchReplayMode.LATEST, cursorSeq: BigInt(0), mode }
  return base as WatchAgentEntry
}

/**
 * One plan per worker that hosts any placed tab. Excludes FILE tabs.
 *
 * A child agent tab (a subagent transcript with `parentAgentId` set) also
 * subscribes to its ROOT owner agent id with NOTIFY mode. The registry
 * (BackgroundTasksChanged) and the root's TodosChanged are broadcast under the
 * root id, so without this entry a child-only watcher never receives live
 * updates when the root spawns another subagent or its todo list changes.
 * NOTIFY is sufficient (both event types are notification-class) and avoids
 * the backend "last mode wins" dedup demoting a real on-screen root tab's FULL
 * entry.
 *
 * `getAgentTab` resolves a child to its root; pass `undefined` to skip the
 * root-entry logic (used by tests that do not exercise the child path).
 */
export function buildWatchPlans(
  tabs: readonly Tab[],
  activeWorkspaceId: string | null,
  activeKeyForTile: (tileId: string) => string | null,
  agentResumeSeq: (agentId: string) => bigint = () => 0n,
  terminalAfterOffset: (terminalId: string) => bigint | number = () => 0,
  getAgentTab: ((agentId: string) => AgentTab | undefined) | undefined = undefined,
): Map<string, WatchPlan> {
  const plans = new Map<string, WatchPlan>()
  // Track which agent ids each worker's plan already watches, so a child-driven
  // root entry is not duplicated when the root tab is itself placed (or when two
  // children share a root). Keeps watchPlanKey stable and the wire update minimal.
  const watchedAgentIds = new Map<string, Set<string>>()
  const agentIdsFor = (workerId: string): Set<string> => {
    let s = watchedAgentIds.get(workerId)
    if (!s) {
      s = new Set()
      watchedAgentIds.set(workerId, s)
    }
    return s
  }
  for (const tab of tabs) {
    if (tab.type === TabType.FILE)
      continue
    if (!tab.workerId || !tab.tileId)
      continue
    const mode = tabWatchMode(tab, activeWorkspaceId, activeKeyForTile)
    const workerId = tab.workerId
    let plan = plans.get(workerId)
    if (!plan) {
      plan = { agents: [], terminals: [] }
      plans.set(workerId, plan)
    }
    if (tab.type === TabType.AGENT) {
      const seen = agentIdsFor(workerId)
      plan.agents.push(agentWatchEntry(tab.id, agentResumeSeq(tab.id), mode))
      seen.add(tab.id)
      // A child tab also needs the root's notification-class events
      // (BackgroundTasksChanged, the root's TodosChanged). These are broadcast
      // under the root id, so subscribe to it with NOTIFY. Skip when the root
      // is already watched (the root tab itself is placed, or a sibling child
      // already added it).
      if (getAgentTab && tab.parentAgentId) {
        const rootId = rootAgentIdFor(getAgentTab, tab.id)
        if (rootId !== tab.id && !seen.has(rootId)) {
          plan.agents.push(agentWatchEntry(rootId, 0n, WatchMode.NOTIFY))
          seen.add(rootId)
        }
      }
    }
    else if (tab.type === TabType.TERMINAL) {
      const after = terminalAfterOffset(tab.id)
      plan.terminals.push({
        terminalId: tab.id,
        afterOffset: typeof after === 'bigint' ? after : BigInt(after),
        mode,
      } as WatchTerminalEntry)
    }
  }
  return plans
}

/**
 * Change key for one plan: ids AND modes, deliberately NOT cursors. Cursors
 * move on every message, and keying on them would re-send an update per chat
 * frame.
 */
export function watchPlanKey(plan: WatchPlan): string {
  const agents = plan.agents
    .map(a => `${a.agentId}:${a.mode}`)
    .toSorted()
    .join(',')
  const terminals = plan.terminals
    .map(t => `${t.terminalId}:${t.mode}`)
    .toSorted()
    .join(',')
  return `a:${agents}|t:${terminals}`
}

/**
 * Retry a rejected entity only when the reason is TRANSIENT and a local tab
 * for it still exists. A durable rejection (or an unrecognized reason from a
 * newer worker) settles: the entry is dropped from the next plan and the next
 * genuine tab change re-states it if it ever comes back. Settling by default
 * is the safe direction -- a wrongly-settled entry costs one stale tab until
 * the next change, a wrongly-retried one loops forever.
 */
export function shouldRetryRejection(r: WatchRejection, tabExists: boolean): boolean {
  return tabExists && r.reason === WatchRejectionReason.LOOKUP_FAILED
}
