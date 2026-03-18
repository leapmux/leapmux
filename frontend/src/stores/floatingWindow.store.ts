import type { LayoutNodeLocal } from './layout.store'
import type { FloatingWindow as FloatingWindowProto } from '~/generated/leapmux/v1/workspace_pb'
import { create } from '@bufbuild/protobuf'
import { createStore, produce } from 'solid-js/store'
import { FloatingWindowSchema } from '~/generated/leapmux/v1/workspace_pb'
import { addSiblingInSameDirectionSplit, fromProto, getAllTileIds, optimize, removeNode, replaceNode, toProto } from './layout.store'

export interface FloatingWindowState {
  id: string
  x: number
  y: number
  width: number
  height: number
  opacity: number
  zIndex: number
  layoutRoot: LayoutNodeLocal
  focusedTileId: string | null
}

export interface FloatingWindowStoreState {
  windows: FloatingWindowState[]
  nextZIndex: number
}

let fwCounter = 0
function generateFwId(): string {
  fwCounter++
  return `fw-${Date.now()}-${fwCounter}`
}

let fwTileCounter = 0
function generateFwTileId(): string {
  fwTileCounter++
  return `fwtile-${Date.now()}-${fwTileCounter}`
}

export function createFloatingWindowStore() {
  const [state, setState] = createStore<FloatingWindowStoreState>({
    windows: [],
    nextZIndex: 1000,
  })

  function findWindowIndex(id: string): number {
    return state.windows.findIndex(w => w.id === id)
  }

  return {
    state,

    addWindow(opts?: { x?: number, y?: number, width?: number, height?: number }): { windowId: string, tileId: string } {
      const tileId = generateFwTileId()
      const windowId = generateFwId()
      setState(produce((s) => {
        s.windows.push({
          id: windowId,
          x: opts?.x ?? 0.2,
          y: opts?.y ?? 0.15,
          width: opts?.width ?? 0.4,
          height: opts?.height ?? 0.5,
          opacity: 1,
          zIndex: s.nextZIndex,
          layoutRoot: { type: 'leaf', id: tileId },
          focusedTileId: tileId,
        })
        s.nextZIndex++
      }))
      return { windowId, tileId }
    },

    removeWindow(id: string) {
      setState('windows', w => w.filter(win => win.id !== id))
    },

    updatePosition(id: string, x: number, y: number) {
      const idx = findWindowIndex(id)
      if (idx < 0)
        return
      setState('windows', idx, produce((w) => {
        w.x = x
        w.y = y
      }))
    },

    updateSize(id: string, width: number, height: number) {
      const idx = findWindowIndex(id)
      if (idx < 0)
        return
      setState('windows', idx, produce((w) => {
        w.width = Math.max(width, 0.05)
        w.height = Math.max(height, 0.05)
      }))
    },

    updateOpacity(id: string, opacity: number) {
      const idx = findWindowIndex(id)
      if (idx < 0)
        return
      setState('windows', idx, 'opacity', Math.max(0.2, Math.min(1, opacity)))
    },

    bringToFront(id: string) {
      const idx = findWindowIndex(id)
      if (idx < 0)
        return
      setState(produce((s) => {
        s.windows[idx].zIndex = s.nextZIndex
        s.nextZIndex++
      }))
    },

    setFocusedTile(windowId: string, tileId: string) {
      const idx = findWindowIndex(windowId)
      if (idx < 0)
        return
      setState('windows', idx, 'focusedTileId', tileId)
    },

    splitTile(windowId: string, tileId: string, direction: 'horizontal' | 'vertical'): string | null {
      const idx = findWindowIndex(windowId)
      if (idx < 0)
        return null
      const newTileId = generateFwTileId()
      setState('windows', idx, produce((w) => {
        const [newRoot, added] = addSiblingInSameDirectionSplit(w.layoutRoot, tileId, newTileId, direction)
        if (added) {
          w.layoutRoot = optimize(newRoot)
        }
        else {
          w.layoutRoot = optimize(replaceNode(w.layoutRoot, tileId, leaf => ({
            type: 'split' as const,
            id: generateFwTileId(),
            direction,
            ratios: [0.5, 0.5],
            children: [
              { type: 'leaf' as const, id: leaf.id },
              { type: 'leaf' as const, id: newTileId },
            ],
          })))
        }
      }))
      return newTileId
    },

    closeTile(windowId: string, tileId: string): boolean {
      const idx = findWindowIndex(windowId)
      if (idx < 0)
        return false
      const win = state.windows[idx]
      const result = removeNode(win.layoutRoot, tileId)
      if (!result) {
        // Last tile — remove the window
        setState('windows', w => w.filter(win => win.id !== windowId))
        return true
      }
      setState('windows', idx, produce((w) => {
        w.layoutRoot = optimize(result)
        const tileIds = getAllTileIds(w.layoutRoot)
        if (!tileIds.includes(w.focusedTileId ?? '')) {
          w.focusedTileId = tileIds[0] ?? null
        }
      }))
      return false
    },

    updateRatios(windowId: string, splitId: string, ratios: number[]) {
      const idx = findWindowIndex(windowId)
      if (idx < 0)
        return
      setState('windows', idx, produce((w) => {
        const updateInNode = (node: LayoutNodeLocal): void => {
          if (node.type === 'split') {
            if (node.id === splitId) {
              node.ratios = ratios
            }
            else {
              node.children.forEach(updateInNode)
            }
          }
        }
        updateInNode(w.layoutRoot)
      }))
    },

    getWindowForTile(tileId: string): string | null {
      for (const w of state.windows) {
        if (getAllTileIds(w.layoutRoot).includes(tileId))
          return w.id
      }
      return null
    },

    getAllTileIds(): string[] {
      return state.windows.flatMap(w => getAllTileIds(w.layoutRoot))
    },

    getWindow(id: string): FloatingWindowState | undefined {
      return state.windows.find(w => w.id === id)
    },

    getWindowTileIds(windowId: string): string[] {
      const win = state.windows.find(w => w.id === windowId)
      return win ? getAllTileIds(win.layoutRoot) : []
    },

    /** Check if a floating window has zero tabs across all its tiles */
    isWindowEmpty(windowId: string, getTabsForTile: (tileId: string) => unknown[]): boolean {
      const win = state.windows.find(w => w.id === windowId)
      if (!win)
        return true
      const tileIds = getAllTileIds(win.layoutRoot)
      return tileIds.every(id => getTabsForTile(id).length === 0)
    },

    /**
     * Remove a floating window if it's empty, and reset focus to a main layout
     * tile when the focused tile belonged to the removed window.
     * Returns true if the window was removed.
     */
    removeIfEmpty(
      windowId: string,
      getTabsForTile: (tileId: string) => unknown[],
      focusedTileId: string,
      resetFocus: (tileId: string) => void,
      mainTileIds: string[],
    ): boolean {
      const win = state.windows.find(w => w.id === windowId)
      if (!win)
        return false
      const tileIds = getAllTileIds(win.layoutRoot)
      if (!tileIds.every(id => getTabsForTile(id).length === 0))
        return false
      const windowTileIdSet = new Set(tileIds)
      setState('windows', w => w.filter(ww => ww.id !== windowId))
      if (windowTileIdSet.has(focusedTileId) && mainTileIds.length > 0) {
        resetFocus(mainTileIds[0])
      }
      return true
    },

    toProto(): FloatingWindowProto[] {
      return floatingWindowsToProto([...state.windows])
    },

    fromProto(protos: FloatingWindowProto[]) {
      const windows: FloatingWindowState[] = []
      let maxZ = 999
      for (const p of protos) {
        maxZ++
        const layoutRoot = p.layout ? fromProto(p.layout) : { type: 'leaf' as const, id: generateFwTileId() }
        windows.push({
          id: p.id || generateFwId(),
          x: p.x,
          y: p.y,
          width: p.width || 0.4,
          height: p.height || 0.5,
          opacity: p.opacity || 1,
          zIndex: maxZ,
          layoutRoot,
          focusedTileId: getAllTileIds(layoutRoot)[0] ?? null,
        })
      }
      setState('windows', windows)
      setState('nextZIndex', maxZ + 1)
    },

    snapshot(): FloatingWindowStoreState {
      return {
        windows: state.windows.map(w => ({
          ...w,
          layoutRoot: JSON.parse(JSON.stringify(w.layoutRoot)) as LayoutNodeLocal,
        })),
        nextZIndex: state.nextZIndex,
      }
    },

    restore(snap: FloatingWindowStoreState) {
      setState('windows', snap.windows.map(w => ({
        ...w,
        layoutRoot: JSON.parse(JSON.stringify(w.layoutRoot)) as LayoutNodeLocal,
      })))
      setState('nextZIndex', snap.nextZIndex)
    },
  }
}

export type FloatingWindowStoreType = ReturnType<typeof createFloatingWindowStore>

/** Pure function to convert floating window state to proto, usable outside a reactive root. */
export function floatingWindowsToProto(windows: FloatingWindowState[]): FloatingWindowProto[] {
  return windows.map(w => create(FloatingWindowSchema, {
    id: w.id,
    x: w.x,
    y: w.y,
    width: w.width,
    height: w.height,
    opacity: w.opacity,
    layout: toProto(w.layoutRoot),
  }))
}
