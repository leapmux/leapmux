import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import type { TerminalInfo } from '~/generated/leapmux/v1/terminal_pb'
import type { WorkspaceTab } from '~/generated/leapmux/v1/workspace_pb'
import { listAgents, listTerminals } from '~/api/workerRpc'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createLogger } from '~/lib/logger'
import { tabKey } from '~/stores/tab.store'

const log = createLogger('tabHydrate')

export interface WorkerFanOutResult {
  agents: AgentInfo[]
  /**
   * Per-worker terminal results. `terminals === null` means the ListTerminals
   * RPC failed (worker offline/crashed), and callers that care can preserve
   * the previously-cached terminal tabs instead of dropping them. An empty
   * array means the worker returned no terminals, which is authoritative.
   */
  terminalsByWorker: Array<{ workerId: string, terminals: TerminalInfo[] | null }>
  /** Maps `${tabType}:${tabId}` → tileId, populated from the hub response. */
  tabTileMap: Map<string, string>
}

/**
 * Fans out per-worker listAgents/listTerminals calls for a set of hub-level
 * WorkspaceTab records. Used by both the active-workspace restore path and
 * the sibling-expand path, which share the same group-by-worker + fetch
 * logic before diverging at how they assemble the result into stores.
 */
export async function fanOutTabsToWorkers(tabs: WorkspaceTab[]): Promise<WorkerFanOutResult> {
  const agentIdsByWorker = new Map<string, string[]>()
  const terminalIdsByWorker = new Map<string, string[]>()
  const tabTileMap = new Map<string, string>()

  for (const t of tabs) {
    if (t.tileId)
      tabTileMap.set(tabKey({ type: t.tabType, id: t.tabId }), t.tileId)
    if (!t.workerId)
      continue
    if (t.tabType === TabType.AGENT) {
      const ids = agentIdsByWorker.get(t.workerId) ?? []
      ids.push(t.tabId)
      agentIdsByWorker.set(t.workerId, ids)
    }
    else if (t.tabType === TabType.TERMINAL) {
      const ids = terminalIdsByWorker.get(t.workerId) ?? []
      ids.push(t.tabId)
      terminalIdsByWorker.set(t.workerId, ids)
    }
  }

  const [agentResults, terminalResults] = await Promise.all([
    Promise.all(Array.from(agentIdsByWorker, async ([workerId, tabIds]) => {
      try {
        return (await listAgents(workerId, { tabIds })).agents
      }
      catch (err) {
        log.warn('failed to list agents from worker', { workerId, tabIds, err })
        return []
      }
    })),
    Promise.all(Array.from(terminalIdsByWorker, async ([workerId, tabIds]) => {
      try {
        return { workerId, terminals: (await listTerminals(workerId, { tabIds })).terminals }
      }
      catch (err) {
        log.warn('failed to list terminals from worker', { workerId, tabIds, err })
        return { workerId, terminals: null as TerminalInfo[] | null }
      }
    })),
  ])

  return {
    agents: agentResults.flat(),
    terminalsByWorker: terminalResults,
    tabTileMap,
  }
}
