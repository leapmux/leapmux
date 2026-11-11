import type { AgentActivityStore } from '~/stores/agentActivity.store'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabView } from '~/stores/tabView'
import { createComputed, createEffect, createMemo, createSignal, onCleanup, untrack } from 'solid-js'
import { workerAgent, workerPrivate, workerTerminal } from '~/api/workerRpc'
import { TabHydrationStatus, TabType } from '~/generated/proto/leapmux/v1/common_pb'
import { resolveSettingsTabFields } from '~/hooks/agentEvents'
import { createExponentialBackoff } from '~/lib/retry'
import { sameKeys } from '~/lib/sameKeys'
import { tabPayloadView } from '~/lib/tabPayload'
import { migrateErrorHintFromForResolvedRepo, upsertRepoGitFromProtoStatus } from '~/stores/repoGit'
import { keepLiveTerminalStatus, protoToAgentTabFields, tabKey, terminalMetadata, withoutClientHeldTerminalFields, withoutLiveCatalogFields, withoutLiveStatusFields } from '~/stores/tab.helpers'
import { isPayloadBackedTabType } from '~/stores/tab.types'
import { tabPayloadMetadata, watchTabWrites } from '~/stores/tabMetadata.store'

/** No axis is in flight. Shared so the common case allocates nothing. */
const EMPTY_PENDING_AXES: ReadonlySet<string> = new Set()

/**
 * Every tab a hydrator may act on.
 *
 * `view.all()` holds the PLACED tabs, and a quake terminal -- the shell behind
 * a quake panel -- is deliberately absent from it. A hydrator that read only
 * that list would never ask again about a quake terminal after an outage, so
 * the panel would keep a status from before the outage until a page reload.
 */
function hydratableTabsOf(view: TabView) {
  return [
    ...view.all(),
    ...view.detachedTerminalTabs(),
  ]
}

/**
 * The current state of one tab, for a retry that fires later.
 *
 * `view.get` is PLACEMENT-shaped and answers nothing for a quake terminal, so a
 * per-tab retry for one would read "the tab is gone" and cancel itself on its
 * first tick.
 */
function resolveHydratableIn(view: TabView, tab: { type: TabType, id: string }) {
  return tab.type === TabType.TERMINAL
    ? view.getTerminalTab(tab.id)
    : view.get(tabKey(tab))
}

/**
 * Tabs that must be asked again although they are already `hydrated`.
 *
 * The set is what makes the flag re-armable WITHOUT making `hydrated` itself
 * mean something weaker. A batch drops the marks of the tabs it asked about
 * once the worker answers, whatever the reply says, so a tab the worker no
 * longer knows cannot stay armed and re-ask forever. A failed ask keeps them,
 * so the retry asks again.
 */
function createReaskSet() {
  const [ids, setIds] = createSignal<ReadonlySet<string>>(new Set())
  return {
    /** Whether `tabId` waits for a re-ask. Reactive. */
    has: (tabId: string): boolean => ids().has(tabId),
    /** Mark each of `tabIds` for a re-ask. */
    arm: (tabIds: readonly string[]): void => {
      if (tabIds.length === 0)
        return
      setIds(prev => new Set([
        ...prev,
        ...tabIds,
      ]))
    },
    /** Clear the mark of each of `tabIds`. Keeps the same set when none was marked. */
    drop: (tabIds: Iterable<string>): void => {
      setIds((prev) => {
        let next: Set<string> | undefined
        for (const id of tabIds) {
          if (!prev.has(id))
            continue
          next ??= new Set(prev)
          next.delete(id)
        }
        return next ?? prev
      })
    },
  }
}

/** What `useTabHydrators` gives its caller. */
export interface TabHydrators {
  /**
   * Ask `workerId` again for every terminal on it, the hydrated ones included.
   *
   * For a worker that this client reconnected to. The worker replays a status
   * only for a terminal in FULL mode, so a background terminal learns nothing of
   * the outage from the watch stream.
   *
   * The reply leaves alone what this client keeps current by itself: see
   * `withoutClientHeldTerminalFields`.
   */
  reaskWorkerTerminals: (workerId: string) => void
}

