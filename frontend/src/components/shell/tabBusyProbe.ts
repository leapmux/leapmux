import type { TerminalProcess } from '~/generated/proto/leapmux/v1/terminal_pb'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import type { Tab } from '~/stores/tab.types'
import * as workerRpc from '~/api/workerRpc'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createLogger } from '~/lib/logger'
import { activityInterruptsWork } from '~/stores/agentActivity.store'
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
  /** The tab's background-task rows, scoped to that tab (root roll-up or a child's own). */
  tasksFor: (agentId: string) => BackgroundTaskItem[]
}

/**
 * Answers "would closing this tab interrupt running work".
 *
 * BOTH kinds ask the Worker, and the answer is the Worker's exact word:
 *
 * - a TERMINAL asks because nothing watches a terminal's process tree
 *   continuously and nothing should: the answer is wanted once, at the moment
 *   of the close.
 * - an AGENT asks because the pushed state is DEBOUNCED. The Worker holds a
 *   settle for three seconds so the completion sound does not ring for work
 *   that resumes, which means the store says "busy" for that long after the
 *   work finished. A guard reading it would raise a confirmation dialog over
 *   nothing, and would disagree with the CLI guard, which reads the exact
 *   state. One round trip per worker buys one answer for both surfaces.
 *
 * FAIL OPEN throughout. A probe that cannot answer reports "not busy" and lets
 * the close proceed, matching what handleTabClose already does for an
 * unreachable worker. The alternative -- refusing a close nobody can confirm --
 * strands a tab the user has no other way to shut.
 */
export function createTabBusyProbe(deps: TabBusyProbeDeps) {
  /**
   * A subagent tab closes in the UI only: the worker treats CloseAgent on a
   * child as tab-close-only, the transcript survives and the tab can be
   * revived. Nothing stops, so there is nothing to warn about.
   */
  const guardedAgentTab = (tab: Tab): boolean => tab.type === TabType.AGENT && !isSubagentTab(tab)

  const idsByWorker = (tabs: readonly Tab[], want: (tab: Tab) => boolean): Map<string, string[]> => {
    const byWorker = new Map<string, string[]>()
    for (const tab of tabs) {
      if (!want(tab) || !tab.workerId)
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
   * Ask every worker that hosts one of these agents for the EXACT activity
   * state, one request per worker.
   *
   * activityInterruptsWork, not "busy". They differ for an agent blocked on a
   * permission prompt. The indicator must
   * not spin at somebody who is being asked
   * a question, but its turn is still in
   * flight and this close kills it along
   * with every background task under it.
   */
  const agentReasons = async (tabs: readonly Tab[]): Promise<Map<string, TabBusyReason>> => {
    const out = new Map<string, TabBusyReason>()
    await Promise.all([...idsByWorker(tabs, guardedAgentTab)].map(async ([workerId, tabIds]) => {
      try {
        const resp = await workerRpc.listAgents(workerId, { tabIds })
        for (const info of resp.agents) {
          if (!activityInterruptsWork(info.activityState))
            continue
          out.set(info.id, {
            kind: 'agent-turn',
            // The same predicate the chip counts with. Re-spelling the statuses
            // here would let the dialog list fewer running tasks than the chip
            // beside it reports as soon as a new status joins the enum.
            activeTasks: deps.tasksFor(info.id).filter(t => isActiveBackgroundTaskStatus(t.status)),
          })
        }
      }
      catch (err) {
        // Fail open: the close proceeds unwarned rather than being blocked by a
        // question nobody could answer.
        log.warn('listAgents failed; closing without a busy check', err)
      }
    }))
    return out
  }

  /**
   * Ask every worker that hosts one of these terminals, concurrently, and merge
   * the answers. One request per WORKER rather than per tab: closing a tile of
   * eight terminals on one machine is one round trip.
   */
  const terminalReasons = async (tabs: readonly Tab[]): Promise<Map<string, TabBusyReason>> => {
    const out = new Map<string, TabBusyReason>()
    await Promise.all([...idsByWorker(tabs, t => t.type === TabType.TERMINAL)].map(async ([workerId, terminalIds]) => {
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
    // Both kinds concurrently: a tile that mixes them is still one round of
    // requests, not two in sequence.
    const [terminals, agents] = await Promise.all([terminalReasons(tabs), agentReasons(tabs)])
    const out: BusyTab[] = []
    for (const tab of tabs) {
      const reason = tab.type === TabType.TERMINAL ? terminals.get(tab.id) ?? null : agents.get(tab.id) ?? null
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
