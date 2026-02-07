import type { JSX } from 'solid-js'
import { closestCenter, DragDropProvider, DragDropSensors, DragOverlay } from '@thisbeyond/solid-dnd'
import { createContext, createSignal, useContext } from 'solid-js'

/** Prefix used for tab-bar zone droppable IDs. */
export const TABBAR_ZONE_PREFIX = 'tabbar-zone:'

interface CrossTileDragState {
  /** Tile ID where the drag started. */
  dragSourceTileId: () => string | null
  /** Tile ID currently being hovered over. */
  dragOverTileId: () => string | null
  /** Key of the tab being dragged. */
  draggedTabKey: () => string | null
}

const CrossTileDragContext = createContext<CrossTileDragState>()

export function useCrossTileDrag(): CrossTileDragState {
  const ctx = useContext(CrossTileDragContext)
  if (!ctx)
    throw new Error('useCrossTileDrag must be used within CrossTileDragProvider')
  return ctx
}

interface CrossTileDragProviderProps {
  onIntraTileReorder: (tileId: string, fromKey: string, toKey: string) => void
  onCrossTileMove: (fromTileId: string, toTileId: string, tabKey: string, nearTabKey: string | null) => void
  lookupTileIdForTab: (tabKey: string) => string | undefined
  renderDragOverlay: (tabKey: string) => JSX.Element
  children: JSX.Element
}

export function CrossTileDragProvider(props: CrossTileDragProviderProps) {
  const [dragSourceTileId, setDragSourceTileId] = createSignal<string | null>(null)
  const [dragOverTileId, setDragOverTileId] = createSignal<string | null>(null)
  const [draggedTabKey, setDraggedTabKey] = createSignal<string | null>(null)

  const handleDragStart = ({ draggable }: any) => {
    if (!draggable)
      return
    const tabKey = String(draggable.id)
    const tileId = props.lookupTileIdForTab(tabKey)
    setDraggedTabKey(tabKey)
    setDragSourceTileId(tileId ?? null)
    setDragOverTileId(null)
  }

  const handleDragOver = ({ droppable }: any) => {
    if (!droppable) {
      setDragOverTileId(null)
      return
    }
    const droppableId = String(droppable.id)
    if (droppableId.startsWith(TABBAR_ZONE_PREFIX)) {
      setDragOverTileId(droppableId.slice(TABBAR_ZONE_PREFIX.length))
    }
    else {
      // Droppable is a tab — look up its tile
      const tileId = props.lookupTileIdForTab(droppableId)
      setDragOverTileId(tileId ?? null)
    }
  }

  const handleDragEnd = ({ draggable, droppable }: any) => {
    const tabKey = draggedTabKey()
    const sourceTileId = dragSourceTileId()

    // Reset state
    setDraggedTabKey(null)
    setDragSourceTileId(null)
    setDragOverTileId(null)

    if (!draggable || !droppable || !tabKey || !sourceTileId)
      return

    const droppableId = String(droppable.id)

    if (droppableId.startsWith(TABBAR_ZONE_PREFIX)) {
      // Dropped on a tab bar zone — cross-tile append
      const targetTileId = droppableId.slice(TABBAR_ZONE_PREFIX.length)
      if (targetTileId === sourceTileId)
        return // Same tile, no-op
      props.onCrossTileMove(sourceTileId, targetTileId, tabKey, null)
    }
    else {
      // Dropped on a tab
      const targetTileId = props.lookupTileIdForTab(droppableId)
      if (!targetTileId)
        return

      if (targetTileId === sourceTileId) {
        // Intra-tile reorder
        if (tabKey !== droppableId) {
          props.onIntraTileReorder(sourceTileId, tabKey, droppableId)
        }
      }
      else {
        // Cross-tile move near a specific tab
        props.onCrossTileMove(sourceTileId, targetTileId, tabKey, droppableId)
      }
    }
  }

  const ctxValue: CrossTileDragState = {
    dragSourceTileId,
    dragOverTileId,
    draggedTabKey,
  }

  return (
    <CrossTileDragContext.Provider value={ctxValue}>
      <DragDropProvider
        onDragStart={handleDragStart}
        onDragOver={handleDragOver}
        onDragEnd={handleDragEnd}
        collisionDetector={closestCenter}
      >
        <DragDropSensors />
        {props.children}
        <DragOverlay>
          {(draggable: any) => {
            if (!draggable)
              return <></>
            const key = String(draggable.id)
            return props.renderDragOverlay(key)
          }}
        </DragOverlay>
      </DragDropProvider>
    </CrossTileDragContext.Provider>
  )
}
