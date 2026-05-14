import type { Tab } from '~/stores/tab.types'
import type { createWorkspaceStoreRegistry } from '~/stores/workspaceStoreRegistry'
import { listTabsForWorkspace } from '~/api/listTabsBatcher'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createInflightCache } from '~/lib/inflightCache'
import { createLogger } from '~/lib/logger'
import { preserveNonEmptyGitFields, preserveTerminalDisplayFields, protoToAgentTab, protoToTerminalTab, tabKey, tabsByKey } from '~/stores/tab.helpers'
import { fanOutTabsToWorkers } from './workspaceTabHydration'

const log = createLogger('useWorkspaceHydration')

export interface UseWorkspaceHydrationArgs {
  registry: ReturnType<typeof createWorkspaceStoreRegistry>
  getOrgId: () => string | undefined
}

/**
 * Lazy-loads a workspace's tabs + agent/terminal metadata into the
 * registry cache the first time the user expands the workspace's
 * sidebar entry. Mirrors the same agent/terminal/tab hydration shape
 * that `useWorkspaceRestore` does for the active workspace — minus
 * the "make it the active one" wiring — so the cached snapshot is
 * ready when the user does eventually switch in.
 *
 * Returns `{ expand }`: call with a workspace_id to trigger the lazy
 * fetch. Inflight expansions and already-loaded snapshots are no-ops.
 * Failures are logged but otherwise swallowed; the sidebar will
 * retry on the next user interaction.
 */
export function useWorkspaceHydration(args: UseWorkspaceHydrationArgs): { expand: (workspaceId: string) => void } {
  const { registry, getOrgId } = args
  const tabsLoadInflight = createInflightCache<string, void>()

  const expand = (workspaceId: string): void => {
    const snap = registry.get(workspaceId)
    if (snap?.tabsLoaded)
      return
    if (tabsLoadInflight.has(workspaceId))
      return
    const currentOrgId = getOrgId()
    if (!currentOrgId)
      return

    void tabsLoadInflight.run(workspaceId, async () => {
      try {
        const tabsResp = await listTabsForWorkspace(currentOrgId, workspaceId)
        const { agents, terminalsByWorker } = await fanOutTabsToWorkers(tabsResp.tabs)
        const anyTerminalFetchFailed = terminalsByWorker.some(r => r.terminals === null)

        // Index the previous snapshot's tabs so we can preserve
        // gitBranch/gitOriginUrl across a transient BatchGetGitStatus
        // miss. Matches the hydration behaviour in useWorkspaceRestore.
        const existing = registry.get(workspaceId)
        const previousTabsByKey = existing ? tabsByKey(existing.tabs) : new Map<string, Tab>()

        const tabs: Tab[] = []
        for (const a of agents) {
          const fresh = protoToAgentTab(a.workerId, a)
          const previous = previousTabsByKey.get(tabKey(fresh))
          tabs.push({ ...fresh, ...preserveNonEmptyGitFields(fresh, previous) })
        }
        for (const { workerId, terminals } of terminalsByWorker) {
          if (terminals === null)
            continue
          for (const t of terminals) {
            const fresh = protoToTerminalTab(workerId, t)
            const previous = previousTabsByKey.get(tabKey(fresh))
            const fields = preserveTerminalDisplayFields(preserveNonEmptyGitFields(fresh, previous), previous)
            tabs.push({ ...fresh, ...fields })
          }
        }

        // When a terminal fetch fails, preserve the previous terminal tabs (if any)
        // so they don't disappear from the sidebar on a transient error. An empty
        // successful result means the worker truly has no terminals.
        if (anyTerminalFetchFailed && existing) {
          const freshTerminalIds = new Set<string>()
          for (const t of tabs) {
            if (t.type === TabType.TERMINAL)
              freshTerminalIds.add(t.id)
          }
          for (const t of existing.tabs) {
            if (t.type === TabType.TERMINAL && !freshTerminalIds.has(t.id))
              tabs.push(t)
          }
        }
        registry.set(workspaceId, {
          workspaceId,
          tabs,
          activeTabKey: existing?.activeTabKey ?? null,
          layout: existing?.layout ?? { root: { type: 'leaf', id: 'default' }, focusedTileId: null },
          restored: false,
          tabsLoaded: true,
        })
      }
      catch (err) {
        // Transient: the sidebar will re-expand and retry on the next user
        // interaction. Worth a warn so flaky hubs don't fail silently.
        log.warn('failed to lazy-load tabs for workspace', { workspaceId, err })
      }
    })
  }

  return { expand }
}
