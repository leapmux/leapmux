import type { Accessor, Setter } from 'solid-js'
import { createMemo } from 'solid-js'

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

export const MIN_FRACTION = 0.15

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

export interface UseResizeHandleOptions {
  /** IDs of expandable sections (in display order). */
  expandableSectionIds: Accessor<string[]>
  /** Whether a given section is open. */
  isOpen: (id: string) => boolean
  /** Current per-section fractional sizes. */
  sectionSizes: Accessor<Record<string, number>>
  /** Signal setter for per-section fractional sizes. */
  setSectionSizes: Setter<Record<string, number>>
  /** Signal setter for which handle is currently being dragged. */
  setDraggingHandleIndex: Setter<number | null>
  /** Ref to the container element (for measuring height). */
  containerRef: () => HTMLDivElement | undefined
  /** Called after resize completes (mouseup) or after reset. */
  notifyStateChange: () => void
}

export interface UseResizeHandleReturn {
  /** Number of currently expanded sections. */
  expandedCount: Accessor<number>
  /** Normalized fractional sizes for currently expanded sections. */
  expandedSizes: Accessor<Map<string, number>>
  /** Start dragging a resize handle between two adjacent expanded sections. */
  handleResizeStart: (handleIndex: number, e: MouseEvent) => void
  /** Reset all expanded sections to equal sizes. */
  handleResetSplit: () => void
}

export function useResizeHandle(options: UseResizeHandleOptions): UseResizeHandleReturn {
  const {
    expandableSectionIds,
    isOpen,
    sectionSizes,
    setSectionSizes,
    setDraggingHandleIndex,
    containerRef,
    notifyStateChange,
  } = options

  // Count how many expandable sections are currently expanded
  const expandedCount: Accessor<number> = () => {
    let count = 0
    for (const id of expandableSectionIds()) {
      if (isOpen(id))
        count++
    }
    return count
  }

  // Compute normalized fractional sizes for currently expanded sections
  const expandedSizes = createMemo(() => {
    const expandedIds = expandableSectionIds().filter(sid => isOpen(sid))
    if (expandedIds.length <= 1)
      return new Map<string, number>()

    const sizes = sectionSizes()
    const result = new Map<string, number>()
    let total = 0

    for (const id of expandedIds) {
      const size = sizes[id] ?? (1 / expandedIds.length)
      result.set(id, size)
      total += size
    }

    // Normalize to sum to 1.0
    if (total > 0 && Math.abs(total - 1) > 0.001) {
      for (const [id, size] of result) {
        result.set(id, size / total)
      }
    }

    return result
  })

  /**
   * Start dragging a resize handle between two adjacent expanded sections.
   * handleIndex: 0-based index among expanded sections -- handle sits between
   * expandedIds[handleIndex] and expandedIds[handleIndex + 1].
   */
  const handleResizeStart = (handleIndex: number, e: MouseEvent) => {
    e.preventDefault()
    const container = containerRef()
    if (!container)
      return
    setDraggingHandleIndex(handleIndex)
    document.body.style.cursor = 'row-resize'

    const expandedIds = expandableSectionIds().filter(sid => isOpen(sid))
    const currentSizes = expandedSizes()

    const aboveId = expandedIds[handleIndex]
    const belowId = expandedIds[handleIndex + 1]
    const aboveSize = currentSizes.get(aboveId) ?? 0
    const belowSize = currentSizes.get(belowId) ?? 0
    const pairTotal = aboveSize + belowSize

    const startY = e.clientY
    const startAboveSize = aboveSize
    const containerHeight = container.getBoundingClientRect().height

    const onMouseMove = (moveEvent: MouseEvent) => {
      const deltaY = moveEvent.clientY - startY
      const deltaFraction = deltaY / containerHeight

      const newAboveSize = Math.max(
        MIN_FRACTION * pairTotal,
        Math.min(
          (1 - MIN_FRACTION) * pairTotal,
          startAboveSize + deltaFraction,
        ),
      )
      const newBelowSize = pairTotal - newAboveSize

      setSectionSizes(prev => ({
        ...prev,
        [aboveId]: newAboveSize,
        [belowId]: newBelowSize,
      }))
    }

    const onMouseUp = () => {
      setDraggingHandleIndex(null)
      document.body.style.cursor = ''
      document.removeEventListener('mousemove', onMouseMove)
      document.removeEventListener('mouseup', onMouseUp)
      notifyStateChange()
    }

    document.addEventListener('mousemove', onMouseMove)
    document.addEventListener('mouseup', onMouseUp)
  }

  /** Reset all expanded sections to equal sizes. */
  const handleResetSplit = () => {
    const expandedIds = expandableSectionIds().filter(sid => isOpen(sid))
    if (expandedIds.length < 2)
      return
    const equalSize = 1 / expandedIds.length
    setSectionSizes((prev) => {
      const next = { ...prev }
      for (const eid of expandedIds)
        next[eid] = equalSize
      return next
    })
    notifyStateChange()
  }

  return {
    expandedCount,
    expandedSizes,
    handleResizeStart,
    handleResetSplit,
  }
}
