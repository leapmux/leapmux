import type { Tab } from './tab.types'
import type { TabMetadataStore } from './tabMetadata.store'
import type { TabView } from './tabView'
import type { TabType } from '~/generated/leapmux/v1/workspace_pb'
import type { Projection } from '~/lib/crdt'
import { createEffect, createMemo } from 'solid-js'
import { createStore, produce } from 'solid-js/store'
import { sameKeys } from '~/lib/sameKeys'
import { tabKey } from './tab.helpers'

/**
 * Which tab is selected, where.
 *
 * Deliberately not in the CRDT: `layout.store.ts` states the rule for its
 * sibling — "focus is per-client, not synced" — and the same holds here. Two
 * devices viewing one workspace should not fight over which tab is in front.
 *
 * Two maps, keyed differently on purpose:
 *
 *   - `activeByWorkspace` — per workspace, because "the active tab" is only
 *     meaningful within one.
 *   - `activeByTile` — keyed by tile id, which is a globally-unique CRDT node
 *     id. It therefore holds every workspace's tiles at once with no workspace
 *     dimension at all, the same way `zOrder` and `focusByWindow` do in
 *     `floatingWindow.store.ts`.
 *
 * Both survive a workspace switch in memory, which is new — they used to ride
 * along in the registry snapshot. Across a page RELOAD they are restored from
 * sessionStorage by `useTabPersistence`, which remains the only carrier for
 * state the CRDT will never hold.
 *
 * Both maps store a tab KEY, and the tab it names can disappear underneath
 * them at any moment: closed here, closed on another device, or tombstoned by a
 * peer's op. None of those paths write to this store — a tombstone is a CRDT
 * op, not a store call — so the pointers are healed at READ time against the
 * projection instead. See {@link resolve}.
 */
export interface TabSelectionState {
  activeByWorkspace: Record<string, string | null>
  activeByTile: Record<string, string | null>
}

