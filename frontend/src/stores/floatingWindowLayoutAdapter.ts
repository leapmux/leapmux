import type { FloatingWindowStoreType } from './floatingWindow.store'
import type { LayoutNodeLocal } from './layout.store'
import type { LayoutNode } from '~/generated/leapmux/v1/workspace_pb'
import { getAllTileIds, toProto } from './layout.store'

/**
 * Creates a thin adapter that presents a floating window's layout subtree
 * as a layout store, matching the interface expected by TileRenderer and
 * TilingLayout.
 */
export function createFloatingWindowLayoutAdapter(
  floatingWindowStore: FloatingWindowStoreType,
  windowId: string,
) {
  const getWindow = () => floatingWindowStore.getWindow(windowId)

  return {
    get state() {
      const win = getWindow()
      return {
        root: win?.layoutRoot ?? { type: 'leaf' as const, id: '' },
        focusedTileId: win?.focusedTileId ?? null,
      }
    },

    setFocusedTile(tileId: string) {
      floatingWindowStore.setFocusedTile(windowId, tileId)
    },

    focusedTileId(): string {
      const win = getWindow()
      if (!win)
        return ''
      return win.focusedTileId ?? getAllTileIds(win.layoutRoot)[0] ?? ''
    },

    splitTileHorizontal(tileId: string): string {
      return floatingWindowStore.splitTile(windowId, tileId, 'horizontal') ?? ''
    },

    splitTileVertical(tileId: string): string {
      return floatingWindowStore.splitTile(windowId, tileId, 'vertical') ?? ''
    },

    closeTile(tileId: string) {
      floatingWindowStore.closeTile(windowId, tileId)
    },

    updateRatios(splitId: string, ratios: number[]) {
      floatingWindowStore.updateRatios(windowId, splitId, ratios)
    },

    canSplitTile(_tileId: string): boolean {
      return true
    },

    getAllTileIds(): string[] {
      const win = getWindow()
      return win ? getAllTileIds(win.layoutRoot) : []
    },

    toProto(): LayoutNode {
      const win = getWindow()
      if (!win)
        return toProto({ type: 'leaf', id: '' })
      return toProto(win.layoutRoot)
    },

    setLayout(node: LayoutNodeLocal) {
      void node
    },

    initSingleTile(): string {
      return ''
    },

    fromProto() {},

    snapshot() {
      const win = getWindow()
      return {
        root: win?.layoutRoot ?? { type: 'leaf' as const, id: '' },
        focusedTileId: win?.focusedTileId ?? null,
      }
    },

    restore() {},
  }
}

export type FloatingWindowLayoutAdapterType = ReturnType<typeof createFloatingWindowLayoutAdapter>
