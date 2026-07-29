import type { AgentTab, FileTab, Tab, TerminalTab } from './tab.types'
import type { TabMetadataStore } from './tabMetadata.store'
import type { UserCrdtState } from '~/generated/leapmux/v1/user_crdt_pb'
import type { Projection, RenderedTab } from '~/lib/crdt'
import { createMemo } from 'solid-js'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { cmpStr, hlcIsZero } from '~/lib/crdt'
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

/**
 * Last placement the projection successfully resolved for a tab.
 *
 * A tab's `tile_id` can name a node this client does not have yet. The CRDT
 * genuinely cannot answer where such a tab belongs — the chain to a root is
 * broken — so we remember where it last resolved and keep rendering it there
 * until it resolves again. Entries are replaced on every successful resolve and
 * dropped when the tab leaves the CRDT, so this never accumulates.
 *
 * The ROUTINE cause of that window is gone. A tile split or make-grid ships the
 * new cell as an `EntityMaterialized` frame and the `SetTabRegister(tile_id)`
 * in a Batch frame, and the hub used to send the batch FIRST — so every peer's
 * split opened a gap where the tab pointed at an unknown node and vanished from
 * the UI. `broadcastBatchToSubscriber` now emits materialized entities before
 * the batch that references them, nodes before tabs.
 *
 * What remains is the genuinely unorderable: a tab naming a tile id this client
 * has no record of at all, because the frame that would explain it has not
 * arrived — frames that crossed different sockets, or a chain still mid-reparent.
 * Holding the last placement keeps those from blanking the UI.
 *
 * A tile TOMBSTONED by another client is deliberately NOT covered, despite
 * looking like the same shape: the node record still exists, so the projection
 * dropped the tab on purpose and holding it would re-assert the tab on a tile
 * the projection says it has left. See the `state.nodes[...] !== undefined`
 * test below, which is what draws that line.
 */
export interface CreateTabViewOpts {
  /**
   * The shared user-wide projection; null before the CRDT bootstrap lands.
   * `project()` returns a fresh object per call, so a plain `createMemo` over it
   * propagates normally.
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
 * Do two `placedTabs` results describe the same tabs in the same places?
 *
 * Every field a `Tab` derives from the projection is compared; anything else on
 * a `Tab` comes from `tabMetadata`, which consumers subscribe to separately.
 * Order is part of the comparison because the projection emits a stable order
 * and a genuine reorder must propagate.
 */
const PLACEMENT_FIELDS = ['tabId', 'tabType', 'tileId', 'position', 'workerId', 'workspaceId'] as const

function samePlacements(a: readonly RenderedTab[], b: readonly RenderedTab[]): boolean {
  if (a.length !== b.length)
    return false
  for (let i = 0; i < a.length; i++) {
    const x = a[i]
    const y = b[i]
    for (const f of PLACEMENT_FIELDS) {
      if (x[f] !== y[f])
        return false
    }
  }
  return true
}

export function createTabView(opts: CreateTabViewOpts) {
  // Survives across ticks by design — see the doc above. Holds the whole
  // `RenderedTab`, not a parallel field-by-field copy of it: a second interface
  // restating the same fields meant a new field on `RenderedTab` had to be
  // added in four places, two of which the compiler could not check.
  const lastResolved = new Map<string, RenderedTab>()

  /**
   * Every tab that should render, tagged with its workspace: the projection's
   * own rows, plus any alive tab the projection could not resolve, held at its
   * last known placement.
   *
   * The `equals` is load-bearing, not an optimization. `state` notifies on
   * EVERY CRDT tick (it must — see `CreateTabViewOpts.state`), and a tile drag
   * or floating-window move fires one batch per coalesced pointermove. Without
   * the structural comparison this memo would hand a fresh array to
   * `byWorkspace` ~50 times a second during a drag, which rebuilds every `Tab`
   * object in every workspace and re-runs every effect reading them — for
   * geometry the tabs do not participate in. The old tab store got this for
   * free by being a fine-grained store; a join has to say it.
   */
  const placedTabs = createMemo<RenderedTab[]>(() => {
    const proj = opts.projection()
    const state = opts.state()
    if (!proj || !state)
      return []

    const out: RenderedTab[] = []
    const resolved = new Set<string>()
    for (const r of proj.renderedTabs) {
      resolved.add(r.tabId)
      lastResolved.set(r.tabId, r)
      out.push(r)
    }

    for (const rec of Object.values(state.tabs)) {
      if (resolved.has(rec.tabId))
        continue
      if (!hlcIsZero(rec.tombstoneAt)) {
        lastResolved.delete(rec.tabId)
        continue
      }
      const held = lastResolved.get(rec.tabId)
      if (!held)
        continue
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
      // Note this tests `rec`'s tile, not `held`'s: `held` is by construction a
      // placement that already resolved, so its node always exists.
      if (state.nodes[rec.tileId?.value ?? ''] !== undefined)
        continue
      // `??`, not `||`: an empty string is a REAL register value here — the hub
      // documents and tests `worker_id: ""` as the "clear" case — so `||` would
      // silently resurrect the stale worker id instead of honouring the clear.
      // The absent-register case is already covered by `?.`.
      out.push({
        ...held,
        userId: proj.userId,
        tabId: rec.tabId,
        workerId: rec.workerId?.value ?? held.workerId,
        position: rec.position?.value ?? held.position,
      })
    }

    // Forget tabs the CRDT no longer has any record of at all.
    for (const tabId of lastResolved.keys()) {
      if (!state.tabs[tabId])
        lastResolved.delete(tabId)
    }
    return out
  }, [], { equals: samePlacements })

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
      gitBranch: m.gitBranch,
      gitOriginUrl: m.gitOriginUrl,
      gitToplevel: m.gitToplevel,
      gitIsWorktree: m.gitIsWorktree,
      gitDiffAdded: m.gitDiffAdded,
      gitDiffDeleted: m.gitDiffDeleted,
      gitDiffUntracked: m.gitDiffUntracked,
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
          agentGitStatus: m.agentGitStatus,
          startupError: m.startupError,
          startupMessage: m.startupMessage,
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
        } satisfies TerminalTab
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
   * All tabs, grouped by workspace. One memo feeds every workspace's view, so
   * the join runs once per tick no matter how many workspaces are on screen.
   */
  /**
   * Last object handed out per tab id, so an unchanged tab keeps its identity.
   *
   * `assemble` builds a fresh object every time it runs, and the consumers
   * iterate with `<For>`, which keys by item IDENTITY. Without this the tab
   * strip and the sidebar tree tear down and re-create every row on any
   * recompute — DOM churn on every keystroke that touches metadata, and a
   * detached element under anything holding a reference (a drag in progress,
   * a `boundingBox()` call, a focused input).
   */
  const assembled = new Map<string, Tab>()