/**
 * Per-tab-type hydration of CRDT-projected tabs that arrived without
 * their worker-side metadata (path / agent record / terminal title).
 * The hub strips file paths and agent/terminal payloads from the
 * userevents stream — those live behind E2EE on the worker. Without
 * these hydrators a tab opened by another client (or by the
 * `leapmux control tab open` CLI) renders as a bare CRDT row until the
 * user clicks it.
 *
 * `createTabHydration` factors out the membership-set + in-flight-set
 * + best-effort-fetch pattern shared by the three hydrators. The
 * membership memo re-fires only when the SET of pending tab ids
 * actually changes, so unrelated tab mutations (drag, rename, status
 * update) don't re-walk the tab list.
 */
export interface UseTabHydratorsOpts {
  userId: () => string | undefined
  view: TabView
  metadata: TabMetadataStore
  repoGitStore: ReturnType<typeof createRepoGitStore>
  /**
   * Seeded from `AgentInfo.busy` on every batch reply.
   *
   * This is the ONLY leg that reaches a tab watching in NOTIFY mode, which gets
   * no catch-up replay at all -- so without it a background tab would show no
   * spinner until its agent's next transition, and the close guard would let a
   * working agent go with no warning.
   */
  agentActivityStore: AgentActivityStore
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
  /**
   * In-flight settings axes for an agent, so a re-ask cannot overwrite an
   * optimistic edit the user made while the reply was on its way.
   *
   * The live status handler routes every catalog it applies through
   * `resolveSettingsTabFields` for this reason. The batch below applied the
   * `ListAgents` reply RAW, which was safe only while the batch could never run
   * twice for one tab. It can now (see the worker-online re-ask), so it routes
   * through the same suppression.
   */
  settingsPendingAxes?: (agentId: string) => ReadonlySet<string>
}

export function useTabHydrators(opts: UseTabHydratorsOpts): TabHydrators {
  let current: TabHydrators | undefined
  createComputed(() => {
    const account = opts.userId()
    const accountGeneration = opts.metadata.state.accountGeneration
    if (!account)
      return
    let alive = true
    onCleanup(() => {
      alive = false
      current = undefined
    })
    current = untrack(() => createAccountTabHydrators(opts, () =>
      alive && opts.userId() === account && opts.view.ownerUserId() === account
      && opts.metadata.state.userId === account && opts.metadata.state.accountGeneration === accountGeneration))
  })
  return {
    reaskWorkerTerminals: workerId => current?.reaskWorkerTerminals(workerId),
  }
}

