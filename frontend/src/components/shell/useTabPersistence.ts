import type { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import { createEffect } from 'solid-js'
import { sessionStorageHas, sessionStorageRemove, sessionStorageSet } from '~/lib/browserStorage'
import { activeTabKey, focusedTileKey, tileActiveTabsKey } from './tabPersistenceKeys'

/**
 * Persists the per-workspace selection pointers to sessionStorage so a page
 * RELOAD lands on the tab and tile the user left. Within a session these live
 * in `tabSelection`, which survives a workspace switch on its own; only a
 * reload, which wipes memory, needs this.
 *
 * The CRDT will never carry them — focus and active-tab are per-client by
 * design — so sessionStorage is the only carrier.
 *
 * Keys:
 *   - `leapmux:activeTab:${wsId}`      → the workspace's active tab key
 *   - `leapmux:tileActiveTabs:${wsId}` → per-tile active tab keys
 *   - `leapmux:focusedTile:${wsId}`    → `layoutStore.focusedTileId()`
 *
 * WHICH workspace is active is deliberately not here: it is written by
 * `createWorkspaceSwitcher` at the moment of the switch, to localStorage and
 * keyed by user, because unlike these three it has to survive a tab close.
 *
 * `hasWorkspace` gates every write until the CRDT bootstrap has delivered THIS
 * workspace. The gate matters because before it lands the projected tree is a
 * placeholder, so there is nothing worth recording.
 *
 * It asks the projection directly rather than riding `workspaceLoading`, which
 * is the PAINT gate: a watchdog clears that one on a timer so a wedged
 * bootstrap still renders a shell instead of a blank pane. Sharing it here
 * meant the timer also authorised these writes against an empty projection.
 *
 * The gate is NOT, however, an ordering guarantee against
 * `restoreTabSelection`. Both wait on the same predicate, which once read as
 * "the writer cannot outrun it" — it can, and did: the restore carries extra
 * conditions of its own, so there are ticks where this hook writes and the
 * restore has not yet run. Every write below is therefore built to be harmless
 * in that window rather than relying on losing the race, and the one question
 * that decides it is **has anything been chosen in this session at all**. The
 * store's `chosen*` accessors answer exactly that, in three parts:
 *
 *   - `undefined` — nothing chosen. Touch nothing. This is the reload window,
 *     and writing here is what clobbered the restore: the only answer available
 *     is `activeKeyForWorkspace`'s synthesised `mruHead`, and since `mru` is
 *     never persisted every tab comes back scoring zero, so that answer is
 *     always the FIRST tab. (Persisting `mru` would fix the fallback at its
 *     root: https://github.com/leapmux/leapmux/issues/345)
 *   - `null` — chosen, but the tab is gone (closed, or moved away). HEAL it and
 *     persist that: the user is looking at the promoted survivor, and the MRU
 *     stamps behind it are live in memory. Clearing instead would drop them
 *     back to the first tab on the next load.
 *   - a key — a live choice. Persist it.
 *
 * That the heal is safe in the second case but not the first is not a
 * coincidence: `restoreTabSelection` yields to an in-memory pointer the moment
 * one exists, so "a choice exists" and "the stored key has already been
 * consumed" are the same condition.
 *
 * History: the prior `useTabPersistence` was deleted by the CRDT
 * workspace-sync refactor (commit e8870a1a). The hub-side `SaveLayout`
 * RPC it also drove is replaced by CRDT op replication; only the
 * sessionStorage mirror needs to live on for in-tab refresh
 * continuity.
 */
export interface UseTabPersistenceOpts {
  selection: TabSelectionStore
  layoutStore: ReturnType<typeof createLayoutStore>
  /**
   * Floating windows own tiles too. `layoutStore.getAllTileIds()` covers the
   * MAIN tree only, so persisting from it alone drops every floating window's
   * per-tile selection — the window reopens on its first tab after a reload.
   */
  floatingWindowStore: ReturnType<typeof createFloatingWindowStore>
  getActiveWorkspaceId: () => string | null | undefined
  /**
   * True once the CRDT bootstrap has delivered `workspaceId` to the projection.
   * Deliberately per-workspace and asked of the projection directly — see the
   * module doc for why the paint gate cannot stand in for it.
   */
  hasWorkspace: (workspaceId: string) => boolean
}

/**
 * A sessionStorage writer that skips redundant work.
 *
 * The persistence effects re-fire whenever any tracked dependency changes, but
 * the underlying string is often unchanged (`chosenTileActivesFor` re-fires on
 * any leaf write). Caching the last value per key avoids re-serialising and
 * re-storing the same JSON payload on every store mutation.
 *
 * The cache lives inside the closure rather than being threaded through every
 * call as a trailing argument, so no call site can be handed the wrong one.
 */
function createDedupedSessionWriter() {
  const lastWritten = new Map<string, string>()
  return {
    write(key: string, value: string) {
      if (lastWritten.get(key) === value)
        return
      sessionStorageSet(key, value)
      lastWritten.set(key, value)
    },
    clear(key: string) {
      if (!lastWritten.has(key) && !sessionStorageHas(key))
        return
      sessionStorageRemove(key)
      lastWritten.delete(key)
    },
  }
}

export function useTabPersistence(opts: UseTabPersistenceOpts) {
  const { selection, layoutStore, floatingWindowStore, getActiveWorkspaceId, hasWorkspace } = opts
  const session = createDedupedSessionWriter()

  /**
   * Run `write` for the active workspace, once the bootstrap has delivered it.
   *
   * All three writers share this gate and must keep sharing it -- see the module
   * doc for why it is `hasWorkspace` and not the paint gate. Naming it once is
   * what stops a fourth writer being added without it, and hands each writer a
   * non-null workspace id rather than making each re-narrow one.
   */
  function gatedEffect(write: (wsId: string) => void): void {
    createEffect(() => {
      const wsId = getActiveWorkspaceId()
      if (!wsId || !hasWorkspace(wsId))
        return
      write(wsId)
    })
  }

  gatedEffect((wsId) => {
    const chosen = selection.chosenKeyForWorkspace(wsId)
    // NOTHING has been chosen in this session. Leave the stored key completely
    // alone -- neither write nor clear. Overwhelmingly this is the reload
    // window before `restoreTabSelection` has read it back, and writing here is
    // what clobbered it: `activeKeyForWorkspace` would have answered with a
    // synthesised `mruHead`, and since `mru` is never persisted every tab comes
    // back scoring zero, so that answer is always the FIRST tab.
    //
    // Not writing is safe even if the restore never runs, because the restore
    // yields to an in-memory pointer anyway: the moment one exists it marks the
    // workspace restored and returns. So "a pointer exists" and "the stored key
    // has been consumed" are the same condition, which is what makes the heal
    // below trustworthy.
    if (chosen === undefined)
      return
    // A choice EXISTS. If its tab is gone -- closed, or moved to another
    // workspace -- the resolver's heal is what the user is now looking at, and
    // here it is a real answer rather than an invented one: the MRU stamps that
    // drive it are live in memory. Persisting it keeps the tab they were left
    // on across the next reload, where clearing would drop them back to the
    // first tab.
    const key = chosen ?? selection.activeKeyForWorkspace(wsId)
    if (key)
      session.write(activeTabKey(wsId), key)
    // The heal came back null too: the workspace has no tabs at all, so there
    // is nothing left to point at. Clear, don't just skip -- leaving the key
    // would let the next load's restore see `hasStoredState` and spend its one
    // attempt on a pointer to nothing.
    else
      session.clear(activeTabKey(wsId))
  })

  gatedEffect((wsId) => {
    // Narrow to THIS workspace's tiles: `activeByTile` is keyed by globally
    // unique tile id and therefore spans every workspace. Both owners are
    // unioned -- a floating window's tiles are this workspace's too, and
    // leaving them out silently drops their selection across a reload.
    //
    // `getAllTileIdsFor(wsId)`, not `getAllTileIds()`: the latter answers for
    // the RENDERED slice, which is the active workspace only. That happened to
    // be the same set here, but by accident of an undocumented filter inside
    // the store rather than by the narrowing this comment claims -- so the day
    // this effect ran for any other workspace it would silently persist none of
    // its floating tiles.
    const chosenByTile = selection.chosenTileActivesFor([
      ...layoutStore.tileOrderFor(wsId),
      ...floatingWindowStore.getAllTileIdsFor(wsId),
    ])
    const key = tileActiveTabsKey(wsId)
    // Exactly the workspace effect's reasoning, per tile. A tile nobody has
    // chosen on is ABSENT from the map, so "no entries at all" -- what every
    // reload looks like until the restore runs -- writes and clears nothing,
    // leaving the snapshot that keeps the tab bar on the right tab. A tile
    // whose choice is dead maps to null and is healed the same way, because the
    // same thing makes the heal trustworthy: a choice exists.
    const live: [string, string][] = []
    for (const [tileId, chosen] of Object.entries(chosenByTile)) {
      const healed = chosen ?? selection.activeKeyForTile(tileId)
      if (healed)
        live.push([tileId, healed])
    }
    if (live.length > 0)
      session.write(key, JSON.stringify(Object.fromEntries(live)))
    // Every tile that was chosen on has had its tabs taken away, so the stored
    // snapshot names nothing that exists.
    else if (Object.keys(chosenByTile).length > 0)
      session.clear(key)
  })

  gatedEffect((wsId) => {
    // `focusedTileIdFor`, NOT `focusedTileId()`. The latter falls back to
    // `firstLeafId(projectedRoot())` when the user has not focused anything,
    // and persisting a synthesised answer records a choice the user never made
    // — its own doc says "a synthesised answer would be a wrong yes". On the
    // next load that id is restored as if chosen, and if the real first leaf
    // has since changed (another client split or closed the root tile) it names
    // a tile that no longer exists.
    const focusedTileId = layoutStore.focusedTileIdFor(wsId)
    // Deliberately has no clear branch at all, unlike the two effects above.
    // All three persist a user CHOICE, but only theirs can be told apart from
    // "never chosen": the store answers null for a choice whose TAB is gone,
    // and a tab set can empty. A tile does not disappear when its tabs do, so
    // null here means only "not picked in this session" — never "the stored
    // tile is dead" — and clearing on it would destroy the key in exactly the
    // case where the restore has not run yet: a zero-tab workspace with stored
    // state early-returns in `restoreTabSelection`. `useFocusInvariant` is what
    // repairs a focused tile that really did disappear.
    if (!focusedTileId)
      return
    session.write(focusedTileKey(wsId), focusedTileId)
  })
}
