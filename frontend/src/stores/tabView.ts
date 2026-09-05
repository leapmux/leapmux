import type { AgentTab, FileTab, ImageTab, Tab, TerminalTab } from './tab.types'
import type { TabMetadataStore } from './tabMetadata.store'
import type { TabRecord, UserCrdtState } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import type { Projection, RenderedTab } from '~/lib/crdt'
import { createMemo, mapArray } from 'solid-js'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { cmpStr, hlcIsZero } from '~/lib/crdt'
import { shallowEqual, shallowEqualArraysDeep } from '~/lib/shallowEqual'
import { tabKey } from './tab.helpers'

/**
 * The join.
 *
 * A `Tab` is not stored anywhere — it is assembled per tick from the CRDT
 * projection (identity and placement) and `tabMetadata` (everything the CRDT
 * does not carry). Consumers get the same `Tab` shape they always did.
 *
 * This replaces `reconcileFromProjection`: with no second copy there is nothing
 * to drop, add or sync, and a tab cannot drift from the projection because it
 * has no independent existence. Placement is read straight from the projection,
 * so a remote move shows up everywhere at once, in every workspace, with no
 * active/inactive distinction.
 */

export interface CreateTabViewOpts {
  /**
   * The shared user-wide projection; null before the CRDT bootstrap lands.
   *
   * Identity is meaningful: with a `ProjectionCache` wired (production, and
   * `test-support/tabStores`) `project()` hands back the same `renderedTabs`
   * array whenever no tab's placement moved, which is what lets `placedTabs`
   * below skip its walk entirely.
   */
  projection: () => Projection | null
  /**
   * Raw state, for the alive-but-unresolved check.
   *
   * MUST notify on every CRDT tick. `PendingOpsManager` mutates
   * `speculativeState` IN PLACE, so its object identity never changes — a
   * `createMemo(() => bridge.speculativeState())` with default equality
   * short-circuits on `===` and every update after the first is silently
   * swallowed. Build it with `{ equals: false }`, or derive it from the version
   * signal the manager bumps.
   */
  state: () => UserCrdtState | null
  metadata: TabMetadataStore
}

/**
 * The one empty result `placedTabs` hands out, so a run before the CRDT
 * bootstrap lands returns the array Solid already retains rather than a fresh
 * equal one.
 */
const NO_PLACEMENTS: readonly RenderedTab[] = []

/**
 * Do two `placedTabs` results describe the same tabs in the same places?
 *
 * Every field a `Tab` derives from the projection is compared; anything else on
 * a `Tab` comes from `tabMetadata`, which consumers subscribe to separately.
 * Order is part of the comparison because the projection emits a stable order
 * and a genuine reorder must propagate.
 *
 * Through the shared element-wise comparator rather than a local field list: a
 * `RenderedTab` is exactly its projection-derived fields, so a hand-maintained
 * list of them was a restatement of the type that a NEW field would silently
 * fall out of -- compared nowhere, and so invisible to this guard. `shallowEqual`
 * checks key count first, so it tracks the type by construction.
 */
function samePlacements(a: readonly RenderedTab[], b: readonly RenderedTab[]): boolean {
  // `shallowEqualArraysDeep` short-circuits on `a === b` first, which is
  // reachable and the common case: `placedTabs` returns its previous array
  // outright when the projection's rows did not move.
  return shallowEqualArraysDeep(a, b)
}