  /** Shallow field-wise equality; `Tab` is a flat record of scalars. */
  function sameTab(a: Tab, b: Tab): boolean {
    const ak = Object.keys(a) as Array<keyof Tab>
    const bk = Object.keys(b) as Array<keyof Tab>
    if (ak.length !== bk.length)
      return false
    for (const k of ak) {
      // `screen` is a Uint8Array; compare by reference, which is what the
      // terminal writer gives us -- it replaces the buffer rather than mutating.
      if (a[k] !== b[k])
        return false
    }
    return true
  }

  /** `assemble`, reusing the previous object when nothing about the tab changed. */
  function assembleStable(r: RenderedTab): Tab {
    const next = assemble(r)
    const prev = assembled.get(r.tabId)
    if (prev && sameTab(prev, next))
      return prev
    assembled.set(r.tabId, next)
    return next
  }

  // PERF: `assemble` below reads `metadata.get(tabId)` — a fine-grained store
  // read — for every tab in the account inside this ONE computation, so a
  // single `metadata.patch` anywhere invalidates the whole join and re-groups
  // every workspace. `assembleStable` blunts the downstream DOM churn, not this
  // walk. Partitioning per workspace is the fix.
  // Tracked: https://github.com/leapmux/leapmux/issues/336
  const byWorkspace = createMemo<Map<string, Tab[]>>(() => {
    // Deliberately does NOT read `opts.state()`: that notifies on every tick,
    // which would defeat `placedTabs`' equality guard. `placedTabs` is already
    // empty when there is no state.
    const grouped = new Map<string, Tab[]>()
    const live = new Set<string>()
    for (const r of placedTabs()) {
      live.add(r.tabId)
      const list = grouped.get(r.workspaceId)
      const tab = assembleStable(r)
      if (list)
        list.push(tab)
      else
        grouped.set(r.workspaceId, [tab])
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
    // Drop cached objects for tabs that left, so this can't grow unbounded.
    for (const id of assembled.keys()) {
      if (!live.has(id))
        assembled.delete(id)
    }
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
