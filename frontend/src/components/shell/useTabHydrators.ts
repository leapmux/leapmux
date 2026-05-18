import type { createFileTabPathsStore } from '~/lib/fileTabPaths'
import type { createTabStore } from '~/stores/tab.store'
import { createEffect, createMemo, onCleanup } from 'solid-js'
import { getFileTabPath, listAgents, listTerminals } from '~/api/workerRpc'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createExponentialBackoff } from '~/lib/retry'
import { protoToAgentTabFields, protoToTerminalTabFields, tabKey } from '~/stores/tab.helpers'

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

  interface BaseSpec {
    predicate: (tab: Tab) => boolean
    precondition?: () => boolean
  }
  /** Per-tab fetch. One RPC per candidate. */
  interface PerTabSpec extends BaseSpec {
    kind: 'per-tab'
    fetch: (tab: Tab) => Promise<void>
  }
  /**
   * Batched fetch: receives all same-worker candidates in one call so
   * one RPC per worker hydrates N tabs instead of N RPCs. The
   * predicate must require `tab.workerId` to be non-empty (a tab with
   * no workerId can't be batch-fetched and is filtered out before
   * dispatch).
   */
  interface BatchedSpec extends BaseSpec {
    kind: 'batched'
    fetchBatch: (workerId: string, tabs: Tab[]) => Promise<void>
  }
  type HydrationSpec = PerTabSpec | BatchedSpec

  function createTabHydration(spec: HydrationSpec): void {
    const inflight = new Set<string>()
    // Per-tab retry: a single RPC failure (e.g. worker channel still
    // handshaking after a page refresh) would otherwise leave the tab
    // in its bare CRDT state indefinitely — the candidate set hasn't
    // changed, so the effect below never re-fires. The retry kicks
    // off another attempt; we eventually succeed once the worker is
    // reachable.
    const retry = createExponentialBackoff<string>({ initialMs: 500, maxMs: 10_000 })
    onCleanup(() => retry.cancelAll())

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

    function schedulePerTabRetry(tab: Tab): void {
      const tabId = tab.id
      const key = tabKey(tab)
      retry.schedule(tabId, () => {
        const stillPending = opts.tabStore.getTabByKey(key)
        if (!stillPending || !spec.predicate(stillPending)) {
          // Tab is gone (closed by user, removed by another client,
          // or hydrated by another path). Drop the per-tab delay so
          // the Map doesn't accumulate ghost entries.
          retry.reset(tabId)
          return
        }
        runFor(stillPending)
      })
    }

    // Worker-keyed retry for batched specs: one timer re-batches every
    // still-pending tab on that worker the next time it fires, instead
    // of N separate single-tab batch RPCs (which would defeat the
    // batching the spec was designed for).
    function scheduleBatchRetry(workerId: string): void {
      retry.schedule(workerId, () => {
        if (spec.kind !== 'batched')
          return
        const stillPending: Tab[] = []
        for (const tab of opts.tabStore.state.tabs) {
          if (tab.workerId === workerId && spec.predicate(tab))
            stillPending.push(tab)
        }
        if (stillPending.length === 0) {
          retry.reset(workerId)
          return
        }
        runForBatch(workerId, stillPending)
      })
    }

    function runFor(tab: Tab): void {
      if (spec.kind !== 'per-tab')
        return
      if (inflight.has(tab.id))
        return
      inflight.add(tab.id)
      const tabId = tab.id
      spec.fetch(tab)
        .then(() => retry.reset(tabId))
        .catch(() => schedulePerTabRetry(tab))
        .finally(() => inflight.delete(tabId))
    }

    function runForBatch(workerId: string, tabs: Tab[]): void {
      if (spec.kind !== 'batched')
        return
      const fresh = tabs.filter(t => !inflight.has(t.id))
      if (fresh.length === 0)
        return
      for (const t of fresh)
        inflight.add(t.id)
      spec.fetchBatch(workerId, fresh)
        .then(() => retry.reset(workerId))
        .catch(() => scheduleBatchRetry(workerId))
        .finally(() => {
          for (const t of fresh)
            inflight.delete(t.id)
        })
    }

    createEffect(() => {
      if (spec.precondition && !spec.precondition())
        return
      const { candidates } = matches()
      if (spec.kind === 'batched') {
        // Group by workerId so one RPC hydrates N same-worker tabs.
        // Predicates for batched hydrators require a non-empty
        // workerId, so candidates without one are filtered upstream;
        // the explicit check here narrows the type for TypeScript.
        const byWorker = new Map<string, Tab[]>()
        for (const tab of candidates) {
          const wid = tab.workerId
          if (!wid)
            continue
          let group = byWorker.get(wid)
          if (!group) {
            group = []
            byWorker.set(wid, group)
          }
          group.push(tab)
        }
        for (const [wid, group] of byWorker)
          runForBatch(wid, group)
        return
      }
      for (const tab of candidates)
        runFor(tab)
    })
  }

  // FILE: GetFileTabPath populates the path/title via worker E2EE. The
  // WatchWorkspacePrivateEvents stream's bootstrap reply covers the
  // late-joiner case, but if a tab lands before the private-event
  // stream has finished its bootstrap, we issue a one-shot
  // GetFileTabPath so the title renders without a perceptible delay.
  createTabHydration({
    kind: 'per-tab',
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
  // tile/position/worker fields. Batched per worker so opening a
  // workspace with N agents on the same worker costs one ListAgents
  // call instead of N.
  createTabHydration({
    kind: 'batched',
    predicate: tab => tab.type === TabType.AGENT && Boolean(tab.workerId) && tab.agentStatus === undefined,
    fetchBatch: async (workerId, tabs) => {
      const tabIds = tabs.map(t => t.id)
      const resp = await listAgents(workerId, { tabIds })
      const byId = new Map(resp.agents.map(a => [a.id, a]))
      for (const tab of tabs) {
        const agent = byId.get(tab.id)
        if (!agent)
          continue
        // protoToAgentTabFields writes every per-agent field onto the
        // tab and primes `settingsLabelCache` with the agent's catalogs.
        opts.tabStore.updateTab(TabType.AGENT, tab.id, protoToAgentTabFields(workerId, agent))
      }
    },
  })

  // TERMINAL metadata lives on the tab itself; a tab the user just
  // received via CRDT has only tile/position/worker set. We treat an
  // empty title as "not hydrated yet" — workspace restore goes through
  // `protoToTerminalTab` which always populates title (or a fallback),
  // so the only path that leaves it empty is the CRDT-projection-only
  // path this hydrator covers. Batched per worker (see AGENT).
  createTabHydration({
    kind: 'batched',
    predicate: tab => tab.type === TabType.TERMINAL && Boolean(tab.workerId) && !tab.title,
    fetchBatch: async (workerId, tabs) => {
      const tabIds = tabs.map(t => t.id)
      const resp = await listTerminals(workerId, { tabIds })
      const byId = new Map(resp.terminals.map(t => [t.terminalId, t]))
      for (const tab of tabs) {
        const term = byId.get(tab.id)
        if (term)
          opts.tabStore.updateTab(TabType.TERMINAL, tab.id, protoToTerminalTabFields(workerId, term))
      }
    },
  })
}
