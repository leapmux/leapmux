import type { Accessor, Setter } from 'solid-js'
import { createMemo } from 'solid-js'
import { rebalancePair } from '~/lib/pairDrag'
import { useWindowPointerDrag } from './windowPointerDrag'

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
  /** Optional default fractional size per section ID (used instead of 1/N). */
  defaultSizes?: Accessor<Map<string, number>>
}

export interface UseResizeHandleReturn {
  /** Number of currently expanded sections. */
  expandedCount: Accessor<number>
  /** Normalized fractional sizes for currently expanded sections. */
  expandedSizes: Accessor<Map<string, number>>
  /** Start dragging a resize handle between two adjacent expanded sections. */
  handleResizeStart: (handleIndex: number, e: PointerEvent) => void
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
    defaultSizes,
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

    const defaults = defaultSizes?.()
    for (const id of expandedIds) {
      const size = sizes[id] ?? defaults?.get(id) ?? (1 / expandedIds.length)
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

  const drag = useWindowPointerDrag()

  /**
   * Start dragging a resize handle between two adjacent expanded sections.
   * handleIndex: 0-based index among expanded sections -- handle sits between
   * expandedIds[handleIndex] and expandedIds[handleIndex + 1].
   */
  const handleResizeStart = (handleIndex: number, e: PointerEvent) => {
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

    drag.start({
      coalesce: true,
      onMove: (moveEvent) => {
        const deltaY = moveEvent.clientY - startY
        const deltaFraction = deltaY / containerHeight
        // Floor scales with the pair: each side keeps at least MIN_FRACTION
        // (15%) of what the pair owns, not 15% of the whole container.
        const [newAboveSize, newBelowSize] = rebalancePair(
          startAboveSize,
          pairTotal,
          deltaFraction,
          MIN_FRACTION * pairTotal,
        )
        setSectionSizes((prev) => {
          // Skip the emit + downstream re-render once the drag is pinned
          // at the floor and producing identical sizes per tick.
          if (prev[aboveId] === newAboveSize && prev[belowId] === newBelowSize)
            return prev
          return { ...prev, [aboveId]: newAboveSize, [belowId]: newBelowSize }
        })
      },
      onUp: () => {
        notifyStateChange()
      },
      // Cursor + dragging-handle reset must fire even on a bare click
      // (pointerdown → pointerup with no pointermove between). `onUp` is
      // gated on `moved`, so cleanup goes in `onFinish`.
      onFinish: () => {
        setDraggingHandleIndex(null)
        document.body.style.cursor = ''
      },
    })
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
