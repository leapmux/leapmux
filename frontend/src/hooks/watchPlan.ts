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
import type { Tab } from '~/stores/tab.types'
import { WatchReplayMode } from '~/generated/leapmux/v1/agent_pb'
import {
  TabType,
  WatchMode,
  WatchRejectionReason,
} from '~/generated/leapmux/v1/workspace_pb'

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
 */
export function buildWatchPlans(
  tabs: readonly Tab[],
  activeWorkspaceId: string | null,
  activeKeyForTile: (tileId: string) => string | null,
  agentResumeSeq: (agentId: string) => bigint = () => 0n,
  terminalAfterOffset: (terminalId: string) => bigint | number = () => 0,
): Map<string, WatchPlan> {
  const plans = new Map<string, WatchPlan>()
  for (const tab of tabs) {
    if (tab.type === TabType.FILE)
      continue
    if (!tab.workerId || !tab.tileId)
      continue
    const mode = tabWatchMode(tab, activeWorkspaceId, activeKeyForTile)
    let plan = plans.get(tab.workerId)
    if (!plan) {
      plan = { agents: [], terminals: [] }
      plans.set(tab.workerId, plan)
    }
    if (tab.type === TabType.AGENT) {
      plan.agents.push(agentWatchEntry(tab.id, agentResumeSeq(tab.id), mode))
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
