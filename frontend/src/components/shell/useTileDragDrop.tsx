import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { positionAtInsertIdx } from '~/lib/lexorank'
import { tabDisplayLabel, tabKey } from '~/stores/tab.helpers'
import { emitReorderTabs, emitSetTabPosition } from '~/stores/tabOps'
import { clippedText } from '~/styles/shared.css'
import * as styles from './AppShell.css'
import { useTileMove } from './useTileMove'

interface UseTileDragDropOpts {
  view: TabView
  selection: TabSelectionStore
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore: FloatingWindowStoreType
}

export function useTileDragDrop(opts: UseTileDragDropOpts) {
  const { view, selection, layoutStore, floatingWindowStore } = opts
  const tileMove = useTileMove({ view, selection, layoutStore, floatingWindowStore })

  const handleIntraTileReorder = (_tileId: string, fromKey: string, toKey: string) => {
    emitReorderTabs(view.forTile(view.get(fromKey)?.tileId ?? ''), fromKey, toKey)
  }

  const handleCrossTileMove = (fromTileId: string, toTileId: string, draggedTabKey: string, nearTabKey: string | null) => {
    const draggedTab = view.get(draggedTabKey)
    if (!draggedTab)
      return
    // Capture BEFORE the move: was this the active tab on the source?
    // If yes, the user is "carrying" what they were working on across
    // panes and focus should follow — keeping focus on the source
    // tile after the move would leave the user clicking back to where
    // their tab no longer is. If the dragged tab was inactive in its
    // source tile bar (user dragging tab Y while reading tab X), the
    // user's attention is still on X — leave focus alone.
    const wasActiveOnSource = selection.activeKeyForTile(fromTileId) === draggedTabKey

    // Capture the destination's occupants BEFORE the move. `moveTabToTile`
    // applies its op to `speculativeState` synchronously, so reading afterwards
    // would return a list that already contains the dragged tab -- at its stale
    // SOURCE rank, since only `tile_id` changed. `positionAtInsertIdx` would
    // then pick the tab's own rank as one of the two boundary neighbours and
    // mint a position against itself: dropping the first tab of one tile onto
    // the first tab of another gives `mid('n','n')` -> `'nn'`, landing it after
    // the target and tied with the target's neighbour. `emitReorderTabs`
    // splices the moved tab out for exactly this reason, and the
    // `emitMergeTabsIntoTile` callers read their lists before emitting.
    const targetTabs = view.forTile(toTileId)

    tileMove.moveTabToTile(draggedTab, toTileId, { takeFocus: wasActiveOnSource, cleanupSource: true })

    // Resolve insertion index against the pre-move tab list: when a
    // near-tab is named (drop landed on a specific tab), the dragged
    // tab takes that slot, displacing the target right; otherwise
    // append. `positionAtInsertIdx` handles all four edge cases
    // (head, tail, between, empty list) via `mid`'s documented
    // empty-string semantics.
    const nearIdx = nearTabKey
      ? targetTabs.findIndex(t => tabKey(t) === nearTabKey)
      : -1
    const insertIdx = nearIdx >= 0 ? nearIdx : targetTabs.length
    // `draggedTab`, not `draggedTabKey`: the tab is already resolved above
    // (with an early return), so re-parsing the key here only re-derived the
    // same `type`/`id` behind an `if (parsed)` guard that could never be false.
    emitSetTabPosition(draggedTab.type, draggedTab.id, positionAtInsertIdx(targetTabs, insertIdx))
  }

  const lookupTileIdForTab = (key: string): string | undefined => {
    return view.get(key)?.tileId
  }

  const renderDragOverlay = (key: string) => {
    const tab = view.get(key)
    if (!tab)
      return <></>
    // The raw style, not `ClippedText`: this is the drag image, which follows
    // the pointer and is destroyed when the drag ends. A tooltip needs 700ms of
    // still hover to appear, so one here could never show.
    return (
      <div class={styles.dragPreviewTooltip}>
        <span class={clippedText}>{tabDisplayLabel(tab)}</span>
      </div>
    )
  }

  return {
    handleIntraTileReorder,
    handleCrossTileMove,
    lookupTileIdForTab,
    renderDragOverlay,
  }
}