function createAccountTabHydrators(opts: UseTabHydratorsOpts, isCurrent: () => boolean): TabHydrators {
  type Tab = ReturnType<typeof opts.view.all>[number]
  const hydratableTabs = (): Tab[] => isCurrent()
    ? hydratableTabsOf(opts.view)
    : []
  const resolveHydratable = (tab: Tab): Tab | undefined => {
    if (!isCurrent())
      return undefined
    const current = resolveHydratableIn(opts.view, tab)
    return current?.type === tab.type && current.workerId === tab.workerId
      ? current
      : undefined
  }

  const hydrationKey = (tab: Tab): string => JSON.stringify([tab.type, tab.workerId, tab.id])

  /**
   * Has the worker answered for this tab yet?
   *
   * Read straight off the metadata row rather than through the assembled `Tab`
   * so "hydrated" stays a fact about the fetch, independent of which payload
   * fields happened to arrive.
   */
  const isHydrated = (tabId: string): boolean => opts.metadata.get(tabId)?.hydrated === true

  /**
   * Agent tabs whose worker came back and that must be asked again, although
   * they are already `hydrated`.
   *
   * An agent tab has no other re-arm. Without one, its title, status and option
   * catalogs stay at whatever was true before the link dropped, for the life of
   * the page.
   *
   * A re-ask is safe only because the batch now applies the reply through
   * `resolveSettingsTabFields`; see `settingsPendingAxes`.
   */
  const reaskAgents = createReaskSet()

  /**
   * Terminal tabs of a worker that this client reconnected to, to be asked
   * again although they are already `hydrated`. See `reaskWorkerTerminals`.
   */
  const reaskTerminals = createReaskSet()

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
      for (const tab of hydratableTabs()) {
        if (!spec.predicate(tab))
          continue
        candidates.push(tab)
        ids.add(hydrationKey(tab))
      }
      return { ids, candidates }
    }, {
      ids: new Set(),
      candidates: [],
    }, {
      equals: (a, b) => sameKeys(a.ids, b.ids),
    })

    function schedulePerTabRetry(tab: Tab): void {
      const retryKey = hydrationKey(tab)
      if (!isCurrent())
        return
      retry.schedule(retryKey, () => {
        const stillPending = resolveHydratable(tab)
        if (!stillPending || !spec.predicate(stillPending)) {
          // Tab is gone (closed by user, removed by another client,
          // or hydrated by another path). Drop the per-tab delay so
          // the Map doesn't accumulate ghost entries.
          retry.reset(retryKey)
          return
        }
        runFor(stillPending)
      })
    }

    /** Every candidate on `workerId` that still matches the predicate, now. */
    function pendingForWorker(workerId: string): Tab[] {
      const stillPending: Tab[] = []
      for (const tab of hydratableTabs()) {
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
      if (!isCurrent())
        return
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
      if (inflight.has(hydrationKey(tab)))
        return
      inflight.add(hydrationKey(tab))
      const tabId = tab.id
      try {
        await spec.fetch(tab)
        if (!resolveHydratable(tab))
          return
        opts.metadata.patch(tabId, { hydrated: true })
        retry.reset(hydrationKey(tab))
      }
      catch {
        schedulePerTabRetry(tab)
      }
      finally {
        inflight.delete(hydrationKey(tab))
      }
    }

    /** One batched fetch for `tabs`, plus what to do with what comes back. */
    async function runForBatch(workerId: string, tabs: Tab[]): Promise<void> {
      if (spec.kind !== 'batched')
        return
      const fresh = tabs.filter(t => !inflight.has(hydrationKey(t)))
      if (fresh.length === 0)
        return
      for (const t of fresh)
        inflight.add(hydrationKey(t))
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
        if (!isCurrent())
          return
        const verdict = new Map(result.verdicts.map(v => [v.tabId, v.status]))
        for (const t of fresh) {
          if (resolveHydratable(t) && result.resolved.has(t.id))
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
          inflight.delete(hydrationKey(t))
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
        for (const wid of [
          ...lastGroupIds.keys(),
        ]) {
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
    // Keyed on the OFF -> ON transition, not on membership: the client
    // re-fetches the worker list on every `workersChanged` user event and on
    // every user-events bootstrap, and a worker that was online through all of
    // those reads has nothing to retry.
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

  /**
   * Arm a re-ask for every agent tab of a worker that just came back.
   *
   * The generic worker-online effect inside `createTabHydration` resets the
   * retry budget, but it selects from the CANDIDATE set -- and a `hydrated` tab
   * is not a candidate, so it could never reach one. This marks those tabs so
   * the predicate admits them, and the same effect then dispatches the batch.
   *
   * Keyed on the OFF -> ON transition, not on membership: the client re-fetches
   * the worker list on every `workersChanged` user event and on every
   * user-events bootstrap, and a worker that stayed online through all of those
   * reads has nothing to re-ask.
   */
  if (opts.onlineWorkerIds) {
    let wasOnline: ReadonlySet<string> | undefined
    // Whether any reading BEFORE this one yet carried a worker. The workers
    // signal starts EMPTY and fills from the first listWorkers reply, so the
    // load's empty-to-populated step is the FIRST reading in disguise --
    // treating it as a transition armed a re-ask for every agent tab on a
    // page load, the exact outcome the first-reading rule exists to prevent.
    // The guard therefore reads the flag BEFORE the current reading updates
    // it: reading it after the update made `!seenWorkers` dead code, because
    // `seenWorkers === false` implied `online.size === 0` implied an empty
    // `cameOnline` anyway, and the empty-to-populated fill passed straight
    // through. The flag LATCHES and never resets: a set that carried workers
    // and then emptied is a real outage, and its refill IS a transition.
    let seenWorkers = false
    createEffect(() => {
      const online = opts.onlineWorkerIds!()
      const previous = wasOnline
      const seenBefore = seenWorkers
      wasOnline = online
      if (online.size > 0)
        seenWorkers = true
      if (!previous || !seenBefore)
        return
      const cameOnline = [...online].filter(id => !previous.has(id))
      if (cameOnline.length === 0)
        return
      untrack(() => {
        reaskAgents.arm(opts.view.all()
          .filter(t => t.type === TabType.AGENT && t.workerId && cameOnline.includes(t.workerId))
          .map(t => t.id))
      })
    })
  }

  // FILE and IMAGE: GetTabPayload populates whatever the tab shows, via worker
  // E2EE. The WatchWorkerPrivateEvents stream's bootstrap reply covers the
  // late-joiner case, but if a tab lands before the private-event
  // stream has finished its bootstrap, we issue a one-shot
  // GetTabPayload so the title renders without a perceptible delay.
  //
  // One hydrator for both kinds because the fetch is one RPC and the patch is
  // one shared mapping. An IMAGE tab needs this at least as much as a FILE tab
  // does: without the payload it knows no agent, no seq and no index, so it can
  // resolve nothing at all.
  //
  // `hydrated` ALONE, with no `!tab.filePath` clause and no second cache. The
  // payload sniff was a stand-in for a flag the local open path forgot to set,
  // and `SharedMeta.hydrated`'s own doc rules it out: a predicate keyed on a
  // payload field is forgeable by anything else that writes that field. The
  // separate path cache that used to answer "did the stream already tell us?"
  // is gone too -- the stream marks the tab `hydrated`, because a
  // `TabPayloadRegistered` IS a worker answer for this exact tab carrying the
  // same payload this fetch returns. One flag, one sweep: a cache keyed by tab
  // id but swept on a different schedule than the row could say "already
  // answered" for a row that no longer exists, and nothing would ever ask again.
  createTabHydration({
    kind: 'per-tab',
    predicate: tab => isPayloadBackedTabType(tab.type) && !isHydrated(tab.id) && Boolean(tab.workerId),
    fetch: async (tab) => {
      // The predicate already requires a non-empty workerId; this
      // re-check narrows the optional field for TypeScript.
      if (!tab.workerId)
        return
      const landed = watchTabWrites(opts.metadata, [tab.id])
      const resp = await workerPrivate.getTabPayload(tab.workerId, { tabId: tab.id })
      if (!resolveHydratable(tab))
        return
      const payload = tabPayloadView(resp.payload)
      // A payload this client cannot read patches nothing, and the tab is still
      // marked hydrated by `runFor` -- which is correct, however it reads.
      // The worker ANSWERED; asking again would return the same bytes this
      // build still could not decode, so leaving the tab un-hydrated would only
      // spin the retry loop forever.
      //
      // What the tab then shows is the open problem, not this early return.
      // `tabView`'s assembly falls to its FILE branch for a kind it does not
      // know, so such a tab renders as a file viewer with no path, which holds
      // "Loading..." for the life of the session. The app has no
      // unsupported-tab state to render instead. Do NOT restate this as "left
      // un-hydrated on purpose" -- an earlier comment here claimed exactly
      // that, and no such branch has ever existed.
      if (!payload)
        return
      // The patch is `tabPayloadMetadata`, shared with the private-event
      // stream. `workingDir` comes along because it is what puts this tab in a
      // branch group and drives the git-status containment match -- a hydrated
      // tab without it falls back to matching on the file's own path, which is
      // a different repo whenever the file was opened from another checkout.
      const fields = tabPayloadMetadata(payload)
      if (landed.title(tab.id))
        delete fields.title
      opts.metadata.patch(tab.id, fields)
    },
  })

  // AGENT: ListAgents fetches the agent record for tabs that arrived
  // via the CRDT projection without going through this client's local
  // OpenAgent response (e.g. another browser tab, or the
  // `leapmux control tab open` CLI). Without this, AGENT tabs render as
  // "Agent not found." because the tab carries only the CRDT-driven
  // tile/position/worker fields. Batched per worker so opening a
  // workspace with N agents on the same worker costs one ListAgents
  // call instead of N.
  createTabHydration({
    kind: 'batched',
    predicate: tab => tab.type === TabType.AGENT
      && Boolean(tab.workerId)
      && (!isHydrated(tab.id) || reaskAgents.has(tab.id)),
    fetchBatch: async (workerId, tabs) => {
      const landed = watchTabWrites(opts.metadata, tabs.map(t => t.id))
      const activityBefore = new Map(tabs.map(tab => [
        tab.id,
        opts.agentActivityStore.observationEpoch(tab.id),
      ]))
      const resp = await workerAgent.listAgents(workerId, {
        tabIds: tabs.map(t => t.id),
      })
      if (!isCurrent()) {
        return {
          resolved: new Set(),
          verdicts: [],
        }
      }
      const byId = new Map(resp.agents.map(a => [a.id, a]))
      const resolved = new Set<string>()
      // The ask happened, so drop the re-ask marks whatever the reply says. A
      // tab the worker no longer knows must not stay armed and re-ask forever.
      reaskAgents.drop(tabs.map(t => t.id))
      for (const requested of tabs) {
        const tab = resolveHydratable(requested)
        if (!tab)
          continue
        const snapshot = byId.get(tab.id)
        if (!snapshot)
          continue
        const agent = {
          ...snapshot,
          ...(landed.title(tab.id)
            ? { title: '' }
            : {}),
          ...(landed.git(tab.id)
            ? { gitStatus: undefined }
            : {}),
          ...(landed.catalog(tab.id)
            ? { optionGroups: opts.metadata.get(tab.id)?.optionGroups ?? [] }
            : {}),
        }
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
        // NOT the raw mapper's settings fields. `resolveSettingsTabFields` is
        // the same path the live status handler uses, and it keeps a per-axis
        // value the user is editing right now instead of overwriting it with
        // the worker's older answer. A re-ask makes that reachable: this batch
        // can run for a tab that already has in-flight edits.
        // The mapper writes the repo entry itself, from the same status it
        // reads `gitToplevel` from, so the two halves cannot be written apart.
        // This caller holds a LIVE tab, so it is the one that can compute the
        // orphan-migration tip. Hoisted so the spread narrows: the option
        // takes no explicit undefined.
        const migrateFrom = migrateErrorHintFromForResolvedRepo(workerId, tab, agent.gitStatus)
        const fields = protoToAgentTabFields(opts.repoGitStore, workerId, agent, {
          ...(migrateFrom === undefined
            ? {}
            : { migrateErrorHintFrom: migrateFrom }),
        })
        // The reply holds the lifecycle and the catalog as the worker read them
        // before the reply left. A status event or a catalog event that landed since
        // is newer, and nothing sends it again, so the reply keeps what the live
        // stream wrote and gives only the rest. The two kinds count apart: a STARTING
        // event carries no catalog, so the reply catalog still applies after it.
        const lifecycleKept = landed.status(tab.id)
          ? withoutLiveStatusFields(fields)
          : fields
        opts.metadata.patch(tab.id, landed.catalog(tab.id)
          ? withoutLiveCatalogFields(lifecycleKept)
          : {
              ...lifecycleKept,
              ...resolveSettingsTabFields(
                opts.view.getAgentTab(tab.id),
                agent.optionGroups,
                opts.settingsPendingAxes?.(tab.id) ?? EMPTY_PENDING_AXES,
              ),
            })
        // Hydration, not a transition, so it seeds and raises nothing. This
        // batch runs when a tab first appears and on an explicit re-ask, never
        // as a poll, so an agent that settled between the two is not news the
        // user asked for.
        //
        // publishedActivityState, not activityState. The display must match
        // what the live events
        // carry. Seeded with the
        // EXACT value, a tab
        // hydrating inside the
        // Worker's settle window
        // drops its spinner early.
        // If the work then resumes,
        // the Worker derives a state
        // equal to what it already
        // published and broadcasts
        // nothing, so that tab shows
        // no spinner for the rest of
        // the turn. The exact value belongs to the close guard,
        // which fetches it at the moment of the close (see createTabBusyProbe).
        if (opts.agentActivityStore.observationEpoch(tab.id) === activityBefore.get(tab.id))
          opts.agentActivityStore.seedPublished(tab.id, agent.publishedActivityState)
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
  // The second clause asks again about every terminal of a worker that is
  // reachable again; see `reaskWorkerTerminals`.
  createTabHydration({
    kind: 'batched',
    predicate: tab => tab.type === TabType.TERMINAL
      && Boolean(tab.workerId)
      && (!isHydrated(tab.id) || reaskTerminals.has(tab.id)),
    fetchBatch: async (workerId, tabs) => {
      const landed = watchTabWrites(opts.metadata, tabs.map(t => t.id))
      const resp = await workerTerminal.listTerminals(workerId, {
        tabIds: tabs.map(t => t.id),
      })
      if (!isCurrent()) {
        return {
          resolved: new Set(),
          verdicts: [],
        }
      }
      // The ask happened, so drop the re-ask marks whatever the reply says, as
      // the agent batch does.
      reaskTerminals.drop(tabs.map(t => t.id))
      const byId = new Map(resp.terminals.map(t => [t.terminalId, t]))
      const resolved = new Set<string>()
      for (const requested of tabs) {
        const tab = resolveHydratable(requested)
        if (!tab)
          continue
        const snapshot = byId.get(tab.id)
        if (snapshot) {
          const term = {
            ...snapshot,
            ...(landed.git(tab.id)
              ? { gitStatus: undefined }
              : {}),
            ...(landed.title(tab.id)
              ? { title: '' }
              : {}),
          }
          // The reply holds the lifecycle as the worker read it before the reply
          // left. A status event that landed since is newer, and nothing sends it
          // again, so the reply keeps what the live stream wrote and gives only
          // the rest: see `keepLiveTerminalStatus`. The reply also fills only what
          // this client does not hold, read now rather than before the request
          // left: the cursor and the size can change while it is pending.
          const fields = withoutClientHeldTerminalFields(terminalMetadata(workerId, term), opts.metadata.get(tab.id))
          opts.metadata.patch(tab.id, landed.status(tab.id)
            ? keepLiveTerminalStatus(fields)
            : fields)
          // Hoisted so the spread narrows: the option takes no explicit undefined.
          const migrateFrom = migrateErrorHintFromForResolvedRepo(workerId, tab, term.gitStatus)
          upsertRepoGitFromProtoStatus(opts.repoGitStore, workerId, term.gitStatus, {
            ...(migrateFrom === undefined
              ? {}
              : { migrateErrorHintFrom: migrateFrom }),
          })
          resolved.add(tab.id)
        }
      }
      return { resolved, verdicts: resp.verdicts }
    },
  })

  return {
    reaskWorkerTerminals: (workerId) => {
      if (!workerId)
        return
      // `untrack`: a caller can run this inside an effect, and the tab list
      // must not become that effect's dependency.
      untrack(() => {
        reaskTerminals.arm(hydratableTabs()
          .filter(t => t.type === TabType.TERMINAL && t.workerId === workerId)
          .map(t => t.id))
      })
    },
  }
}
