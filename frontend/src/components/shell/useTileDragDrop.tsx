import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { positionAtInsertIdx } from '~/lib/lexorank'
import { basename } from '~/lib/paths'
import { parseTabKey, tabKey } from '~/stores/tab.store'
import * as styles from './AppShell.css'
import { removeEmptyFloatingWindow } from './tileLifecycle'

interface UseTileDragDropOpts {
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore: FloatingWindowStoreType
  persistLayout: () => void
}

export function useTileDragDrop(opts: UseTileDragDropOpts) {
  const { tabStore, layoutStore, floatingWindowStore, persistLayout } = opts

  const handleIntraTileReorder = (_tileId: string, fromKey: string, toKey: string) => {
    tabStore.reorderTabs(fromKey, toKey)
    persistLayout()
  }

  const handleCrossTileMove = (fromTileId: string, toTileId: string, draggedTabKey: string, nearTabKey: string | null) => {
    tabStore.moveTabToTile(draggedTabKey, toTileId)

    // Resolve insertion index: when a near-tab is named (drop landed on a
    // specific tab), the dragged tab takes that slot, displacing the target
    // right; otherwise append. `positionAtInsertIdx` handles all four edge
    // cases (head, tail, between, empty list) via `mid`'s documented
    // empty-string semantics.
    const targetTabs = tabStore.getTabsForTile(toTileId)
    const nearIdx = nearTabKey
      ? targetTabs.findIndex(t => tabKey(t) === nearTabKey)
      : -1
    const insertIdx = nearIdx >= 0 ? nearIdx : targetTabs.length
    const newPosition = positionAtInsertIdx(targetTabs, insertIdx)
    tabStore.setTabPosition(draggedTabKey, newPosition)

    const parsed = parseTabKey(draggedTabKey)
    if (parsed) {
      tabStore.setActiveTabForTile(toTileId, parsed.type, parsed.id)
    }

    // Remove the source floating window if it's now empty.
    removeEmptyFloatingWindow(layoutStore, floatingWindowStore, tabStore, fromTileId)

    persistLayout()
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
