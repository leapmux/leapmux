import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { positionAtInsertIdx } from '~/lib/lexorank'
import { basename } from '~/lib/paths'
import { tabKey } from '~/stores/tab.helpers'
import * as styles from './AppShell.css'
import { useTileMove } from './useTileMove'

interface UseTileDragDropOpts {
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore: FloatingWindowStoreType
}

export function useTileDragDrop(opts: UseTileDragDropOpts) {
  const { tabStore, layoutStore, floatingWindowStore } = opts
  const tileMove = useTileMove({ tabStore, layoutStore, floatingWindowStore })

  const handleIntraTileReorder = (_tileId: string, fromKey: string, toKey: string) => {
    tabStore.reorderTabs(fromKey, toKey)
  }

  const handleCrossTileMove = (fromTileId: string, toTileId: string, draggedTabKey: string, nearTabKey: string | null) => {
    const draggedTab = tabStore.getTabByKey(draggedTabKey)
    if (!draggedTab)
      return
    // Capture BEFORE the move: was this the active tab on the source?
    // If yes, the user is "carrying" what they were working on across
    // panes and focus should follow — keeping focus on the source
    // tile after the move would leave the user clicking back to where
    // their tab no longer is. If the dragged tab was inactive in its
    // source tile bar (user dragging tab Y while reading tab X), the
    // user's attention is still on X — leave focus alone.
    const wasActiveOnSource = tabStore.getActiveTabKeyForTile(fromTileId) === draggedTabKey

    tileMove.moveTabToTile(draggedTab, toTileId, { takeFocus: wasActiveOnSource, cleanupSource: true })

    // Resolve insertion index against the post-move tab list: when a
    // near-tab is named (drop landed on a specific tab), the dragged
    // tab takes that slot, displacing the target right; otherwise
    // append. `positionAtInsertIdx` handles all four edge cases
    // (head, tail, between, empty list) via `mid`'s documented
    // empty-string semantics.
    const targetTabs = tabStore.getTabsForTile(toTileId)
    const nearIdx = nearTabKey
      ? targetTabs.findIndex(t => tabKey(t) === nearTabKey)
      : -1
    const insertIdx = nearIdx >= 0 ? nearIdx : targetTabs.length
    tabStore.setTabPosition(draggedTabKey, positionAtInsertIdx(targetTabs, insertIdx))
  }

  const lookupTileIdForTab = (key: string): string | undefined => {
    return tabStore.getTabByKey(key)?.tileId
  }

  const dragLabelFor = (tab: { title?: string, type: TabType, filePath?: string }): string => {
    if (tab.title)
      return tab.title
    if (tab.type === TabType.AGENT)
      return 'Agent'
    if (tab.type === TabType.FILE)
      return (tab.filePath && basename(tab.filePath)) || 'File'
    return 'Terminal'
  }

  const renderDragOverlay = (key: string) => {
    const tab = tabStore.getTabByKey(key)
    if (!tab)
      return <></>
    return (
      <div class={styles.dragPreviewTooltip}>
        <span>{dragLabelFor(tab)}</span>
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
