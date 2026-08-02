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
 * Writers and {@link restoreTabSelection} share no ordering guarantee — both
 * gate on `hasWorkspace`, but the restore carries its own extra conditions (a
 * live in-memory pointer beats disk, and it runs once per workspace), so there
 * are ticks where a writer fires before the restore has consumed its one-shot.
 * That used to require a defensive three-way distinction ("nothing chosen /
 * chosen-but-dead / chosen-and-live") so the writer would no-op in the reload
 * window: the only answer available then was `activeKeyForWorkspace`'s healed
 * `mruHead`, and because `mru` was never persisted every tab scored zero, so
 * that answer was always the FIRST tab — writing it clobbered the real stored
 * key before the restore read it back. The `mru` stamp is now persisted by
 * `tabMetadata` (seeded eagerly on construction), so the healed key the writer
 * computes in the reload window is the genuinely most-recent tab — the same key
 * `restore()` would apply. Writing it is therefore idempotent rather than
 * destructive, and the writer can simply persist the resolved answer.
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
 * the underlying string is often unchanged (`activeKeyForTile` re-fires on any
 * leaf write). Caching the last value per key avoids re-serialising and
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
    // Persist the resolved active-tab key. `activeKeyForWorkspace` heals a dead
    // pointer (closed, moved, tombstoned) to the highest-MRU survivor, and
    // because MRU is now persisted the healed answer is correct even in the
    // reload window — see the module doc. Null means the workspace has no tabs
    // at all: clear so a future restore does not spend its one attempt on a
    // pointer to nothing.
    const key = selection.activeKeyForWorkspace(wsId)
    if (key)
      session.write(activeTabKey(wsId), key)
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
    const tiles = [
      ...layoutStore.tileOrderFor(wsId),
      ...floatingWindowStore.getAllTileIdsFor(wsId),
    ]
    const key = tileActiveTabsKey(wsId)
    // Same heal-and-persist discipline as the workspace effect, per tile. A tile
    // whose resolved pointer is null (its tab is gone) is dropped from the map
    // rather than cleared, so a tile that has simply never held a tab is left
    // untouched — matching the workspace's clear-when-empty rule at the map
    // level below.
    const live: [string, string][] = []
    for (const tileId of tiles) {
      const healed = selection.activeKeyForTile(tileId)
      if (healed)
        live.push([tileId, healed])
    }
    if (live.length > 0)
      session.write(key, JSON.stringify(Object.fromEntries(live)))
    // Every tile this workspace owns has lost its tab, so the stored snapshot
    // names nothing that exists — clear rather than leave a stale map.
    else
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
