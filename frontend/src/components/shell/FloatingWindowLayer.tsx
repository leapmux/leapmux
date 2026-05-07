import type { Component, JSX } from 'solid-js'
import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import type { GridAxis } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import { For } from 'solid-js'
import { ChatDropZone } from '~/components/chat/ChatDropZone'
import { FloatingWindowContainer } from './FloatingWindowContainer'
import * as styles from './FloatingWindowContainer.css'
import { TilingLayout } from './TilingLayout'

interface FloatingWindowLayerProps {
  floatingWindowStore: FloatingWindowStoreType
  tabStore: ReturnType<typeof createTabStore>
  renderTile: (tileId: string) => JSX.Element
  onRatioChange: (windowId: string, splitId: string, ratios: number[]) => void
  onGridRatiosChange?: (windowId: string, gridId: string, axis: GridAxis, ratios: number[]) => void
  onCloseWindow: (windowId: string) => void
  onActivateWindow?: (windowId: string) => void
  onGeometryChange?: () => void
  editorPanel?: (windowId: string) => JSX.Element | false
  onFileDrop?: (dataTransfer: DataTransfer, shiftKey: boolean) => void
  fileDropDisabled?: boolean
}

/**
 * Floor for the inline `z-index` we hand each floating window. Sits well
 * below CSS `--z-dropdown` and friends so even the topmost floating window
 * stays under modals / popovers / tooltips. The store doesn't carry a
 * z-index field — we derive it from the window's position in
 * `state.windows` (last = topmost) per its `bringToFront` contract.
 */
const Z_INDEX_BASE = 1000

export const FloatingWindowLayer: Component<FloatingWindowLayerProps> = (props) => {
  const getWindowTitle = (windowId: string): string => {
    const win = props.floatingWindowStore.getWindow(windowId)
    if (!win?.focusedTileId)
      return 'Window'
    return props.tabStore.getActiveTabForTile(win.focusedTileId)?.title || 'Window'
  }

  return (
    <div class={styles.floatingLayer} data-testid="floating-window-layer">
      <For each={props.floatingWindowStore.state.windows}>
        {(win, idx) => (
          <FloatingWindowContainer
            windowId={win.id}
            x={win.x}
            y={win.y}
            width={win.width}
            height={win.height}
            opacity={win.opacity}
            zIndex={Z_INDEX_BASE + idx()}
            title={getWindowTitle(win.id)}
            floatingWindowStore={props.floatingWindowStore}
            onClose={() => props.onCloseWindow(win.id)}
            onActivate={() => props.onActivateWindow?.(win.id)}
            onGeometryChange={props.onGeometryChange}
          >
            <ChatDropZone onDrop={props.onFileDrop} disabled={props.fileDropDisabled}>
              <TilingLayout
                root={win.layoutRoot}
                renderTile={props.renderTile}
                onRatioChange={(splitId, ratios) => props.onRatioChange(win.id, splitId, ratios)}
                onGridRatiosChange={(gridId, axis, ratios) => props.onGridRatiosChange?.(win.id, gridId, axis, ratios)}
              />
              {props.editorPanel?.(win.id)}
            </ChatDropZone>
          </FloatingWindowContainer>
        )}
      </For>
    </div>
  )
}
