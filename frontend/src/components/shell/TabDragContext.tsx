import type { JSX } from 'solid-js'
import { createContext, createSignal, onCleanup, useContext } from 'solid-js'
import { useOptionalSectionDrag } from './SectionDragContext'

/** Prefix used for tab-bar zone droppable IDs. */
export const TABBAR_ZONE_PREFIX = 'tabbar-zone:'

/** Prefix used for workspace drop target IDs in sidebar. */
export const WORKSPACE_DROP_PREFIX = 'workspace-drop:'

/** Prefix used for draggable sidebar tab tree leaves. Format: `sidebar-tab:{workspaceId}:{tabType}:{tabId}` */
export const SIDEBAR_TAB_PREFIX = 'sidebar-tab:'

interface TabDragState {
  /** Tile ID where the drag started. */
  dragSourceTileId: () => string | null
  /** Tile ID currently being hovered over. */
  dragOverTileId: () => string | null
  /** Key of the tab being dragged. */
  draggedTabKey: () => string | null
}

const TabDragContext = createContext<TabDragState>()

export function useTabDrag(): TabDragState {
  const ctx = useContext(TabDragContext)
  if (!ctx)
    throw new Error('useTabDrag must be used within TabDragProvider')
  return ctx
}

interface TabDragProviderProps {
  onIntraTileReorder: (tileId: string, fromKey: string, toKey: string) => void
  onCrossTileMove: (fromTileId: string, toTileId: string, tabKey: string, nearTabKey: string | null) => void
  onCrossWorkspaceMove?: (targetWorkspaceId: string, tabKey: string, sourceWorkspaceId?: string, targetTileId?: string) => void
  lookupTileIdForTab: (tabKey: string) => string | undefined
  renderDragOverlay: (tabKey: string) => JSX.Element
  children: JSX.Element
}

/**
 * Tab drag-and-drop context that delegates to the unified SectionDragProvider.
 *
 * Instead of creating its own DragDropProvider (which would shadow the outer
 * SectionDragProvider and prevent cross-scope interactions), this component
 * registers its drag handlers as external handlers on SectionDragProvider.
 * Tab draggables and droppables register with the single shared provider,
 * enabling tabs to be dragged between the main area, floating windows, and
 * workspace items in the sidebar.
 */
export function TabDragProvider(props: TabDragProviderProps) {
  const sectionDrag = useOptionalSectionDrag()
  if (!sectionDrag)
    throw new Error('TabDragProvider must be used within SectionDragProvider')
  return <DelegatingTabDragProvider sectionDrag={sectionDrag} {...props} />
}

/**
 * Registers tab drag handlers on the parent SectionDragProvider.
 * Used in the main layout where a SectionDragProvider exists.
 */
