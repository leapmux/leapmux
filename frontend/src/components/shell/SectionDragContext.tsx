import type { JSX } from 'solid-js'
import type { Section } from '~/generated/leapmux/v1/section_pb'
import { closestCenter, DragDropProvider, DragDropSensors, DragOverlay } from '@thisbeyond/solid-dnd'
import { createContext, createSignal, useContext } from 'solid-js'
import { Sidebar } from '~/generated/leapmux/v1/section_pb'
import { mid } from '~/lib/lexorank'

/** Prefix for section draggable IDs. */
export const SECTION_DRAG_PREFIX = 'sidebar-section:'

/** Prefix for sidebar drop-zone droppable IDs. */
export const SIDEBAR_ZONE_PREFIX = 'sidebar-zone:'

type ExternalDragHandler = (event: { draggable: any, droppable: any }) => void
type ExternalOverlayRenderer = (draggable: any) => JSX.Element

interface SectionDragState {
  draggedSectionId: () => string | null
  /** Register an external drag end handler (e.g., workspace DnD). */
  setExternalDragHandler: (handler: ExternalDragHandler | null) => void
  /** Register an external drag overlay renderer. */
  setExternalOverlayRenderer: (renderer: ExternalOverlayRenderer | null) => void
}

const SectionDragCtx = createContext<SectionDragState>()

export function useSectionDrag(): SectionDragState {
  const ctx = useContext(SectionDragCtx)
  if (!ctx)
    throw new Error('useSectionDrag must be used within SectionDragProvider')
  return ctx
}

interface SectionDragProviderProps {
  /** All sections (both sidebars), sorted by position within each sidebar. */
  sections: () => Section[]
  /** Optimistic UI update for moving a section. */
  onMoveSection: (sectionId: string, sidebar: Sidebar, position: string) => void
  /** Persist the move to the server. */
  onMoveSectionServer: (sectionId: string, sidebar: Sidebar, position: string) => void
  children: JSX.Element
}

/**
 * Custom collision detector that filters droppables based on drag type.
 * Section drags only interact with section/sidebar-zone droppables.
 * Workspace drags only interact with workspace/section droppables.
 * This prevents cross-type collisions when both section and workspace DnD
 * share the same DragDropProvider.
 */
function filteredCollisionDetector(draggable: any, droppables: any[], context: any) {
  const dragId = String(draggable?.id ?? '')

  if (dragId.startsWith(SECTION_DRAG_PREFIX)) {
    // Section drag — prioritize section droppables over sidebar zones.
    // Try section droppables first; only fall back to zones when no
    // section droppable is close enough (e.g. dragging into an empty sidebar).
    const sectionDroppables = droppables.filter((d: any) =>
      String(d.id).startsWith(SECTION_DRAG_PREFIX),
    )
    const sectionResult = closestCenter(draggable, sectionDroppables, context)
    if (sectionResult)
      return sectionResult

    const zoneDroppables = droppables.filter((d: any) =>
      String(d.id).startsWith(SIDEBAR_ZONE_PREFIX),
    )
    return closestCenter(draggable, zoneDroppables, context)
  }

  if (dragId.startsWith('ws-')) {
    // Workspace drag — only consider workspace and section droppables
    const filtered = droppables.filter((d: any) => {
      const id = String(d.id)
      return id.startsWith('ws-') || id.startsWith('section-')
    })
    return closestCenter(draggable, filtered, context)
  }

  return closestCenter(draggable, droppables, context)
}