export function createTabView(opts: CreateTabViewOpts) {
  /**
   * Last placement rendered for a tab, whether the projection resolved it or
   * this store held it in place.
   *
   * A tab's `tile_id` can name a node this client does not have yet. The CRDT
   * genuinely cannot answer where such a tab belongs — the chain to a root is
   * broken — so we remember where it last resolved and keep rendering it there
   * until it resolves again. Entries are replaced on every successful resolve
   * and dropped on any of three things: the tab is tombstoned, it leaves the
   * CRDT outright, or the projection shows it has MOVED to a tile this client
   * can see (see `takeCandidate` — that last one is what stops a stale
   * placement being resurrected a tick later). So this never accumulates — see
   * `lastWalk` for why the fast path cannot skip that sweep on a tick that
   * needs it.
   *
   * Holds the whole `RenderedTab`, not a parallel field-by-field copy of it: a
   * second interface restating the same fields meant a new field on
   * `RenderedTab` had to be added in four places, two of which the compiler
   * could not check. A HELD row is stored back here too, so the next tick that
   * still holds the tab reuses that exact object instead of minting an equal
   * one — `mapArray` keys by reference, so a fresh object would remount the
   * tab's row.
   *
   * The ROUTINE cause of the unresolved window is gone. A tile split or
   * make-grid ships the new cell as an `EntityMaterialized` frame and the
   * `SetTabRegister(tile_id)` in a Batch frame, and the hub used to send the
   * batch FIRST — so every peer's split opened a gap where the tab pointed at
   * an unknown node and vanished from the UI. `broadcastBatchToSubscriber` now
   * emits materialized entities before the batch that references them, nodes
   * before tabs.
   *
   * What remains is the genuinely unorderable: a tab naming a tile id this
   * client has no record of at all, because the frame that would explain it has
   * not arrived — frames that crossed different sockets, or a chain still
   * mid-reparent. Holding the last placement keeps those from blanking the UI.
   *
   * A tile TOMBSTONED by another client is deliberately NOT covered, despite
   * looking like the same shape: the node record still exists, so the projection
   * dropped the tab on purpose and holding it would re-assert the tab on a tile
   * the projection says it has left. See the `state.nodes[...] !== undefined`
   * test below, which is what draws that line.
   */
  const lastResolved = new Map<string, RenderedTab>()

  /**
   * What the last walk saw: the projection rows it ran against, and the ids of
   * the hold-in-place CANDIDATES it found — alive tabs absent from
   * `renderedTabs` that `lastResolved` still remembers a placement for, whether
   * or not they ended up held.
   *
   * Carrying the ids rather than a count is what lets a tick whose rows did not
   * move re-check just those tabs instead of rescanning the account. Null before
   * the first walk and whenever the projection goes away.
   */
  let lastWalk: { rows: readonly RenderedTab[], candidates: readonly string[] } | null = null

  /**
   * Every tab that should render, tagged with its workspace: the projection's
   * own rows, plus any alive tab the projection could not resolve, held at its
   * last known placement.
   *
   * The `equals` is load-bearing, not an optimization. `state` notifies on
   * EVERY CRDT tick (it must — see `CreateTabViewOpts.state`), and a tile drag
   * or floating-window move fires one batch per coalesced pointermove. Without
   * the structural comparison this memo would hand a fresh array to `assembled`
   * ~50 times a second during a drag; `mapArray` reuses each row's computation
   * by reference, so no `Tab` would be rebuilt, but its own output array is
   * fresh on every run, so `byWorkspace` would re-group and re-sort every tab in
   * the account and `byKey` / `byTile` would rebuild behind it — for geometry
   * the tabs do not participate in. The old tab store got this for free by being
   * a fine-grained store; a join has to say it.
   *
   * `prev` is the value Solid RETAINED, i.e. the array this memo last handed
   * out — when `equals` holds, Solid keeps the old value and discards the new
   * one. Returning `prev` on the fast paths is therefore exact, and there is no
   * second copy of "what we last returned" to keep in step.
   */
  const placedTabs = createMemo<readonly RenderedTab[]>((prev) => {
    // BOTH accessors are read before any early return, and that is load-bearing:
    // Solid re-collects a memo's dependencies on every run, so returning before
    // reading `state` would unsubscribe this memo from the CRDT and it would
    // never run again.
    const proj = opts.projection()
    const state = opts.state()
    if (!proj || !state) {
      // Forget the last walk entirely: what this memo returns now is empty, so
      // the fast paths below may not hand back a candidate list from before the
      // gap.
      lastWalk = null
      return NO_PLACEMENTS
    }

    const rows = proj.renderedTabs
    // A tab can only BECOME a hold candidate on a tick where `renderedTabs`
    // changed — a brand-new tab has no `lastResolved` entry, and an existing tab
    // can only stop being rendered by leaving `renderedTabs`. So while the
    // projection hands back the same rows, the last walk's candidate list is
    // still the complete set, and the account-wide scan below has nothing left
    // to find: every `lastResolved` entry names a tab that is either in `rows`
    // or in that list.
    const reuse = lastWalk?.rows === rows ? lastWalk : null
    // Re-checking the candidates is still required on every tick, which is why
    // the gate is "no candidates" and not "nothing was held". A candidate whose
    // tile is currently KNOWN is not held, and it starts being held the moment
    // that tile's record is deleted (`EntityRemoved`) — which need not touch
    // `renderedTabs` at all, because the tab had already been dropped from it.
    // The same tick is where a held tab picks up a `worker_id` / `position`
    // write, and where a tab that left the CRDT outright is swept from
    // `lastResolved`.
    if (reuse && reuse.candidates.length === 0)
      return prev

    // Held rows are collected separately so the common case -- a walk that holds
    // nothing -- can hand back the projection's own array untouched instead of
    // copying it.
    const held: RenderedTab[] = []
    const candidates: string[] = []

    /** Decide whether one alive, unrendered tab is held, and with which row. */
    const takeCandidate = (rec: TabRecord): void => {
      if (!hlcIsZero(rec.tombstoneAt)) {
        lastResolved.delete(rec.tabId)
        return
      }
      const previous = lastResolved.get(rec.tabId)
      if (!previous)
        return
      candidates.push(rec.tabId)
      // Hold ONLY when the tab's CURRENT tile id names a node this client has
      // no record of.
      //
      // "Alive and absent from `renderedTabs`" is far broader than the window
      // this exists for, and the extra cases are ones the projection dropped
      // ON PURPOSE: the tile is a live SPLIT/GRID rather than a leaf, its chain
      // was tombstoned by a peer, or its workspace record is gone. Holding
      // those overrides the projection with a stale placement and re-asserts a
      // tab on a tile the projection says it is not on. An unknown tile id is
      // the genuinely unorderable case — the frame that would explain it has
      // not arrived yet — and it is the only one worth compensating for here.
      //
      // Note this tests `rec`'s tile, not the remembered row's: that row is by
      // construction a placement that already resolved, so its node exists.
      const tileId = rec.tileId?.value ?? ''
      if (state.nodes[tileId] !== undefined) {
        // Not held -- and if the tab has MOVED since we remembered it, the
        // memory is not merely unused here, it is now WRONG, so forget it.
        //
        // The two cases differ by whether `rec` still names the tile the entry
        // was recorded for. Same tile means the tab has not gone anywhere; the
        // tile just stopped owning it (it became a SPLIT, or a peer tombstoned
        // its chain), and if that record is later deleted the remembered
        // placement is still the tab's own last confirmed one -- which is the
        // transition the candidate re-check exists to catch.
        //
        // A DIFFERENT tile means the projection has told us where the tab went,
        // and it is not where we remember. Keeping the entry lets a later
        // deletion of that new tile resurrect the old placement: the tab
        // reappears on a tile -- possibly in a WORKSPACE -- it demonstrably
        // left, several ticks stale. That is the same "override the projection
        // with a stale placement" this gate exists to refuse; it just takes one
        // extra tick to bite. Forgetting leaves the tab unrendered instead,
        // which is the honest answer: its tile is gone and nothing this client
        // holds says where it belongs now.
        if (tileId !== previous.tileId)
          lastResolved.delete(rec.tabId)
        return
      }
      // `??`, not `||`: an empty string is a REAL register value here — the hub
      // documents and tests `worker_id: ""` as the "clear" case — so `||` would
      // silently resurrect the stale worker id instead of honouring the clear.
      // The absent-register case is already covered by `?.`.
      const workerId = rec.workerId?.value ?? previous.workerId
      const position = rec.position?.value ?? previous.position
      // Keep the row we handed out last time when nothing on it moved, and store
      // every held row back into `lastResolved` so the next tick can do the
      // same. `assembled` maps over this array with `mapArray`, which keys each
      // tab's computation by item REFERENCE — a fresh-but-identical object would
      // dispose that computation and remount the tab's `<For>` row on every tick
      // it stays held, dropping an in-flight drag or a focused rename input.
      const row = workerId === previous.workerId && position === previous.position
        ? previous
        : { ...previous, workerId, position }
      lastResolved.set(rec.tabId, row)
      held.push(row)
    }

    if (reuse) {
      for (const tabId of reuse.candidates) {
        const rec = state.tabs[tabId]
        // The tab left the CRDT outright (`EntityRemoved`). On a rescan the
        // sweep below catches this; here the candidate list is the only thing
        // that still names it.
        if (rec)
          takeCandidate(rec)
        else
          lastResolved.delete(tabId)
      }
    }
    else {
      const resolved = new Set<string>()
      for (const r of rows) {
        resolved.add(r.tabId)
        lastResolved.set(r.tabId, r)
      }
      for (const rec of Object.values(state.tabs)) {
        if (!resolved.has(rec.tabId))
          takeCandidate(rec)
      }
      // Forget tabs the CRDT no longer has any record of at all. Only the rescan
      // needs this: it is the one branch that can see a `lastResolved` entry the
      // loop above never visited, because the loop is driven by `state.tabs`.
      for (const tabId of lastResolved.keys()) {
        if (!state.tabs[tabId])
          lastResolved.delete(tabId)
      }
    }

    lastWalk = { rows, candidates }
    // Nothing held: hand back the projection's own array. It is `readonly` and
    // nothing here mutates it, and reusing it means a walk that found no hold
    // allocates nothing at all.
    return held.length === 0 ? rows : [...rows, ...held]
  }, NO_PLACEMENTS, { equals: samePlacements })

  /** Assemble one `Tab` from its projected placement plus its metadata. */
  function assemble(r: RenderedTab): Tab {
    const m = opts.metadata.get(r.tabId) ?? {}
    const base = {
      id: r.tabId,
      workspaceId: r.workspaceId,
      tileId: r.tileId,
      position: r.position,
      workerId: r.workerId || undefined,
      title: m.title,
      hasNotification: m.hasNotification,
      mru: m.mru,
      workingDir: m.workingDir,
      createdAt: m.createdAt,
      gitToplevel: m.gitToplevel,
    }
    switch (r.tabType) {
      case TabType.AGENT:
        return {
          ...base,
          type: TabType.AGENT,
          agentProvider: m.agentProvider,
          agentStatus: m.agentStatus,
          agentSessionId: m.agentSessionId,
          optionValues: m.optionValues,
          optionGroups: m.optionGroups,
          startupError: m.startupError,
          startupMessage: m.startupMessage,
          parentAgentId: m.parentAgentId,
          acceptsMessages: m.acceptsMessages,
          supportsSteering: m.supportsSteering,
          rootAgentId: m.rootAgentId,
        } satisfies AgentTab
      case TabType.TERMINAL:
        return {
          ...base,
          type: TabType.TERMINAL,
          status: m.terminalStatus,
          shellStartDir: m.shellStartDir,
          screen: m.screen,
          cols: m.cols,
          rows: m.rows,
          contentReady: m.contentReady,
          startupError: m.startupError,
          startupMessage: m.startupMessage,
          ptyTitle: m.ptyTitle,
          progressState: m.progressState,
          progressPercent: m.progressPercent,
        } satisfies TerminalTab
      case TabType.IMAGE:
        return {
          ...base,
          type: TabType.IMAGE,
          imageAgentId: m.imageAgentId,
          imageSeq: m.imageSeq,
          imageIndex: m.imageIndex,
        } satisfies ImageTab
      default:
        return {
          ...base,
          type: TabType.FILE,
          filePath: m.filePath,
          // Not from the record: TabRecord has registers for these, but nothing
          // has ever emitted them -- see the note in tabMetadata.store.ts.
          displayMode: m.displayMode,
          fileViewMode: m.fileViewMode,
          fileDiffBase: m.fileDiffBase,
          fileOpenSource: m.fileOpenSource,
        } satisfies FileTab
    }
  }

  /**
   * One computation per placed tab.
   *
   * `assemble` reads `metadata.get(tabId)` plus ~20 tracked fields, so running
   * it for every tab in the account inside ONE computation made a single
   * `metadata.patch` anywhere — an agent status flip, a git-status refresh, an
   * MRU touch — re-assemble and re-compare every live tab in every workspace
   * (measured 0.247ms at 100 tabs, 0.750ms at 300). Scoped per tab, a patch
   * re-runs exactly one of these.
   *
   * `mapArray` keys by item REFERENCE, which is exactly right here because
   * `project()`'s `ProjectionCache` keeps a `RenderedTab` reference stable for as
   * long as the placement is: an untouched tab's computation survives, and a tab
   * whose placement moved gets a fresh one, which is the only case where its
   * `Tab` genuinely differs. `mapArray` also runs each mapping under its own
   * root and disposes it when the item leaves, which retires the hand-rolled
   * cache-plus-eviction this used to carry. A HELD tab keeps its reference for
   * the same reason — see `lastResolved`, which is where that half is paid for.
   *
   * `equals: shallowEqual` is what keeps a tab's object identity across
   * recomputes. `Tab` is a flat record, and its two non-scalar fields are meant
   * to be compared by reference: `screen` is a `Uint8Array` the terminal writer
   * REPLACES rather than mutates, and `optionGroups` is rebuilt per patch.
   * The consumers iterate with `<For>`, which keys by item IDENTITY, so without
   * it the tab strip and the sidebar tree tear down and re-create every row on
   * any recompute — DOM churn on every keystroke that touches metadata, and a
   * detached element under anything holding a reference (a drag in progress, a
   * `boundingBox()` call, a focused input).
   *
   * Scoping the remaining group-and-sort per workspace would be a further step,
   * and is deliberately NOT taken: what it saves is O(tabs) pointer work (tens
   * of microseconds), it does nothing for the single-workspace user whose active
   * workspace holds every tab, and it costs a second keyed map plus a
   * per-workspace memo registry. It composes on top if a profile ever asks.
   */
  const assembled = createMemo(mapArray(
    placedTabs,
    r => createMemo(() => assemble(r), undefined, { equals: shallowEqual }),
  ))

  /**
   * All tabs, grouped by workspace. One memo feeds every workspace's view, so
   * the grouping runs once per tick no matter how many workspaces are on screen.
   */
  const byWorkspace = createMemo<Map<string, Tab[]>>(() => {
    // Deliberately does NOT read `opts.state()`: that notifies on every tick,
    // which would defeat `placedTabs`' equality guard. `placedTabs` is already
    // empty when there is no state.
    const grouped = new Map<string, Tab[]>()
    for (const read of assembled()) {
      const tab = read()
      const list = grouped.get(tab.workspaceId)
      if (list)
        list.push(tab)
      else
        grouped.set(tab.workspaceId, [tab])
    }
    // `cmpStr` (bytewise), NOT `localeCompare`: both keys here are ordered by
    // code point everywhere else in the system, and Intl collation disagrees.
    //
    //   - `position` is a LexoRank over `a`-`z`. Under a Lithuanian or Latvian
    //     collation `localeCompare` sorts `y` before `j`..`x`, so a rank
    //     containing `y` renders left of its own predecessors.
    //   - `id` is a MIXED-CASE nanoid, and case-insensitive collation reorders
    //     it in EVERY locale: `'Zx7QpLm'.localeCompare('ax7QpLm')` is 1 while
    //     `'Zx7QpLm' < 'ax7QpLm'` is true.
    //
    // `ops.ts`'s `liveTabsOnTile` sorts the same pair bytewise and its doc
    // promises "the visible order is identical before and after" — a promise
    // only this comparator can keep.
    for (const list of grouped.values())
      list.sort((a, b) => cmpStr(a.position ?? '', b.position ?? '') || cmpStr(a.id, b.id))
    return grouped
  })

  const byKey = createMemo<Map<string, Tab>>(() => {
    const m = new Map<string, Tab>()
    for (const list of byWorkspace().values()) {
      for (const t of list)
        m.set(tabKey(t), t)
    }
    return m
  })

  const byTile = createMemo<Map<string, Tab[]>>(() => {
    const m = new Map<string, Tab[]>()
    for (const list of byWorkspace().values()) {
      for (const t of list) {
        if (!t.tileId)
          continue
        const arr = m.get(t.tileId)
        if (arr)
          arr.push(t)
        else
          m.set(t.tileId, [t])
      }
    }
    return m
  })

  return {
    /** Tabs for one workspace, in position order. Empty for an unknown id. */
    forWorkspace(workspaceId: string): Tab[] {
      return byWorkspace().get(workspaceId) ?? []
    },
    /** Tabs anchored to one tile, in position order. */
    forTile(tileId: string): Tab[] {
      return byTile().get(tileId) ?? []
    },
    get(key: string): Tab | undefined {
      return byKey().get(key)
    },
    getById(type: TabType, id: string): Tab | undefined {
      return byKey().get(tabKey({ type, id }))
    },
    /**
     * Narrowed lookups. Callers that only make sense for one tab kind — agent
     * status handlers, terminal screen writers — get the narrow type instead of
     * casting at each site.
     */
    getAgentTab(id: string): AgentTab | undefined {
      const t = byKey().get(tabKey({ type: TabType.AGENT, id }))
      return t && t.type === TabType.AGENT ? t : undefined
    },
    getTerminalTab(id: string): TerminalTab | undefined {
      const t = byKey().get(tabKey({ type: TabType.TERMINAL, id }))
      return t && t.type === TabType.TERMINAL ? t : undefined
    },
    getFileTab(id: string): FileTab | undefined {
      const t = byKey().get(tabKey({ type: TabType.FILE, id }))
      return t && t.type === TabType.FILE ? t : undefined
    },
    getImageTab(id: string): ImageTab | undefined {
      const t = byKey().get(tabKey({ type: TabType.IMAGE, id }))
      return t && t.type === TabType.IMAGE ? t : undefined
    },
    /** Every tab across every workspace — for worker-scoped sweeps. */
    all(): Tab[] {
      return [...byKey().values()]
    },
    /** Most-recently-used first, within one workspace. */
    mruOrder(workspaceId: string): Tab[] {
      return [...this.forWorkspace(workspaceId)].sort((a, b) => (b.mru ?? 0) - (a.mru ?? 0))
    },
  }
}

export type TabView = ReturnType<typeof createTabView>
