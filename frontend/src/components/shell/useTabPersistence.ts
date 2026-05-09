import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import { createEffect } from 'solid-js'
import { ACTIVE_WORKSPACE_KEY, activeTabKey, focusedTileKey, tileActiveTabsKey } from './tabPersistenceKeys'

/**
 * Persists the view-state slices that `useWorkspaceRestore` reads back
 * on workspace activation / page reload to sessionStorage. Without
 * these writers the restore path's reads are always empty and every
 * refresh activates the first tab and an arbitrary tile, regardless of
 * what the user had focused.
 *
 * Keys mirror the reads in `useWorkspaceRestore.ts`:
 *   - `leapmux:activeTab:${wsId}`      → `tabStore.state.activeTabKey`
 *   - `leapmux:tileActiveTabs:${wsId}` → per-tile active tab keys
 *   - `leapmux:focusedTile:${wsId}`    → `layoutStore.focusedTileId()`
 *   - `leapmux:activeWorkspace`        → the currently active workspace id
 *
 * `workspaceLoading` gates every write so the brief window during
 * `useWorkspaceRestore`'s `tabStore.clear()` doesn't blow away a
 * just-restored key.
 *
 * History: the prior `useTabPersistence` was deleted by the CRDT
 * workspace-sync refactor (commit e8870a1a). The hub-side `SaveLayout`
 * RPC it also drove is replaced by CRDT op replication; only the
 * sessionStorage mirror needs to live on for in-tab refresh
 * continuity.
 */
export interface UseTabPersistenceOpts {
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  getActiveWorkspaceId: () => string | null | undefined
  workspaceLoading: () => boolean
}

function writeIfChanged(key: string, value: string, last: Map<string, string>) {
  if (last.get(key) === value)
    return
  sessionStorage.setItem(key, value)
  last.set(key, value)
}

function clearIfPresent(key: string, last: Map<string, string>) {
  if (!last.has(key) && sessionStorage.getItem(key) == null)
    return
  sessionStorage.removeItem(key)
  last.delete(key)
}

export function useTabPersistence(opts: UseTabPersistenceOpts) {
  const { tabStore, layoutStore, getActiveWorkspaceId, workspaceLoading } = opts
  // Cache the last-written value per key. The persistence effects re-fire
  // whenever any tracked dependency changes, but the underlying string is
  // often unchanged (e.g. tileActiveTabKeys re-fires on any leaf write).
  // Skipping equal writes avoids re-serialising and re-storing the JSON
  // payload on every store mutation.
  const lastWritten = new Map<string, string>()

  createEffect(() => {
    const wsId = getActiveWorkspaceId()
    const activeKey = tabStore.state.activeTabKey
    if (!wsId || workspaceLoading() || !activeKey)
      return
    writeIfChanged(activeTabKey(wsId), activeKey, lastWritten)
  })

  createEffect(() => {
    const wsId = getActiveWorkspaceId()
    const tileActiveTabKeys = tabStore.state.tileActiveTabKeys
    if (!wsId || workspaceLoading())
      return
    const key = tileActiveTabsKey(wsId)
    const entries = Object.entries(tileActiveTabKeys).filter(([, v]) => v != null)
    if (entries.length > 0)
      writeIfChanged(key, JSON.stringify(Object.fromEntries(entries)), lastWritten)
    else
      clearIfPresent(key, lastWritten)
  })

  createEffect(() => {
    const wsId = getActiveWorkspaceId()
    const focusedTileId = layoutStore.focusedTileId()
    if (!wsId || workspaceLoading() || !focusedTileId)
      return
    writeIfChanged(focusedTileKey(wsId), focusedTileId, lastWritten)
  })

  createEffect(() => {
    const wsId = getActiveWorkspaceId()
    if (!wsId || workspaceLoading())
      return
    writeIfChanged(ACTIVE_WORKSPACE_KEY, wsId, lastWritten)
  })
}
