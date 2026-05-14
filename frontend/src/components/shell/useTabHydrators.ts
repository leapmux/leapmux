import type { createFileTabPathsStore } from '~/lib/fileTabPaths'
import type { createTabStore } from '~/stores/tab.store'
import { createEffect, createMemo } from 'solid-js'
import { getFileTabPath, listAgents, listTerminals } from '~/api/workerRpc'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { protoToAgentTabFields, protoToTerminalTabFields } from '~/stores/tab.helpers'

/**
 * Per-tab-type hydration of CRDT-projected tabs that arrived without
 * their worker-side metadata (path / agent record / terminal title).
 * The hub strips file paths and agent/terminal payloads from the
 * org-events stream — those live behind E2EE on the worker. Without
 * these hydrators a tab opened by another client (or by the
 * `leapmux remote tab open` CLI) renders as a bare CRDT row until the
 * user clicks it.
 *
 * `createTabHydration` factors out the membership-hash + in-flight-set
 * + best-effort-fetch pattern shared by the three hydrators. The
 * membership memo re-fires only when the SET of pending tab ids
 * actually changes, so unrelated tab mutations (drag, rename, status
 * update) don't re-walk the tab list.
 */
export interface UseTabHydratorsOpts {
  tabStore: ReturnType<typeof createTabStore>
  fileTabPaths: ReturnType<typeof createFileTabPathsStore>
  getOrgId: () => string | null | undefined
}

export function useTabHydrators(opts: UseTabHydratorsOpts): void {
  type Tab = (typeof opts.tabStore.state.tabs)[number]

  function createTabHydration(spec: {
    predicate: (tab: Tab) => boolean
    fetch: (tab: Tab) => Promise<void>
    precondition?: () => boolean
  }): void {
    const inflight = new Set<string>()
    // candidates carries the per-tab refs alongside the membership hash
    // so the dispatch effect doesn't walk state.tabs a second time just
    // to redo the predicate. The structural-equality `equals` on hash
    // also keeps the downstream effect from firing when membership is
    // unchanged (e.g. an unrelated tab field updated).
    const matches = createMemo<{ hash: string, candidates: Tab[] }>(() => {
      const candidates: Tab[] = []
      const ids: string[] = []
      for (const tab of opts.tabStore.state.tabs) {
        if (!spec.predicate(tab))
          continue
        candidates.push(tab)
        ids.push(tab.id)
      }
      ids.sort()
      return { hash: ids.join(' '), candidates }
    }, { hash: '', candidates: [] }, { equals: (a, b) => a.hash === b.hash })
    createEffect(() => {
      if (spec.precondition && !spec.precondition())
        return
      const { candidates } = matches()
      for (const tab of candidates) {
        if (inflight.has(tab.id))
          continue
        inflight.add(tab.id)
        const tabId = tab.id
        spec.fetch(tab)
          .catch(() => { /* hydration is best-effort; will retry on next state mutation */ })
          .finally(() => inflight.delete(tabId))
      }
    })
  }

  // FILE: GetFileTabPath populates the path/title via worker E2EE. The
  // WatchWorkspacePrivateEvents stream's bootstrap reply covers the
  // late-joiner case, but if a tab lands before the private-event
  // stream has finished its bootstrap, we issue a one-shot
  // GetFileTabPath so the title renders without a perceptible delay.
  createTabHydration({
    precondition: () => Boolean(opts.getOrgId()),
    predicate: tab => tab.type === TabType.FILE && !tab.filePath && Boolean(tab.workerId) && !opts.fileTabPaths.pathFor(tab.id),
    fetch: async (tab) => {
      const orgId = opts.getOrgId()
      if (!orgId || !tab.workerId)
        return
      const resp = await getFileTabPath(tab.workerId, { orgId, tabId: tab.id })
      opts.fileTabPaths.register(tab.id, resp.workspaceId, resp.filePath)
      opts.tabStore.updateTab(TabType.FILE, tab.id, { filePath: resp.filePath })
    },
  })

  // AGENT: ListAgents fetches the agent record for tabs that arrived
  // via the CRDT projection without going through this client's local
  // OpenAgent response (e.g. another browser tab, or the
  // `leapmux remote tab open` CLI). Without this, AGENT tabs render as
  // "Agent not found." because the tab carries only the CRDT-driven
  // tile/position/worker fields.
  createTabHydration({
    predicate: tab => tab.type === TabType.AGENT && Boolean(tab.workerId) && tab.agentStatus === undefined,
    fetch: async (tab) => {
      if (!tab.workerId)
        return
      const resp = await listAgents(tab.workerId, { tabIds: [tab.id] })
      const agent = resp.agents.find(a => a.id === tab.id)
      if (!agent)
        return
      // protoToAgentTabFields writes every per-agent field onto the
      // tab and primes `settingsLabelCache` with the agent's catalogs.
      opts.tabStore.updateTab(TabType.AGENT, tab.id, protoToAgentTabFields(tab.workerId, agent))
    },
  })

  // TERMINAL metadata lives on the tab itself; a tab the user just
  // received via CRDT has only tile/position/worker set. We treat an
  // empty title as "not hydrated yet" — workspace restore goes through
  // `protoToTerminalTab` which always populates title (or a fallback),
  // so the only path that leaves it empty is the CRDT-projection-only
  // path this hydrator covers.
  createTabHydration({
    predicate: tab => tab.type === TabType.TERMINAL && Boolean(tab.workerId) && !tab.title,
    fetch: async (tab) => {
      if (!tab.workerId)
        return
      const resp = await listTerminals(tab.workerId, { tabIds: [tab.id] })
      const term = resp.terminals.find(t => t.terminalId === tab.id)
      if (term)
        opts.tabStore.updateTab(TabType.TERMINAL, tab.id, protoToTerminalTabFields(tab.workerId, term))
    },
  })
}