export function createTabSelectionStore(view: TabView, metadata: TabMetadataStore) {
  const [state, setState] = createStore<TabSelectionState>({
    activeByWorkspace: {},
    activeByTile: {},
  })

  /** Highest-MRU tab in `tabs`, or null if empty. */
  function mruHead(tabs: readonly Tab[]): string | null {
    if (tabs.length === 0)
      return null
    let best = tabs[0]
    for (const t of tabs) {
      if ((t.mru ?? 0) > (best.mru ?? 0))
        best = t
    }
    return tabKey(best)
  }

  /**
   * A stored pointer, healed.
   *
   * Returns `storedKey` when it still names a live tab that STILL BELONGS to
   * the scope the pointer is filed under; otherwise the highest-MRU tab from
   * `survivors` — the same promotion the old tab store did imperatively inside
   * `removeTab`.
   *
   * `belongs` is what makes this a real heal rather than a liveness check.
   * `view.get` is a global lookup across every workspace, so a tab that merely
   * MOVED — drag to another tile, pop out to a floating window, cross-workspace
   * drag — is still "live" and would keep satisfying the pointer of the tile or
   * workspace it left. The old store checked exactly this (`tab.tileId !==
   * tileId` in `getActiveTabKeyForTile`) and dropping it left the source tile
   * rendering a tab that is no longer on it: no pane matches, so the tile goes
   * blank with its tab bar still populated and nothing highlighted.
   *
   * Healing on read rather than on write is deliberate. The alternative is a
   * `releaseFromTile`-style call that every close, move, tombstone and remote-op
   * path has to remember, and the one that forgets leaves the shell rendering
   * an empty pane with tabs still in the bar. Reading through the projection
   * makes the stale state unrepresentable instead of merely unlikely.
   *
   * Deliberately pure: repairing `state` here would be a write hidden inside a
   * getter, firing during render. The stored value is corrected the next time
   * something calls `setActive`.
   */
  function resolve(
    storedKey: string | null | undefined,
    belongs: (tab: Tab) => boolean,
    survivors: () => readonly Tab[],
  ): string | null {
    if (storedKey) {
      const tab = view.get(storedKey)
      if (tab && belongs(tab))
        return storedKey
    }
    return mruHead(survivors())
  }

  /**
   * The two writes that accompany EVERY activation, whatever its scope: stamp
   * MRU so the tab wins the next promotion, and dismiss the badge the user has
   * now seen. Keeping them together is what stops a caller from selecting a tab
   * without recording that it was touched.
   */
  function markTouched(tab: Tab) {
    metadata.touchMru(tab.id)
    // Only when set, so subscribers aren't woken for a no-op write.
    if (metadata.get(tab.id)?.hasNotification)
      metadata.patch(tab.id, { hasNotification: false })
  }

  return {
    state,

    activeKeyForWorkspace(workspaceId: string): string | null {
      return resolve(
        state.activeByWorkspace[workspaceId],
        tab => tab.workspaceId === workspaceId,
        () => view.forWorkspace(workspaceId),
      )
    },

    activeTabForWorkspace(workspaceId: string): Tab | undefined {
      const key = this.activeKeyForWorkspace(workspaceId)
      return key ? view.get(key) : undefined
    },

    activeKeyForTile(tileId: string): string | null {
      return resolve(
        state.activeByTile[tileId],
        tab => tab.tileId === tileId,
        () => view.forTile(tileId),
      )
    },

    activeTabForTile(tileId: string): Tab | undefined {
      const key = this.activeKeyForTile(tileId)
      return key ? view.get(key) : undefined
    },

    /**
     * Select a tab. The workspace and tile are read off the tab itself — it
     * carries both, resolved by the projection — so callers never have to say
     * which workspace they mean.
     *
     * Activating is three writes, not one: the two pointers, the MRU stamp, and
     * the notification-dot clear. The old store's `setActiveTab` /
     * `setActiveTabForTile` / `activateTab` each did all three; keeping them
     * together here is what stops a caller from selecting a tab without
     * recording that it was touched (which silently degrades every MRU consumer
     * to position order) or without dismissing the badge the user just read.
     */
    setActive(tab: Tab) {
      const key = tabKey(tab)
      setState(produce((s) => {
        s.activeByWorkspace[tab.workspaceId] = key
        if (tab.tileId)
          s.activeByTile[tab.tileId] = key
      }))
      markTouched(tab)
    },

    /**
     * Make `tab` the active tab OF ONE TILE, without claiming the workspace.
     *
     * The distinction is not cosmetic, which is why it needs its own entry
     * point rather than being folded into {@link setActive}. Placing a tab on a
     * tile the user is not looking at — dragging a background tab from one
     * background tile to another — must show it on the destination tile without
     * reassigning which tab the WORKSPACE considers active. The workspace
     * pointer feeds the notification-badge suppression check, `activeFilePath`,
     * and the provider seed for a newly-opened agent, so stealing it silently
     * badges the tab the user IS reading and seeds new agents from the wrong
     * one. The store this replaced drew the same line: `setActiveTabForTile`
     * wrote only the tile pointer, and `setActiveTab` wrote both.
     *
     * `tileId` is passed explicitly rather than read off `tab.tileId` so the
     * caller can name the DESTINATION of a move it has just emitted.
     */
    setActiveInTile(tab: Tab, tileId: string) {
      setState(produce((s) => {
        s.activeByTile[tileId] = tabKey(tab)
      }))
      markTouched(tab)
    },

    /**
     * Claim `tileId`'s pointer for `tab`, but only if the tile has none.
     *
     * The close flows all end the same way: a tile inherits tabs from one being
     * destroyed, and the source's active tab should surface on the heir — unless
     * the heir already has a selection of its own, which is a choice the user
     * made and a merge must not overwrite. That "only if unclaimed" rule was
     * copied into three callbacks in `TileRenderer`, each reaching into
     * `state.activeByTile` directly to check it. It belongs with the pointers.
     *
     * Tile pointer only, deliberately — see {@link setActiveInTile}. A close
     * path that should ALSO claim the workspace has to say so explicitly rather
     * than pass a flag, because whether focus follows the merge target is
     * different per flow and the reasoning is worth reading at the call site.
     */
    claimTileIfUnclaimed(tab: Tab | undefined, tileId: string): boolean {
      if (!tab || state.activeByTile[tileId])
        return false
      setState(produce((s) => {
        s.activeByTile[tileId] = tabKey(tab)
      }))
      markTouched(tab)
      return true
    },

    setActiveById(type: TabType, id: string) {
      const tab = view.getById(type, id)
      if (tab)
        this.setActive(tab)
    },

    /**
     * Drop every pointer whose workspace or tile the projection no longer has.
     *
     * Reclamation is a SWEEP, not a per-caller duty, for the same reason
     * `resolve` heals on read: a rule enforced at seven call sites is a rule
     * with seven chances to be forgotten, and one of them always is. The
     * caller-driven version ran from exactly one site (the local delete), so a
     * workspace or tile closed on ANOTHER device -- a pure CRDT tombstone with
     * no local handler -- leaked its entries for the life of the page. It was
     * also a no-op in its own best case: it read the tile ids AFTER awaiting
     * the delete RPC and two list refreshes, by which point the hub's tombstone
     * had usually already removed the workspace from the projection, so the
     * lookup returned nothing to clean.
     *
     * `liveWorkspaces`/`liveTiles` must be the FULL live sets, and the caller
     * must not invoke this before the CRDT bootstrap lands -- see
     * `useSelectionSweep`, which owns that gate. Sweeping against an empty
     * projection would wipe a pointer `restoreTabSelection` had just restored,
     * which it cannot restore twice.
     */
    retainOnly(liveWorkspaces: ReadonlySet<string>, liveTiles: ReadonlySet<string>) {
      const wsIds = Object.keys(state.activeByWorkspace).filter(id => !liveWorkspaces.has(id))
      const tIds = Object.keys(state.activeByTile).filter(id => !liveTiles.has(id))
      if (wsIds.length === 0 && tIds.length === 0)
        return
      setState(produce((s) => {
        for (const id of wsIds)
          delete s.activeByWorkspace[id]
        for (const id of tIds)
          delete s.activeByTile[id]
      }))
    },

    /**
     * Per-tile actives limited to `tileIds`. `activeByTile` spans every
     * workspace (tile ids are global), so a caller persisting one workspace's
     * slice must narrow it or it writes other workspaces' tiles under that
     * workspace's key.
     */
    tileActivesFor(tileIds: Iterable<string>): Record<string, string | null> {
      const out: Record<string, string | null> = {}
      for (const id of tileIds) {
        const key = state.activeByTile[id]
        if (key)
          out[id] = key
      }
      return out
    },

    /**
     * Restore pointers read back from sessionStorage on a page reload.
     *
     * A workspace-level key also points its tab's OWN TILE at it, when the tab
     * still resolves. The two maps answer different questions -- the tab bar
     * reads per-tile, the shell reads per-workspace -- and a caller that knows
     * only "this tab was active" (the sidebar's click-into-another-workspace
     * path writes exactly that) would otherwise restore a workspace pointer
     * while every tile still fell through to its MRU head, landing the user on
     * a different tab than the one they clicked.
     *
     * Explicit `tileActives` entries are applied on top, so a caller that knows
     * both still wins.
     */
    restore(workspaceId: string, activeKey: string | null, tileActives: Record<string, string | null>) {
      const stored = activeKey ? view.get(activeKey) : undefined
      // `view.get` is a GLOBAL lookup, so a stored key can name a tab that has
      // since moved to another workspace (a cross-workspace move here, or one
      // from another device, outliving the reload in sessionStorage). Writing
      // its tile pointer would make THAT workspace's tile jump to this tab. The
      // same `belongs` discipline `resolve` applies on read.
      const activeTab = stored?.workspaceId === workspaceId ? stored : undefined
      setState(produce((s) => {
        if (activeKey)
          s.activeByWorkspace[workspaceId] = activeKey
        if (activeTab?.tileId)
          s.activeByTile[activeTab.tileId] = activeKey
        for (const [tileId, key] of Object.entries(tileActives))
          s.activeByTile[tileId] = key
      }))
    },
  }
}

