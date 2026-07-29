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
 * workspace. The gate matters because before it lands the selection is empty
 * and the projected tree is a placeholder — writing then deletes
 * `tileActiveTabs` (nothing to write, so it clears), destroying the state the
 * restore is about to read.
 *
 * It asks the projection directly rather than riding `workspaceLoading`, which
 * is the PAINT gate: a watchdog clears that one on a timer so a wedged
 * bootstrap still renders a shell instead of a blank pane. Sharing it here
 * meant the timer also authorised these writes, and the three effects would
 * then run against an empty projection and clear the very keys
 * `restoreTabSelection` was still waiting — on the same predicate — to read.
 * That is the destructive case above, reached on every slow load rather than
 * avoided. Same predicate as the restore, so the writer cannot outrun it.
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

function writeIfChanged(key: string, value: string, last: Map<string, string>) {
  if (last.get(key) === value)
    return
  sessionStorageSet(key, value)
  last.set(key, value)
}

function clearIfPresent(key: string, last: Map<string, string>) {
  if (!last.has(key) && !sessionStorageHas(key))
    return
  sessionStorageRemove(key)
  last.delete(key)
}

export function useTabPersistence(opts: UseTabPersistenceOpts) {
  const { selection, layoutStore, floatingWindowStore, getActiveWorkspaceId, hasWorkspace } = opts
  // Cache the last-written value per key. The persistence effects re-fire
  // whenever any tracked dependency changes, but the underlying string is
  // often unchanged (e.g. tileActiveTabKeys re-fires on any leaf write).
  // Skipping equal writes avoids re-serialising and re-storing the JSON
  // payload on every store mutation.
  const lastWritten = new Map<string, string>()

  createEffect(() => {
    const wsId = getActiveWorkspaceId()
    const activeKey = selection.activeKeyForWorkspace(wsId ?? '')
    if (!wsId || !hasWorkspace(wsId))
      return
    // Clear, don't just skip. `activeKeyForWorkspace` answers null ONLY when the
    // workspace has no tabs at all (`resolve` falls through to `mruHead` of an
    // empty list), so "nothing to write" here means "nothing left to point at":
    // returning would leave the key naming the tab the user just closed, and the
    // next load's restore would see `hasStoredState` and spend its one attempt
    // on it. Same reasoning as the per-tile effect below, which already clears.
    if (activeKey)
      writeIfChanged(activeTabKey(wsId), activeKey, lastWritten)
    else
      clearIfPresent(activeTabKey(wsId), lastWritten)
  })

  createEffect(() => {
    const wsId = getActiveWorkspaceId()
    if (!wsId || !hasWorkspace(wsId))
      return
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
    const tileActiveTabKeys = selection.tileActivesFor([
      ...layoutStore.tileOrderFor(wsId),
      ...floatingWindowStore.getAllTileIdsFor(wsId),
    ])
    const key = tileActiveTabsKey(wsId)
    const entries = Object.entries(tileActiveTabKeys).filter(([, v]) => v != null)
    if (entries.length > 0)
      writeIfChanged(key, JSON.stringify(Object.fromEntries(entries)), lastWritten)
    else
      clearIfPresent(key, lastWritten)
  })

  createEffect(() => {
    const wsId = getActiveWorkspaceId()
    if (!wsId || !hasWorkspace(wsId))
      return
    // `focusedTileIdFor`, NOT `focusedTileId()`. The latter falls back to
    // `firstLeafId(projectedRoot())` when the user has not focused anything,
    // and persisting a synthesised answer records a choice the user never made
    // — its own doc says "a synthesised answer would be a wrong yes". On the
    // next load that id is restored as if chosen, and if the real first leaf
    // has since changed (another client split or closed the root tile) it names
    // a tile that no longer exists.
    const focusedTileId = layoutStore.focusedTileIdFor(wsId)
    // Deliberately does NOT clear when null, unlike the two effects above.
    // Those persist values DERIVED from the tab set, so an empty tab set makes
    // the stored value dead. This one persists a user CHOICE that outlives the
    // tab set — tiles do not disappear when their tabs do — and null here means
    // "not picked in this session", not "unfocused". Clearing would destroy the
    // key in exactly the case where the restore has not run yet: a zero-tab
    // workspace with stored state early-returns in `restoreTabSelection`.
    if (!focusedTileId)
      return
    writeIfChanged(focusedTileKey(wsId), focusedTileId, lastWritten)
  })
}