function DelegatingTabDragProvider(props: TabDragProviderProps & { sectionDrag: NonNullable<ReturnType<typeof useOptionalSectionDrag>> }) {
  const [dragSourceTileId, setDragSourceTileId] = createSignal<string | null>(null)
  const [dragOverTileId, setDragOverTileId] = createSignal<string | null>(null)
  const [draggedTabKey, setDraggedTabKey] = createSignal<string | null>(null)
  /** Source workspace ID when dragging a sidebar tab from a non-active workspace. */
  const [dragSourceWorkspaceId, setDragSourceWorkspaceId] = createSignal<string | null>(null)

  // eslint-disable-next-line solid/reactivity -- sectionDrag is a context value passed once, never changes
  const { addExternalDragHandler, addExternalDragStartHandler, addExternalDragOverHandler, addExternalOverlayRenderer } = props.sectionDrag

  /* eslint-disable solid/reactivity -- handler callbacks are stable, invoked by SectionDragProvider events */

  // Register tab drag start handler.
  const disposeStartHandler = addExternalDragStartHandler(({ draggable }) => {
    if (!draggable)
      return
    const id = String(draggable.id)
    // Only handle tab drags (not workspace drags which start with 'ws-')
    if (id.startsWith('ws-'))
      return

    // Sidebar tab drag: sidebar-tab:{workspaceId}:{tabType}:{tabId}
    if (id.startsWith(SIDEBAR_TAB_PREFIX)) {
      const rest = id.slice(SIDEBAR_TAB_PREFIX.length)
      const colonIdx = rest.indexOf(':')
      if (colonIdx >= 0) {
        const wsId = rest.slice(0, colonIdx)
        const realTabKey = rest.slice(colonIdx + 1)
        setDraggedTabKey(realTabKey)
        setDragSourceWorkspaceId(wsId)
        setDragSourceTileId(null) // sidebar tabs have no tile
        setDragOverTileId(null)
        return
      }
    }

    const tileId = props.lookupTileIdForTab(id)
    setDraggedTabKey(id)
    setDragSourceWorkspaceId(null)
    setDragSourceTileId(tileId ?? null)
    setDragOverTileId(null)
  })

  // Register tab drag over handler.
  const disposeOverHandler = addExternalDragOverHandler(({ draggable, droppable }) => {
    const dragId = String(draggable?.id ?? '')
    if (dragId.startsWith('ws-'))
      return
    if (!droppable) {
      setDragOverTileId(null)
      return
    }
    const droppableId = String(droppable.id)
    if (droppableId.startsWith(TABBAR_ZONE_PREFIX)) {
      setDragOverTileId(droppableId.slice(TABBAR_ZONE_PREFIX.length))
    }
    else if (droppableId.startsWith(WORKSPACE_DROP_PREFIX)) {
      setDragOverTileId(null)
    }
    else {
      const tileId = props.lookupTileIdForTab(droppableId)
      setDragOverTileId(tileId ?? null)
    }
  })

  // Register tab drag end handler.
  const disposeDragHandler = addExternalDragHandler(({ draggable, droppable }) => {
    const dragId = String(draggable?.id ?? '')

    // Don't handle workspace drags — they're handled by the workspace handler.
    if (dragId.startsWith('ws-'))
      return

    const tabKeyVal = draggedTabKey()
    const sourceTileId = dragSourceTileId()
    const sourceWsId = dragSourceWorkspaceId()

    // Reset state
    setDraggedTabKey(null)
    setDragSourceTileId(null)
    setDragOverTileId(null)
    setDragSourceWorkspaceId(null)

    if (!draggable || !droppable || !tabKeyVal)
      return

    const droppableId = String(droppable.id)

    // Cross-workspace drop (sidebar workspace item).
    if (droppableId.startsWith(WORKSPACE_DROP_PREFIX)) {
      const targetWsId = droppableId.slice(WORKSPACE_DROP_PREFIX.length)
      props.onCrossWorkspaceMove?.(targetWsId, tabKeyVal, sourceWsId ?? undefined)
      return
    }

    // Sidebar tab dropped onto tabbar zone or tabbar tab → move to active workspace.
    if (sourceWsId) {
      // Determine target tile from the drop target.
      let targetTileId: string | undefined
      if (droppableId.startsWith(TABBAR_ZONE_PREFIX)) {
        targetTileId = droppableId.slice(TABBAR_ZONE_PREFIX.length)
      }
      else {
        targetTileId = props.lookupTileIdForTab(droppableId)
      }
      if (!targetTileId)
        return

      // Check if the tab already exists in the current workspace (same-workspace drag).
      const existingTileId = props.lookupTileIdForTab(tabKeyVal)
      if (existingTileId) {
        // Same workspace: use cross-tile move to relocate the tab.
        if (existingTileId !== targetTileId) {
          props.onCrossTileMove(existingTileId, targetTileId, tabKeyVal, null)
        }
      }
      else {
        // Cross-workspace: move with the specific target tile.
        props.onCrossWorkspaceMove?.('__active__', tabKeyVal, sourceWsId, targetTileId)
      }
      return
    }

    if (!sourceTileId)
      return

    if (droppableId.startsWith(TABBAR_ZONE_PREFIX)) {
      const targetTileId = droppableId.slice(TABBAR_ZONE_PREFIX.length)
      if (targetTileId === sourceTileId)
        return
      props.onCrossTileMove(sourceTileId, targetTileId, tabKeyVal, null)
    }
    else {
      const targetTileId = props.lookupTileIdForTab(droppableId)
      if (!targetTileId)
        return

      if (targetTileId === sourceTileId) {
        if (tabKeyVal !== droppableId) {
          props.onIntraTileReorder(sourceTileId, tabKeyVal, droppableId)
        }
      }
      else {
        props.onCrossTileMove(sourceTileId, targetTileId, tabKeyVal, droppableId)
      }
    }
  })

  // Register tab drag overlay renderer.
  const disposeOverlayRenderer = addExternalOverlayRenderer((draggable: any) => {
    if (!draggable)
      return null
    const id = String(draggable.id)
    // Don't render overlay for workspace drags
    if (id.startsWith('ws-'))
      return null
    // Sidebar tab drag — render overlay from draggable data since the tab
    // may not be in the active workspace's store.
    if (id.startsWith(SIDEBAR_TAB_PREFIX)) {
      const title = String(draggable.data?.title || 'Tab')
      return (
        <div style={{
          'display': 'flex',
          'align-items': 'center',
          'padding': '4px 6px',
          'font-size': '13px',
          'background': 'var(--card)',
          'border': '1px solid var(--border)',
          'border-radius': '4px',
          'box-shadow': '0 2px 8px rgba(0,0,0,0.15)',
          'white-space': 'nowrap',
          'max-width': '180px',
          'overflow': 'hidden',
          'text-overflow': 'ellipsis',
        }}
        >
          <span>{title}</span>
        </div>
      )
    }
    return props.renderDragOverlay(id)
  })
  /* eslint-enable solid/reactivity */

  onCleanup(() => {
    disposeStartHandler()
    disposeOverHandler()
    disposeDragHandler()
    disposeOverlayRenderer()
  })

  const ctxValue: TabDragState = {
    dragSourceTileId,
    dragOverTileId,
    draggedTabKey,
  }

  return (
    <TabDragContext.Provider value={ctxValue}>
      {props.children}
    </TabDragContext.Provider>
  )
}
