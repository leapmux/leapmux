import type { TerminalProcess } from '~/generated/proto/leapmux/v1/terminal_pb'
import type { AgentActivityStore } from '~/stores/agentActivity.store'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import type { Tab } from '~/stores/tab.types'
import * as workerRpc from '~/api/workerRpc'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createLogger } from '~/lib/logger'
import { isActiveBackgroundTaskStatus } from '~/stores/chatBackgroundTasks'
import { isSubagentTab, tabDisplayLabel } from '~/stores/tab.helpers'

const log = createLogger('tabBusyProbe')

/** Why closing a tab would interrupt work, and what that work is. */
export type TabBusyReason
  = | { kind: 'agent-turn', activeTasks: BackgroundTaskItem[] }
    | { kind: 'terminal-processes', processes: TerminalProcess[], totalCount: number }

/** One busy tab and the reason, for the aggregated bulk-close prompt. */
export interface BusyTab {
  tab: Tab
  title: string
  reason: TabBusyReason
}

export interface TabBusyProbeDeps {
  activity: AgentActivityStore
  /** The tab's background-task rows, scoped to that tab (root roll-up or a child's own). */
  tasksFor: (agentId: string) => BackgroundTaskItem[]
}

/**
 * Answers "would closing this tab interrupt running work".
 *
 * The two tab kinds answer from different places, and both are the Worker's
 * word:
 *
 * - an AGENT reads the pushed activity state, already in a store. No round
 *   trip, so the common close stays as fast as it was.
 * - a TERMINAL asks, because nothing watches a terminal's process tree
 *   continuously and nothing should: the answer is wanted once, at the moment
 *   of the close.
 *
 * FAIL OPEN throughout. A probe that cannot answer reports "not busy" and lets
 * the close proceed, matching what handleTabClose already does for an
 * unreachable worker. The alternative -- refusing a close nobody can confirm --
 * strands a tab the user has no other way to shut.
 */
export function createTabBusyProbe(deps: TabBusyProbeDeps) {
  const agentReason = (tab: Tab): TabBusyReason | null => {
    // A subagent tab closes in the UI only: the worker treats CloseAgent on a
    // child as tab-close-only, the transcript survives and the tab can be
    // revived. Nothing stops, so there is nothing to warn about.
    if (tab.type !== TabType.AGENT || isSubagentTab(tab))
      return null
    // interruptsWork, not isBusy. They differ for an agent blocked on a
    // permission prompt: the indicator must not spin at somebody who is being
    // asked a question, but its turn is still in flight and this close kills it
    // along with every background task under it.
    if (!deps.activity.interruptsWork(tab.id))
      return null
    return {
      kind: 'agent-turn',
      // The same predicate the chip counts with. Re-spelling the statuses here
      // would let the dialog list fewer running tasks than the chip beside it
      // reports as soon as a new status joins the enum.
      activeTasks: deps.tasksFor(tab.id).filter(t => isActiveBackgroundTaskStatus(t.status)),
    }
  }

  const terminalIdsByWorker = (tabs: readonly Tab[]): Map<string, string[]> => {
    const byWorker = new Map<string, string[]>()
    for (const tab of tabs) {
      if (tab.type !== TabType.TERMINAL || !tab.workerId)
        continue
      const ids = byWorker.get(tab.workerId)
      if (ids)
        ids.push(tab.id)
      else
        byWorker.set(tab.workerId, [tab.id])
    }
    return byWorker
  }

  /**
   * Ask every worker that hosts one of these terminals, concurrently, and merge
   * the answers. One request per WORKER rather than per tab: closing a tile of
   * eight terminals on one machine is one round trip.
   */
  const terminalReasons = async (tabs: readonly Tab[]): Promise<Map<string, TabBusyReason>> => {
    const out = new Map<string, TabBusyReason>()
    await Promise.all([...terminalIdsByWorker(tabs)].map(async ([workerId, terminalIds]) => {
      try {
        const resp = await workerRpc.inspectTerminalProcesses(workerId, { terminalIds })
        for (const entry of resp.terminals) {
          if (entry.processes.length === 0)
            continue
          out.set(entry.terminalId, {
            kind: 'terminal-processes',
            processes: entry.processes,
            totalCount: entry.totalCount,
          })
        }
      }
      catch (err) {
        // Fail open: the close proceeds unwarned rather than being blocked by a
        // question nobody could answer.
        log.warn('inspectTerminalProcesses failed; closing without a busy check', err)
      }
    }))
    return out
  }

  /**
   * The whole set at once, for the aggregated prompt a tile/grid/window close
   * shows before it starts closing.
   */
  const probeMany = async (tabs: readonly Tab[]): Promise<BusyTab[]> => {
    const terminals = await terminalReasons(tabs)
    const out: BusyTab[] = []
    for (const tab of tabs) {
      const reason = tab.type === TabType.TERMINAL ? terminals.get(tab.id) ?? null : agentReason(tab)
      if (reason)
        out.push({ tab, title: tabDisplayLabel(tab), reason })
    }
    return out
  }

  /**
   * One tab, through the same dispatch the bulk probe uses, so the two cannot
   * disagree about how a tab's reason is resolved. A tab that is not busy
   * produces no row, so an empty answer means "nothing to warn about".
   */
  const probe = async (tab: Tab): Promise<TabBusyReason | null> =>
    (await probeMany([tab]))[0]?.reason ?? null

  return { probe, probeMany }
}

export type TabBusyProbe = ReturnType<typeof createTabBusyProbe>
