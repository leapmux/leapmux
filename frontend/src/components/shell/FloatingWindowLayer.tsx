import type { Component, JSX } from 'solid-js'
import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import type { createTabStore } from '~/stores/tab.store'
import { For } from 'solid-js'
import { tabKey } from '~/stores/tab.store'
import { CrossTileDragProvider } from './CrossTileDragContext'
import { FloatingWindowContainer } from './FloatingWindowContainer'
import * as styles from './FloatingWindowContainer.css'
import { TilingLayout } from './TilingLayout'

interface FloatingWindowLayerProps {
  floatingWindowStore: FloatingWindowStoreType
  tabStore: ReturnType<typeof createTabStore>
  renderTile: (tileId: string) => JSX.Element
  onRatioChange: (windowId: string, splitId: string, ratios: number[]) => void
  onCloseWindow: (windowId: string) => void
  onIntraTileReorder: (tileId: string, fromKey: string, toKey: string) => void
  onCrossTileMove: (fromTileId: string, toTileId: string, draggedTabKey: string, nearTabKey: string | null) => void
  lookupTileIdForTab: (key: string) => string | undefined
  renderDragOverlay: (key: string) => JSX.Element
  editorPanel?: (windowId: string) => JSX.Element | false
}

export const FloatingWindowLayer: Component<FloatingWindowLayerProps> = (props) => {
  const getWindowTitle = (windowId: string): string => {
    const win = props.floatingWindowStore.getWindow(windowId)
    if (!win)
      return 'Window'
    const focusedTileId = win.focusedTileId
    if (!focusedTileId)
      return 'Window'
    const tileActiveKey = props.tabStore.getActiveTabKeyForTile(focusedTileId)
    if (!tileActiveKey)
      return 'Window'
    const tab = props.tabStore.state.tabs.find(t => tabKey(t) === tileActiveKey)
    return tab?.title || 'Window'
  }

  return (
    <div class={styles.floatingLayer} data-testid="floating-window-layer">
      <For each={props.floatingWindowStore.state.windows}>
        {win => (
          <FloatingWindowContainer
            windowId={win.id}
            x={win.x}
            y={win.y}
            width={win.width}
            height={win.height}
            zIndex={win.zIndex}
            title={getWindowTitle(win.id)}
            floatingWindowStore={props.floatingWindowStore}
            onClose={() => props.onCloseWindow(win.id)}
          >
            <CrossTileDragProvider
              onIntraTileReorder={props.onIntraTileReorder}
              onCrossTileMove={props.onCrossTileMove}
              lookupTileIdForTab={props.lookupTileIdForTab}
              renderDragOverlay={props.renderDragOverlay}
            >
              <TilingLayout
                root={win.layoutRoot}
                renderTile={props.renderTile}
                onRatioChange={(splitId, ratios) => props.onRatioChange(win.id, splitId, ratios)}
              />
            </CrossTileDragProvider>
            {props.editorPanel?.(win.id)}
          </FloatingWindowContainer>
        )}
      </For>
    </div>
  )
}
