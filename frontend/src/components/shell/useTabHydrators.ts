import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabView } from '~/stores/tabView'
import { createEffect, createMemo, onCleanup, untrack } from 'solid-js'
import { getFileTabPath, listAgents, listTerminals } from '~/api/workerRpc'
import { TabHydrationStatus } from '~/generated/leapmux/v1/common_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createExponentialBackoff } from '~/lib/retry'
import { sameKeys } from '~/lib/sameKeys'
import { protoToAgentTabFields, tabKey, terminalMetadata } from '~/stores/tab.helpers'

/**
 * Per-tab-type hydration of CRDT-projected tabs that arrived without
 * their worker-side metadata (path / agent record / terminal title).
 * The hub strips file paths and agent/terminal payloads from the
 * userevents stream — those live behind E2EE on the worker. Without
 * these hydrators a tab opened by another client (or by the
 * `leapmux remote tab open` CLI) renders as a bare CRDT row until the
 * user clicks it.
 *
 * `createTabHydration` factors out the membership-set + in-flight-set
 * + best-effort-fetch pattern shared by the three hydrators. The
 * membership memo re-fires only when the SET of pending tab ids
 * actually changes, so unrelated tab mutations (drag, rename, status
 * update) don't re-walk the tab list.
 */
export interface UseTabHydratorsOpts {
  view: TabView
  metadata: TabMetadataStore
  /**
   * Which workers the hub currently reports as online.
   *
   * A worker coming back is the one event that makes a previously hopeless
   * fetch worth retrying, and it is not otherwise observable from here: the
   * candidate set does not change when a worker reconnects, so nothing would
   * re-fire. Without it, a worker offline long enough to exhaust its attempt
   * budget stays unhydrated until the user happens to open another tab on it —
   * every agent tab on it keeps the bare `Agent` label and every terminal keeps
   * its startup spinner, for the life of the page.
   */
  onlineWorkerIds?: () => ReadonlySet<string>
}

