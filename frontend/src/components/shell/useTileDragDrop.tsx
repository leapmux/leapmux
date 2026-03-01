import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import { createMemo } from 'solid-js'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { after, mid } from '~/lib/lexorank'
import { tabKey } from '~/stores/tab.store'
import * as styles from './AppShell.css'

interface UseTileDragDropOpts {
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  persistLayout: () => void
}

export function useTileDragDrop(opts: UseTileDragDropOpts) {
  const { tabStore, layoutStore, persistLayout } = opts

  const hasMultipleTiles = createMemo(() => layoutStore.getAllTileIds().length > 1)

  const handleIntraTileReorder = (_tileId: string, fromKey: string, toKey: string) => {
    tabStore.reorderTabs(fromKey, toKey)
    persistLayout()
  }

  const handleCrossTileMove = (fromTileId: string, toTileId: string, draggedTabKey: string, nearTabKey: string | null) => {
    tabStore.moveTabToTile(draggedTabKey, toTileId)

    const targetTabs = tabStore.getTabsForTile(toTileId)
    let newPosition: string
    if (nearTabKey) {
      const nearIdx = targetTabs.findIndex(t => tabKey(t) === nearTabKey)
      if (nearIdx >= 0) {
        const prevPos = nearIdx > 0 ? targetTabs[nearIdx - 1]?.position ?? '' : ''
        const nextPos = targetTabs[nearIdx]?.position ?? ''
        newPosition = mid(prevPos, nextPos)
      }
      else {
        const lastTab = targetTabs[targetTabs.length - 1]
        newPosition = lastTab?.position ? after(lastTab.position) : 'a'
      }
    }
    else {
      const lastTab = targetTabs[targetTabs.length - 1]
      newPosition = lastTab?.position ? after(lastTab.position) : 'a'
    }
    tabStore.setTabPosition(draggedTabKey, newPosition)

    const parts = draggedTabKey.split(':')
    if (parts.length === 2) {
      tabStore.setActiveTabForTile(toTileId, Number(parts[0]) as TabType, parts[1])
    }

    persistLayout()
  }

  const lookupTileIdForTab = (key: string): string | undefined => {
    const tab = tabStore.state.tabs.find(t => tabKey(t) === key)
    return tab?.tileId
  }

  const renderDragOverlay = (key: string) => {
    const tab = tabStore.state.tabs.find(t => tabKey(t) === key)
    if (!tab)
      return <></>
    const label = tab.title || (tab.type === TabType.AGENT ? 'Agent' : tab.type === TabType.FILE ? (tab.filePath?.split('/').pop() ?? 'File') : 'Terminal')
    return (
      <div class={styles.dragPreviewTooltip}>
        <span>{label}</span>
      </div>
    )
  }

  return {
    hasMultipleTiles,
    handleIntraTileReorder,
    handleCrossTileMove,
    lookupTileIdForTab,
    renderDragOverlay,
  }
}