export type TabSelectionStore = ReturnType<typeof createTabSelectionStore>

/**
 * Keep `tabSelection` free of pointers to workspaces and tiles the projection
 * no longer has.
 *
 * The counterpart to `resolve`'s heal-on-read. Reading is already safe — a
 * dangling pointer resolves to a live tab or to null — so this is purely
 * reclamation, and it is a sweep for the same reason the read is healed rather
 * than the writes policed: a per-caller duty is one a caller eventually
 * forgets. The version this replaced was called from exactly one site, so a
 * workspace or tile closed on ANOTHER device leaked its entries permanently.
 *
 * Gated on the projection being non-null, which is what makes it safe to run
 * against `activeByWorkspace`: before the bootstrap lands there are no live
 * workspaces, and sweeping then would wipe pointers `restoreTabSelection` had
 * just restored from sessionStorage and cannot restore twice. `retainOnly`'s
 * own doc states the same precondition; this hook is where it is enforced.
 *
 * Must be called inside a SolidJS reactive root.
 */
export function useSelectionSweep(
  projection: () => Projection | null,
  selection: TabSelectionStore,
  tileOwners: {
    /** Main-tree tiles of `workspaceId`, memoized by `layout.store`. */
    tileOrderFor: (workspaceId: string) => readonly string[]
    /** Floating tiles of `workspaceId`, memoized by `floatingWindow.store`. */
    getAllTileIdsFor: (workspaceId: string) => string[]
  },
): void {
  const live = createMemo<{ workspaces: Set<string>, tiles: Set<string> } | null>(() => {
    const proj = projection()
    if (!proj)
      return null
    const workspaces = new Set<string>()
    const tiles = new Set<string>()
    // Composed from the two stores that already answer this, rather than a
    // third walk of the same trees. Both memoize per tick; re-deriving here
    // meant converting every workspace's main tree and every floating window's
    // inner tree a second time on every CRDT op -- ~60/s during a drag -- and
    // left "which tiles does this workspace own" answered two different ways in
    // two files. `useTabPersistence` already composes exactly this pair.
    //
    // Floating tiles are included for the same reason they always were: they
    // are real `activeByTile` keys, and they belong to the workspace that owns
    // the window whether or not it is on screen.
    for (const wsId of proj.workspaces.keys()) {
      workspaces.add(wsId)
      for (const id of tileOwners.tileOrderFor(wsId))
        tiles.add(id)
      for (const id of tileOwners.getAllTileIdsFor(wsId))
        tiles.add(id)
    }
    return { workspaces, tiles }
  }, null, {
    // Membership only: a ratio drag rewrites the trees every frame without
    // changing which workspaces or tiles exist.
    equals: (a, b) => a === b
      || (!!a && !!b && sameKeys(a.workspaces, b.workspaces) && sameKeys(a.tiles, b.tiles)),
  })

  createEffect(() => {
    const sets = live()
    if (sets)
      selection.retainOnly(sets.workspaces, sets.tiles)
  })
}