export function useTabHydrators(opts: UseTabHydratorsOpts): void {
  type Tab = ReturnType<typeof opts.view.all>[number]

  /**
   * Has the worker answered for this tab yet?
   *
   * Read straight off the metadata row rather than through the assembled `Tab`
   * so "hydrated" stays a fact about the fetch, independent of which payload
   * fields happened to arrive.
   */
  const isHydrated = (tabId: string): boolean => opts.metadata.get(tabId)?.hydrated === true

  interface BaseSpec {
    predicate: (tab: Tab) => boolean
  }
  /** Per-tab fetch. One RPC per candidate. */
  interface PerTabSpec extends BaseSpec {
    kind: 'per-tab'
    fetch: (tab: Tab) => Promise<void>
  }
  /**
   * What a batched fetch reports back: which tabs it wrote metadata for, and
   * what the worker SAID about each id that was asked for.
   *
   * The verdicts are not redundant with `resolved`. A batch RPC can succeed
   * while omitting a tab, and the client cannot tell "the worker has no record
   * for this id, ever" from "not yet" on its own -- so the only safe local
   * guess is "ask again", which pins a 10s timer per unanswerable tab for the
   * life of the page. ABSENT is the worker saying "stop asking".
   */
  interface BatchResult {
    /** Answered for; their metadata has been written. */
    resolved: Set<string>
    /** The worker's per-tab answer for every id the batch asked about. */
    verdicts: readonly { tabId: string, status: TabHydrationStatus }[]
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
    fetchBatch: (workerId: string, tabs: Tab[]) => Promise<BatchResult>
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
    // `maxAttempts` is what stops a permanently-unreachable worker from holding
    // a timer forever. `maxMs` only bounds how OFTEN we ask; the loop itself has
    // no other exit, because every failure arm re-arms unconditionally and the
    // candidate-set memo is membership-gated so it cannot re-fire on its own. A tab
    // whose worker was deregistered — or whose channel never opens — would
    // otherwise poll every ~10s for the life of the page, once per (hydrator,
    // worker) pair, across every workspace in the account. Eight attempts spans
    // roughly a minute of backoff, long enough to ride out a handshake or a
    // worker restart; anything past that is not a transient failure.
    //
    // The budget is restored by a success, by a change to the candidate set for
    // that key, or by the worker coming back online — the dispatch effects below
    // do the latter two explicitly. They have to: once the budget is spent
    // `schedule` returns null, so a reset written inside a retry callback can
    // never run, and the cap would be permanent rather than per-episode.
    const retry = createExponentialBackoff<string>({ initialMs: 500, maxMs: 10_000, maxAttempts: 8 })
    onCleanup(() => retry.cancelAll())

    // candidates carries the per-tab refs alongside the membership SET so the
    // dispatch effect doesn't walk the join a second time just to redo the
    // predicate, and so the `equals` below can compare membership without
    // firing on an unrelated tab field.
    //
    // A set, not a sorted joined string: the join paid an O(n log n) sort plus
    // a string allocation per tick to answer what membership equality answers
    // in O(n) with none — and `ids.join(' ')` compares EQUAL for two different
    // candidate sets whenever an id contains the separator.
    const matches = createMemo<{ ids: Set<string>, candidates: Tab[] }>(() => {
      const candidates: Tab[] = []
      const ids = new Set<string>()
      for (const tab of opts.view.all()) {
        if (!spec.predicate(tab))
          continue
        candidates.push(tab)
        ids.add(tab.id)
      }
      return { ids, candidates }
    }, { ids: new Set(), candidates: [] }, { equals: (a, b) => sameKeys(a.ids, b.ids) })

    function schedulePerTabRetry(tab: Tab): void {
      const tabId = tab.id
      const key = tabKey(tab)
      retry.schedule(tabId, () => {
        const stillPending = opts.view.get(key)
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

    /** Every candidate on `workerId` that still matches the predicate, now. */
    function pendingForWorker(workerId: string): Tab[] {
      const stillPending: Tab[] = []
      for (const tab of opts.view.all()) {
        if (tab.workerId === workerId && spec.predicate(tab))
          stillPending.push(tab)
      }
      return stillPending
    }

    // Worker-keyed retry for batched specs: one timer re-batches every
    // still-pending tab on that worker the next time it fires, instead
    // of N separate single-tab batch RPCs (which would defeat the
    // batching the spec was designed for).
    function scheduleBatchRetry(workerId: string): void {
      retry.schedule(workerId, () => {
        if (spec.kind !== 'batched')
          return
        const stillPending = pendingForWorker(workerId)
        if (stillPending.length === 0) {
          retry.reset(workerId)
          return
        }
        void runForBatch(workerId, stillPending)
      })
    }

    async function runFor(tab: Tab): Promise<void> {
      if (spec.kind !== 'per-tab')
        return
      if (inflight.has(tab.id))
        return
      inflight.add(tab.id)
      const tabId = tab.id
      try {
        await spec.fetch(tab)
        opts.metadata.patch(tabId, { hydrated: true })
        retry.reset(tabId)
      }
      catch {
        schedulePerTabRetry(tab)
      }
      finally {
        inflight.delete(tabId)
      }
    }

    /** One batched fetch for `tabs`, plus what to do with what comes back. */
    async function runForBatch(workerId: string, tabs: Tab[]): Promise<void> {
      if (spec.kind !== 'batched')
        return
      const fresh = tabs.filter(t => !inflight.has(t.id))
      if (fresh.length === 0)
        return
      for (const t of fresh)
        inflight.add(t.id)
      // A reply that answered for only some of the batch is a PARTIAL success,
      // not a success: the omitted tabs are still bare and nothing else will ask
      // again for them. ABSENT is the one verdict worth retiring on -- the
      // worker holds no record, and the candidate set never changes, so a retry
      // loop over those would never stop on its own. An unrecognised verdict (an
      // older worker, a value added later) counts as pending: that is the
      // behaviour this had before verdicts existed, so an unknown answer
      // degrades to the safe old default rather than silently retiring a live tab.
      let pending: Tab[]
      try {
        const result = await spec.fetchBatch(workerId, fresh)
        const verdict = new Map(result.verdicts.map(v => [v.tabId, v.status]))
        for (const t of fresh) {
          if (result.resolved.has(t.id))
            opts.metadata.patch(t.id, { hydrated: true })
        }
        pending = fresh.filter(t =>
          !result.resolved.has(t.id) && verdict.get(t.id) !== TabHydrationStatus.ABSENT)
      }
      catch {
        scheduleBatchRetry(workerId)
        return
      }
      finally {
        // Released only AFTER the metadata writes above, because each write
        // re-runs the candidate effect synchronously: holding the marks across
        // them is what stops the effect from issuing a second RPC for tabs this
        // batch is still answering for.
        for (const t of fresh)
          inflight.delete(t.id)
      }

      if (pending.length === 0) {
        retry.reset(workerId)
        return
      }
      scheduleBatchRetry(workerId)
    }

    // Per-worker candidate membership as of the last dispatch, so the effect can
    // tell WHICH worker's question changed. Without it the budget below could
    // only be restored for every worker at once or for none.
    const lastGroupIds = new Map<string, Set<string>>()

    createEffect(() => {
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
        for (const [wid, group] of byWorker) {
          // Restore the attempt budget when THIS worker's candidate set
          // changes. The retry timer cannot do it: once the budget is spent
          // `schedule` returns null, so the reset that lives in its callback is
          // unreachable and every later failure is a silent no-op. A worker that
          // was unreachable long enough to exhaust its budget, and then has one
          // more tab opened on it, must get a fresh run — otherwise every tab on
          // it keeps the bare `Agent` / `Terminal` label the projection gives it
          // and every terminal keeps its startup spinner, for the life of the
          // page, with no toast (`isExhausted` has no production consumer).
          const ids = new Set(group.map(t => t.id))
          if (!sameKeys(lastGroupIds.get(wid) ?? new Set<string>(), ids))
            retry.reset(wid)
          lastGroupIds.set(wid, ids)
          void runForBatch(wid, group)
        }
        // Forget workers that no longer have candidates, so the next tab opened
        // on one counts as a change rather than matching a stale entry.
        for (const wid of [...lastGroupIds.keys()]) {
          if (!byWorker.has(wid))
            lastGroupIds.delete(wid)
        }
        return
      }
      // Per-tab hydrators key the budget by tab id, so a tab entering the
      // candidate set brings a fresh key with it and needs no explicit reset.
      for (const tab of candidates)
        void runFor(tab)
    })

    // A worker coming back on line re-arms every candidate waiting on it.
    //
    // The other two ways a budget is restored both need something else to
    // happen first — a success, or a change to that worker's candidate set —
    // and neither describes the case this exists for: the worker was
    // unreachable, the attempts ran out, and then it returned with the tab set
    // exactly as it was. Nothing about that is visible to `matches`, so without
    // this the tabs stay bare until the user opens another one on that worker.
    //
    // Keyed on the OFF -> ON transition, not on membership: the hub re-pushes
    // the worker list on every `WORKERS_CHANGED` frame, and a worker that was
    // online through all of them has nothing to retry.
    if (opts.onlineWorkerIds) {
      let wasOnline: ReadonlySet<string> = new Set()
      createEffect(() => {
        const online = opts.onlineWorkerIds!()
        const cameOnline = [...online].filter(id => !wasOnline.has(id))
        wasOnline = online
        if (cameOnline.length === 0)
          return
        // `untrack`: this effect is driven by worker liveness alone. Reading the
        // candidate memo as a dependency would make every tab open re-run it,
        // duplicating the dispatch effect above.
        untrack(() => {
          const { candidates } = matches()
          for (const wid of cameOnline) {
            const group = candidates.filter(t => t.workerId === wid)
            if (group.length === 0)
              continue
            if (spec.kind === 'batched') {
              retry.reset(wid)
              void runForBatch(wid, group)
            }
            else {
              // Per-tab budgets are keyed by tab id, so each candidate on the
              // returning worker needs its own reset.
              for (const tab of group) {
                retry.reset(tab.id)
                void runFor(tab)
              }
            }
          }
        })
      })
    }
  }

  // FILE: GetFileTabPath populates the path/title via worker E2EE. The
  // WatchWorkerPrivateEvents stream's bootstrap reply covers the
  // late-joiner case, but if a tab lands before the private-event
  // stream has finished its bootstrap, we issue a one-shot
  // GetFileTabPath so the title renders without a perceptible delay.
  //
  // `hydrated` ALONE, with no `!tab.filePath` clause and no second cache. The
  // payload sniff was a stand-in for a flag the local open path forgot to set,
  // and `SharedMeta.hydrated`'s own doc rules it out: a predicate keyed on a
  // payload field is forgeable by anything else that writes that field. The
  // separate path cache that used to answer "did the stream already tell us?"
  // is gone too -- the stream marks the tab `hydrated`, because a
  // `FileTabPathRegistered` IS a worker answer for this exact tab carrying the
  // same payload this fetch returns. One flag, one sweep: a cache keyed by tab
  // id but swept on a different schedule than the row could say "already
  // answered" for a row that no longer exists, and nothing would ever ask again.
  createTabHydration({
    kind: 'per-tab',
    predicate: tab => tab.type === TabType.FILE && !isHydrated(tab.id) && Boolean(tab.workerId),
    fetch: async (tab) => {
      // The predicate already requires a non-empty workerId; this
      // re-check narrows the optional field for TypeScript.
      if (!tab.workerId)
        return
      const resp = await getFileTabPath(tab.workerId, { tabId: tab.id })
      // `workingDir` comes along because it is what puts this tab in a branch
      // group and drives the git-status containment match -- a hydrated tab
      // without it falls back to matching on the file's own path, which is a
      // different repo whenever the file was opened from another checkout.
      opts.metadata.patch(tab.id, { filePath: resp.filePath, workingDir: resp.workingDir })
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
    predicate: tab => tab.type === TabType.AGENT && Boolean(tab.workerId) && !isHydrated(tab.id),
    fetchBatch: async (workerId, tabs) => {
      const resp = await listAgents(workerId, { tabIds: tabs.map(t => t.id) })
      const byId = new Map(resp.agents.map(a => [a.id, a]))
      const resolved = new Set<string>()
      for (const tab of tabs) {
        const agent = byId.get(tab.id)
        if (!agent)
          continue
        // protoToAgentTabFields writes every per-agent field onto the
        // tab and primes `settingsLabelCache` with the agent's catalogs.
        // The tab's current git status goes along so a re-hydration of an
        // unchanged repo reuses that object instead of re-keying the tab
        // (and remounting its pane) -- `tabMetadata.patch` drops an
        // equal-but-freshly-decoded payload at the write point, so a
        // re-hydration of an unchanged repo leaves the tab's objects alone
        // without this path having to say anything about it. That also closes
        // the race it used to have: `listAgents` is awaited, and a live status
        // push landing meanwhile would have made the pre-await snapshot a stale
        // basis for the comparison.
        opts.metadata.patch(tab.id, protoToAgentTabFields(workerId, agent))
        resolved.add(tab.id)
      }
      return { resolved, verdicts: resp.verdicts }
    },
  })

  // TERMINAL metadata lives in `tabMetadata`; a tab that arrived via the CRDT
  // projection has only tile/position/worker set. Batched per worker (see AGENT).
  //
  // "Not hydrated yet" is the `hydrated` flag, NOT a sniffed payload field.
  // Keying on `title` was wrong because a shell that emits no OSC title leaves
  // it undefined forever, so the predicate never went false. Keying on `cols`
  // was wrong in the other direction: `handleTerminalResize` patches `cols`
  // locally within a frame of mount, so a tab whose first `listTerminals` had
  // not landed yet was retired from the candidate set before the worker ever
  // answered -- permanently, since the candidate hash then stops changing.
  //
  // DISCONNECTED re-arms the predicate, and that second clause is load-bearing.
  // `hydrated` is write-once, so on its own it means a terminal marked
  // DISCONNECTED by the worker-offline sweep can never be re-asked: the
  // reconnect's `statusChange{READY}` is deliberately ignored for a
  // DISCONNECTED tab ("Preserve DISCONNECTED / EXITED" in
  // `useWorkspaceConnection`), and this hook is the only production caller of
  // `listTerminals`. The tab would stay read-only -- input dropped, restart
  // gated on EXITED -- until a full page reload. DISCONNECTED is precisely the
  // state only the worker can resolve, which is what makes it a hydration
  // trigger rather than a payload sniff; the predicate goes false again as soon
  // as the reply writes a real status.
  createTabHydration({
    kind: 'batched',
    predicate: tab => tab.type === TabType.TERMINAL
      && Boolean(tab.workerId)
      && (!isHydrated(tab.id) || tab.status === TerminalStatus.DISCONNECTED),
    fetchBatch: async (workerId, tabs) => {
      const resp = await listTerminals(workerId, { tabIds: tabs.map(t => t.id) })
      const byId = new Map(resp.terminals.map(t => [t.terminalId, t]))
      const resolved = new Set<string>()
      for (const tab of tabs) {
        const term = byId.get(tab.id)
        if (term) {
          opts.metadata.patch(tab.id, terminalMetadata(workerId, term))
          resolved.add(tab.id)
        }
      }
      return { resolved, verdicts: resp.verdicts }
    },
  })
}