export function SectionDragProvider(props: SectionDragProviderProps) {
  const [draggedSectionId, setDraggedSectionId] = createSignal<string | null>(null)
  const [externalHandler, setExternalHandler] = createSignal<ExternalDragHandler | null>(null)
  const [externalRenderer, setExternalRenderer] = createSignal<ExternalOverlayRenderer | null>(null)

  const handleDragStart = ({ draggable }: any) => {
    if (!draggable)
      return
    const id = String(draggable.id)
    if (id.startsWith(SECTION_DRAG_PREFIX)) {
      setDraggedSectionId(id.slice(SECTION_DRAG_PREFIX.length))
    }
  }

  const handleDragEnd = ({ draggable, droppable }: any) => {
    const dragId = String(draggable?.id ?? '')

    if (dragId.startsWith(SECTION_DRAG_PREFIX)) {
      // Section drag handling
      const sectionId = draggedSectionId()
      setDraggedSectionId(null)

      if (!draggable || !droppable || !sectionId)
        return

      const dropId = String(droppable.id)

      const sections = props.sections()
      const draggedSection = sections.find(s => s.id === sectionId)
      if (!draggedSection)
        return

      let targetSidebar: Sidebar
      let position: string

      if (dropId.startsWith(SECTION_DRAG_PREFIX)) {
        // Dropped on another section header
        const targetSectionId = dropId.slice(SECTION_DRAG_PREFIX.length)
        if (targetSectionId === sectionId)
          return

        const targetSection = sections.find(s => s.id === targetSectionId)
        if (!targetSection)
          return

        targetSidebar = targetSection.sidebar

        // Compute position: insert before the target section
        const sidebarSections = sections
          .filter(s => s.sidebar === targetSidebar && s.id !== sectionId)
          .sort((a, b) => a.position.localeCompare(b.position))

        const targetIdx = sidebarSections.findIndex(s => s.id === targetSectionId)
        if (targetIdx < 0)
          return

        // If dragging within the same sidebar, check direction
        if (draggedSection.sidebar === targetSidebar) {
          const dragIdx = sections
            .filter(s => s.sidebar === targetSidebar)
            .sort((a, b) => a.position.localeCompare(b.position))
            .findIndex(s => s.id === sectionId)
          const dropIdx = sections
            .filter(s => s.sidebar === targetSidebar)
            .sort((a, b) => a.position.localeCompare(b.position))
            .findIndex(s => s.id === targetSectionId)

          if (dragIdx < dropIdx) {
            // Dragging down: insert after target
            const nextPos = targetIdx + 1 < sidebarSections.length
              ? sidebarSections[targetIdx + 1].position
              : ''
            position = mid(sidebarSections[targetIdx].position, nextPos)
          }
          else {
            // Dragging up: insert before target
            const prevPos = targetIdx > 0 ? sidebarSections[targetIdx - 1].position : ''
            position = mid(prevPos, sidebarSections[targetIdx].position)
          }
        }
        else {
          // Cross-sidebar: insert before target
          const prevPos = targetIdx > 0 ? sidebarSections[targetIdx - 1].position : ''
          position = mid(prevPos, sidebarSections[targetIdx].position)
        }
      }
      else if (dropId.startsWith(SIDEBAR_ZONE_PREFIX)) {
        // Dropped on a sidebar zone — append at end
        const sideStr = dropId.slice(SIDEBAR_ZONE_PREFIX.length)
        targetSidebar = sideStr === 'left' ? Sidebar.LEFT : Sidebar.RIGHT

        if (draggedSection.sidebar === targetSidebar)
          return // Same sidebar zone without a target — no-op

        const sidebarSections = sections
          .filter(s => s.sidebar === targetSidebar && s.id !== sectionId)
          .sort((a, b) => a.position.localeCompare(b.position))

        const lastSection = sidebarSections[sidebarSections.length - 1]
        position = lastSection ? mid(lastSection.position, '') : mid('', '')
      }
      else {
        return
      }

      // Optimistic update
      props.onMoveSection(sectionId, targetSidebar, position)
      // Persist to server
      props.onMoveSectionServer(sectionId, targetSidebar, position)
    }
    else {
      // Non-section drag — delegate to external handler (e.g., workspace DnD)
      setDraggedSectionId(null)
      const handler = externalHandler()
      if (handler && draggable && droppable) {
        handler({ draggable, droppable })
      }
    }
  }

  const ctxValue: SectionDragState = {
    draggedSectionId,
    setExternalDragHandler: h => setExternalHandler(() => h),
    setExternalOverlayRenderer: r => setExternalRenderer(() => r),
  }

  return (
    <SectionDragCtx.Provider value={ctxValue}>
      <DragDropProvider
        onDragStart={handleDragStart}
        onDragEnd={handleDragEnd}
        collisionDetector={filteredCollisionDetector}
      >
        <DragDropSensors />
        {props.children}
        <DragOverlay>
          {(draggable: any) => {
            if (!draggable)
              return <></>
            const id = String(draggable.id)
            if (id.startsWith(SECTION_DRAG_PREFIX)) {
              // Section drag overlay
              const sectionId = id.slice(SECTION_DRAG_PREFIX.length)
              const section = props.sections().find(s => s.id === sectionId)
              if (!section)
                return <></>
              return (
                <div style={{
                  'padding': '4px 12px',
                  'background': 'var(--card)',
                  'border': '1px solid var(--border)',
                  'border-radius': '4px',
                  'font-size': 'var(--text-7)',
                  'font-weight': '600',
                  'color': 'var(--muted-foreground)',
                  'text-transform': 'uppercase',
                  'letter-spacing': '0.5px',
                  'opacity': '0.9',
                }}
                >
                  {section.name}
                </div>
              )
            }
            else {
              // External drag overlay (e.g., workspace DnD)
              const renderer = externalRenderer()
              return renderer ? renderer(draggable) : <></>
            }
          }}
        </DragOverlay>
      </DragDropProvider>
    </SectionDragCtx.Provider>
  )
}
