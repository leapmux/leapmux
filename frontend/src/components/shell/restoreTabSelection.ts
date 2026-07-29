import type { createLayoutStore } from '~/stores/layout.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { sessionStorageGet } from '~/lib/browserStorage'
import { activeTabKey, focusedTileKey, tileActiveTabsKey } from './tabPersistenceKeys'

/**
 * Read back what {@link useTabPersistence} wrote.
 *
 * `tabSelection` and `layoutStore.focusedTileId` are per-client and never enter
 * the CRDT — two devices viewing one workspace must not fight over which tab is
 * in front. Within a session they live in memory and survive a workspace switch
 * on their own. A page RELOAD wipes memory, and sessionStorage is the only
 * carrier, so this is the other half of that pair: without it the writer is
 * writing keys nothing ever reads, and every refresh lands on whichever tab the
 * MRU fallback happens to pick.
 *
 * Applies the stored snapshot at most once per workspace, and never over a live
 * choice: if the workspace already has an in-memory pointer, the user (or a
 * sidebar click that selected the tab before switching to it) has already
 * decided, and the stored value is a stale snapshot of the previous session.
 * Without both guards, clicking a specific tab in another workspace's sidebar
 * tree would switch there and then land on whatever tab was active last time.
 *
 * ONE piece is retried past that: pointing the restored tab's own tile at it,
 * which needs the tab to have been projected. See `pendingTileBackfill`.
 */
export interface RestoreTabSelectionOpts {
  selection: TabSelectionStore
  layoutStore: ReturnType<typeof createLayoutStore>
  view: TabView
  /**
   * Whether the CRDT bootstrap has delivered this workspace.
   *
   * This is the fact the restore actually waits on, so it is asked directly
   * rather than inferred from "does the workspace have any tabs yet". Reactive:
   * the caller's effect re-runs as the projection fills.
   */
  hasWorkspace: (workspaceId: string) => boolean
}

