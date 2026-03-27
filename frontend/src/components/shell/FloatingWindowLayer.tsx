import type { Component, JSX } from 'solid-js'
import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import type { createTabStore } from '~/stores/tab.store'
import { For } from 'solid-js'
import { ChatDropZone } from '~/components/chat/ChatDropZone'
import { tabKey } from '~/stores/tab.store'
import { FloatingWindowContainer } from './FloatingWindowContainer'
import * as styles from './FloatingWindowContainer.css'
import { TilingLayout } from './TilingLayout'

interface FloatingWindowLayerProps {
  floatingWindowStore: FloatingWindowStoreType
  tabStore: ReturnType<typeof createTabStore>
  renderTile: (tileId: string) => JSX.Element
  onRatioChange: (windowId: string, splitId: string, ratios: number[]) => void
  onCloseWindow: (windowId: string) => void
  onGeometryChange?: () => void
  editorPanel?: (windowId: string) => JSX.Element | false
  onFileDrop?: (files: FileList, shiftKey: boolean) => void
  fileDropDisabled?: boolean
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
            opacity={win.opacity}
            zIndex={win.zIndex}
            title={getWindowTitle(win.id)}
            floatingWindowStore={props.floatingWindowStore}
            onClose={() => props.onCloseWindow(win.id)}
            onGeometryChange={props.onGeometryChange}
          >
            <ChatDropZone onDrop={props.onFileDrop} disabled={props.fileDropDisabled}>
              <TilingLayout
                root={win.layoutRoot}
                renderTile={props.renderTile}
                onRatioChange={(splitId, ratios) => props.onRatioChange(win.id, splitId, ratios)}
              />
              {props.editorPanel?.(win.id)}
            </ChatDropZone>
          </FloatingWindowContainer>
        )}
      </For>
    </div>
  )
}
