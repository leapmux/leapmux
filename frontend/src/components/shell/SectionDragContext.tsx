import type { JSX } from 'solid-js'
import type { Section } from '~/generated/leapmux/v1/section_pb'
import { closestCenter, DragDropProvider, DragDropSensors, DragOverlay } from '@thisbeyond/solid-dnd'
import { createContext, createSignal, useContext } from 'solid-js'
import { Sidebar } from '~/generated/leapmux/v1/section_pb'
import { mid } from '~/lib/lexorank'
import {
  computeInsertPosition,
  findClosestSectionDroppable,
  isNearIndicator as isNearIndicatorFn,
  SECTION_DRAG_PREFIX,
  SIDEBAR_ZONE_PREFIX,
} from './sectionDragUtils'

export { SECTION_DRAG_PREFIX, SIDEBAR_ZONE_PREFIX }

type ExternalDragHandler = (event: { draggable: any, droppable: any }) => void
type ExternalOverlayRenderer = (draggable: any) => JSX.Element

export interface DropIndicator {
  /** The section ID being hovered, or `__zone_left__` / `__zone_right__` for sidebar zones. */
  targetSectionId: string
  position: 'before' | 'after'
}

interface SectionDragState {
  draggedSectionId: () => string | null
  /** Current drop indicator position (null when not dragging a section). */
  dropIndicator: () => DropIndicator | null
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

/** Non-throwing version of useSectionDrag for components that may render outside the provider. */
export function useOptionalSectionDrag(): SectionDragState | undefined {
  return useContext(SectionDragCtx)
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

export function SectionDragProvider(props: SectionDragProviderProps) {
  const [draggedSectionId, setDraggedSectionId] = createSignal<string | null>(null)
  const [dropIndicator, setDropIndicator] = createSignal<DropIndicator | null>(null)
  const [externalHandler, setExternalHandler] = createSignal<ExternalDragHandler | null>(null)
  const [externalRenderer, setExternalRenderer] = createSignal<ExternalOverlayRenderer | null>(null)

  // Track the last pointer position during a drag. solid-dnd resets
  // draggable.transformed before firing onDragEnd, so we cannot rely
  // on the transform for position computation. We track the raw
  // pointer ourselves to determine before/after in handleDragEnd
  // and for proximity-based indicator visibility.
  let lastPointerX: number | null = null
  let lastPointerY: number | null = null

  // The current droppable target (set by handleDragOver).
  // Used by the pointermove handler for proximity-based indicator visibility.
  let currentDropTarget: {
    droppable: any
    targetSectionId: string
    /** Zone targets use X-bounds check only; section targets use full proximity. */
    isZone: boolean
  } | null = null

  /**
   * Custom collision detector that filters droppables based on drag type.
   * For section drags, uses the tracked pointer position (when available)
   * as the reference point instead of the element's transformed center.
   * This avoids mis-hits when the element center is offset from the cursor
   * during cross-sidebar drags.
   */
  function collisionDetector(draggable: any, droppables: any[], context: any) {
    const dragId = String(draggable?.id ?? '')

    if (dragId.startsWith(SECTION_DRAG_PREFIX)) {
      // Prefer the actual cursor position. Fall back to element top-center
      // when the pointer hasn't moved yet (drag just started).
      const ref = lastPointerX !== null && lastPointerY !== null
        ? { x: lastPointerX, y: lastPointerY }
        : {
            x: draggable.transformed.center.x,
            y: draggable.transformed.center.y - (draggable.layout.height / 2),
          }
      return findClosestSectionDroppable(dragId, droppables, ref)
    }

    if (dragId.startsWith('ws-')) {
      const filtered = droppables.filter((d: any) => {
        const id = String(d.id)
        return id.startsWith('ws-') || id.startsWith('section-')
      })
      return closestCenter(draggable, filtered, context)
    }

    return closestCenter(draggable, droppables, context)
  }

  /** Check if the cursor is within the proximity zone of the drop indicator line. */
  function isNearIndicator(droppableLayout: any, position: 'before' | 'after'): boolean {
    if (lastPointerY === null || lastPointerX === null)
      return false
    return isNearIndicatorFn(droppableLayout, position, lastPointerX, lastPointerY)
  }

  const onPointerMove = (e: PointerEvent) => {
    lastPointerX = e.clientX
    lastPointerY = e.clientY

    // Update drop indicator visibility based on proximity.
    // handleDragOver fires only on collision target changes, so this
    // handler provides continuous proximity feedback for both section
    // and zone targets.
    if (currentDropTarget) {
      const { droppable, targetSectionId, isZone } = currentDropTarget
      if (isZone) {
        const near = lastPointerX >= droppable.layout.left && lastPointerX <= droppable.layout.right
        if (near)
          setDropIndicator({ targetSectionId, position: 'after' })
        else
          setDropIndicator(null)
      }
      else {
        // Determine position by which proximity zone(s) the cursor is in,
        // rather than using a fixed threshold. This avoids showing the
        // 'after' indicator at the section bottom when the cursor is at
        // the header — for expanded sections the zones don't overlap so
        // each zone maps to exactly one position; for collapsed sections
        // the zones overlap and we pick the closer indicator line.
        const nearBefore = isNearIndicator(droppable.layout, 'before')
        const nearAfter = isNearIndicator(droppable.layout, 'after')
        if (nearBefore && nearAfter) {
          const distBefore = Math.abs(lastPointerY! - droppable.layout.top)
          const distAfter = Math.abs(lastPointerY! - droppable.layout.bottom)
          setDropIndicator({ targetSectionId, position: distBefore <= distAfter ? 'before' : 'after' })
        }
        else if (nearBefore) {
          setDropIndicator({ targetSectionId, position: 'before' })
        }
        else if (nearAfter) {
          setDropIndicator({ targetSectionId, position: 'after' })
        }
        else {
          setDropIndicator(null)
        }
      }
    }
  }

  const handleDragStart = ({ draggable }: any) => {
    if (!draggable)
      return
    const id = String(draggable.id)
    if (id.startsWith(SECTION_DRAG_PREFIX)) {
      setDraggedSectionId(id.slice(SECTION_DRAG_PREFIX.length))
    }
    setDropIndicator(null)
    lastPointerX = null
    lastPointerY = null
    currentDropTarget = null
    document.addEventListener('pointermove', onPointerMove)
  }

  const handleDragOver = ({ draggable, droppable }: any) => {
    if (!draggable || !droppable) {
      currentDropTarget = null
      setDropIndicator(null)
      return
    }

    const dragId = String(draggable.id)
    const dropId = String(droppable.id)

    if (!dragId.startsWith(SECTION_DRAG_PREFIX)) {
      currentDropTarget = null
      setDropIndicator(null)
      return
    }

    const sectionId = dragId.slice(SECTION_DRAG_PREFIX.length)

    if (dropId.startsWith(SECTION_DRAG_PREFIX)) {
      const targetSectionId = dropId.slice(SECTION_DRAG_PREFIX.length)
      if (targetSectionId === sectionId) {
        currentDropTarget = null
        setDropIndicator(null)
        return
      }

      currentDropTarget = { droppable, targetSectionId, isZone: false }
      // Don't set the indicator here — onPointerMove manages visibility
      // and computes position from cursor Y on every pointer move.
    }
    else if (dropId.startsWith(SIDEBAR_ZONE_PREFIX)) {
      const sideStr = dropId.slice(SIDEBAR_ZONE_PREFIX.length)
      // Only show the zone indicator for cross-sidebar moves.
      // Same-sidebar zone drops are no-ops, so showing an indicator is misleading.
      const targetSidebar = sideStr === 'left' ? Sidebar.LEFT : Sidebar.RIGHT
      const draggedSection = props.sections().find(s => s.id === sectionId)
      if (draggedSection && draggedSection.sidebar === targetSidebar) {
        currentDropTarget = null
        setDropIndicator(null)
      }
      else {
        currentDropTarget = { droppable, targetSectionId: `__zone_${sideStr}__`, isZone: true }
        // Don't set the indicator here — onPointerMove manages visibility
        // based on whether the cursor is within the sidebar's X bounds.
      }
    }
    else {
      currentDropTarget = null
      setDropIndicator(null)
    }
  }

  const handleDragEnd = ({ draggable, droppable }: any) => {
    const dragId = String(draggable?.id ?? '')
    const lastIndicator = dropIndicator()

    document.removeEventListener('pointermove', onPointerMove)
    currentDropTarget = null
    setDropIndicator(null)

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

        // Use the indicator position shown to the user so the drop matches
        // the visual. Falls back to cursor-based computation when the
        // indicator target doesn't match (e.g., collision changed at the
        // last moment), and to index-based comparison as a last resort.
        let insertPos: 'before' | 'after'
        if (lastIndicator && lastIndicator.targetSectionId === targetSectionId) {
          insertPos = lastIndicator.position
        }
        else if (lastPointerY !== null) {
          const distBefore = Math.abs(lastPointerY - droppable.layout.top)
          const distAfter = Math.abs(lastPointerY - droppable.layout.bottom)
          insertPos = distBefore <= distAfter ? 'before' : 'after'
        }
        else {
          insertPos = computeInsertPosition(sectionId, targetSectionId, sections)
        }

        // Reject the drop if the cursor is not near any indicator line.
        if (lastPointerY !== null && !isNearIndicator(droppable.layout, 'before') && !isNearIndicator(droppable.layout, 'after')) {
          return
        }

        const sidebarSections = sections
          .filter(s => s.sidebar === targetSidebar && s.id !== sectionId)
          .sort((a, b) => a.position.localeCompare(b.position))

        const targetIdx = sidebarSections.findIndex(s => s.id === targetSectionId)
        if (targetIdx < 0)
          return

        if (insertPos === 'after') {
          // Insert after target
          const nextPos = targetIdx + 1 < sidebarSections.length
            ? sidebarSections[targetIdx + 1].position
            : ''
          position = mid(sidebarSections[targetIdx].position, nextPos)
        }
        else {
          // Insert before target
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

        // Reject the drop if the cursor is not within the sidebar's X bounds.
        if (lastPointerX !== null && (lastPointerX < droppable.layout.left || lastPointerX > droppable.layout.right))
          return

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
    dropIndicator,
    setExternalDragHandler: h => setExternalHandler(() => h),
    setExternalOverlayRenderer: r => setExternalRenderer(() => r),
  }

  return (
    <SectionDragCtx.Provider value={ctxValue}>
      <DragDropProvider
        onDragStart={handleDragStart}
        onDragOver={handleDragOver}
        onDragEnd={handleDragEnd}
        collisionDetector={collisionDetector}
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