export function createTabSelectionRestorer(opts: RestoreTabSelectionOpts) {
  const restored = new Set<string>()

  /**
   * Workspaces whose stored active-tab key named a tab the projection had not
   * delivered yet.
   *
   * The workspace pointer is fine on its own — `resolve` heals it on read — but
   * "point that tab's OWN TILE at it" needs the tab object, and the gate below
   * is satisfied by the FIRST tab that lands, not necessarily the stored one. So
   * the bulk restore consumes its attempt while the tile back-fill silently
   * no-ops, and nothing ever runs it again: the `restored.has` early return
   * exits before the reactive `forWorkspace` read, so the effect loses even that
   * dependency.
   *
   * Deliberately NOT a re-run of the whole restore. `tileActives` and the
   * focused tile are a snapshot of the previous session, and the caller's effect
   * re-runs on every CRDT tick — re-applying them would overwrite a tile the
   * user clicked while waiting. The stop condition is the one the one-shot
   * already uses: the moment the workspace pointer holds anything other than the
   * key we wrote, someone has chosen and the snapshot has lost.
   */
  const pendingTileBackfill = new Map<string, string>()

  function backfillOwnTile(workspaceId: string): void {
    const activeKey = pendingTileBackfill.get(workspaceId)
    if (!activeKey)
      return
    if (opts.selection.state.activeByWorkspace[workspaceId] !== activeKey) {
      pendingTileBackfill.delete(workspaceId)
      return
    }
    // The workspace itself is gone (deleted here or on another device). The tab
    // is never arriving, and without this the entry — plus a `view.get` on every
    // tick this workspace is active — outlives the thing it was waiting for.
    // The other two exits below cover "the tab arrived" and "someone chose"; a
    // workspace that disappears matched neither.
    if (!opts.hasWorkspace(workspaceId)) {
      pendingTileBackfill.delete(workspaceId)
      return
    }
    // Reactive read: the caller's effect re-runs as the projection fills.
    const tab = opts.view.get(activeKey)
    if (!tab)
      return
    pendingTileBackfill.delete(workspaceId)
    if (tab.workspaceId === workspaceId)
      opts.selection.restore(workspaceId, activeKey, {})
  }

  return function restoreTabSelection(workspaceId: string): void {
    if (!workspaceId)
      return
    if (restored.has(workspaceId)) {
      backfillOwnTile(workspaceId)
      return
    }
    // A pointer already in memory beats anything on disk.
    if (opts.selection.state.activeByWorkspace[workspaceId]) {
      restored.add(workspaceId)
      return
    }

    const activeKey = sessionStorageGet<string>(activeTabKey(workspaceId)) ?? null

    // Stored as JSON rather than a wrapped object so the payload stays one
    // key per workspace. A malformed value means a hand-edited or
    // half-written entry; drop it rather than throwing during startup.
    let tileActives: Record<string, string | null> = {}
    const rawTiles = sessionStorageGet<string>(tileActiveTabsKey(workspaceId))
    if (rawTiles) {
      try {
        const parsed: unknown = JSON.parse(rawTiles)
        if (parsed && typeof parsed === 'object' && !Array.isArray(parsed))
          tileActives = parsed as Record<string, string | null>
      }
      catch {
        tileActives = {}
      }
    }

    const focused = sessionStorageGet<string>(focusedTileKey(workspaceId))
    const hasStoredState = Boolean(activeKey) || Object.keys(tileActives).length > 0 || Boolean(focused)

    // Wait for the projection before consuming the one restore attempt.
    //
    // This runs off `activeWorkspaceId`, which is set from the `listWorkspaces`
    // HTTP response; the CRDT bootstrap arrives independently over the
    // userevents socket (connect + Noise handshake) and normally lands LATER.
    // Restoring against an empty projection writes pointers to tabs that do not
    // exist yet and a focused tile absent from the (still placeholder) tree,
    // which `useFocusInvariant` immediately replaces with the first leaf --
    // and marking the workspace restored would burn the only attempt.
    //
    // `hasWorkspace` is a reactive read, so the caller's effect re-runs as the
    // projection fills and this succeeds on a later pass.
    //
    // Gate on the workspace's PRESENCE, not on its tab count. "Has at least one
    // tab" was a proxy for "the bootstrap has landed", and it is wrong in both
    // directions: it passes on the FIRST tab to arrive rather than the stored
    // one (which is why the tile back-fill below needs its own retry), and for
    // a workspace whose tabs were all closed last session it is permanently
    // false — so the early return fired on every tick forever, the one-shot
    // never ran, and the focused tile the user deliberately picked was never
    // restored.
    if (hasStoredState && !opts.hasWorkspace(workspaceId))
      return

    restored.add(workspaceId)

    if (activeKey || Object.keys(tileActives).length > 0)
      opts.selection.restore(workspaceId, activeKey, tileActives)

    // The stored tab can land in a LATER frame than the rest of its workspace —
    // and, without any race at all, whenever it MOVED tile since it was last
    // activated (nothing rewrites `activeByTile` on a move, and `tileActivesFor`
    // narrows to current tile ids, so the workspace key names a tab whose tile
    // has no stored entry). `selection.restore` filed the workspace pointer
    // regardless; its tile back-fill is what needs the retry.
    if (activeKey && !opts.view.get(activeKey))
      pendingTileBackfill.set(workspaceId, activeKey)

    // Focus is only meaningful for the workspace on screen. `setFocusedTile`
    // does NOT validate the id -- `useFocusInvariant` is what snaps focus to
    // the first main leaf when the tile is in neither the main tree nor any
    // floating window, e.g. a tile closed on another device while this client
    // was away.
    // Pass `workspaceId` explicitly. `setFocusedTile` otherwise files the write
    // under whatever `getWorkspaceId()` returns at that instant, which makes
    // this function's per-workspace contract accidental rather than total —
    // the same defect the destination-tile write in `useCrossWorkspaceMove`
    // already had once.
    if (focused)
      opts.layoutStore.setFocusedTile(focused, workspaceId)
  }
}
