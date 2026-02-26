import type { Section } from '~/generated/leapmux/v1/section_pb'
import { headerHeightPx } from '~/styles/tokens'

/** Prefix for section draggable IDs. */
export const SECTION_DRAG_PREFIX = 'sidebar-section:'

/** Prefix for sidebar drop-zone droppable IDs. */
export const SIDEBAR_ZONE_PREFIX = 'sidebar-zone:'

/** Horizontal tolerance so the indicator still activates over the resize handle. */
export const X_TOLERANCE = 4

/**
 * Vertical buffer for the gap between section droppables (pane resize handle).
 * When the cursor is in this gap, we still attribute it to the nearest section
 * rather than letting the sidebar zone win the distance comparison.
 */
export const INTER_SECTION_GAP = 8

/** Extra buffer above/below the header for proximity activation. */
export const PROXIMITY_BUFFER = Math.round(headerHeightPx / 2)

/** Droppable layout shape used by collision detection and proximity checks. */
export interface DroppableLayout {
  top: number
  bottom: number
  left: number
  right: number
  center: { x: number, y: number }
}

/** Minimal droppable shape used by collision detection. */
export interface Droppable {
  id: string | number
  layout: DroppableLayout
}

/**
 * Find the closest section/zone droppable for a section drag.
 * Uses a reference point for containment and distance checks.
 * If the ref is inside a section's bounding box, that section wins immediately.
 * Falls back to distance comparison, preferring sections over zones.
 */
export function findClosestSectionDroppable(
  dragId: string,
  droppables: Droppable[],
  ref: { x: number, y: number },
): Droppable | null {
  // Section whose edge is closest when the cursor is in the inter-section gap
  // (e.g., pane resize handle). Preferred over zones to avoid the sidebar zone
  // winning the distance comparison when its center is closer.
  let gapSection: Droppable | null = null
  let gapEdgeDist = Infinity

  let bestSection: Droppable | null = null
  let bestSectionDist = Infinity
  let bestZone: Droppable | null = null
  let bestZoneDist = Infinity

  for (const d of droppables) {
    const id = String(d.id)
    if (id === dragId)
      continue

    if (id.startsWith(SECTION_DRAG_PREFIX)) {
      const l = d.layout

      // Exact containment: reference point inside the section's bounding box
      if (
        ref.x >= l.left && ref.x <= l.right
        && ref.y >= l.top && ref.y <= l.bottom
      ) {
        return d
      }

      // Gap containment: cursor is just outside the section's Y bounds
      // (within the resize handle gap) but within the section's X bounds.
      // Track the section whose edge is closest.
      if (ref.x >= l.left - X_TOLERANCE && ref.x <= l.right + X_TOLERANCE) {
        const edgeDist = ref.y < l.top ? l.top - ref.y : ref.y - l.bottom
        if (edgeDist <= INTER_SECTION_GAP && edgeDist < gapEdgeDist) {
          gapSection = d
          gapEdgeDist = edgeDist
        }
      }

      const dc = l.center
      const dist = Math.sqrt((ref.x - dc.x) ** 2 + (ref.y - dc.y) ** 2)
      if (dist < bestSectionDist) {
        bestSectionDist = dist
        bestSection = d
      }
    }
    else if (id.startsWith(SIDEBAR_ZONE_PREFIX)) {
      const dc = d.layout.center
      const dist = Math.sqrt((ref.x - dc.x) ** 2 + (ref.y - dc.y) ** 2)
      if (dist < bestZoneDist) {
        bestZoneDist = dist
        bestZone = d
      }
    }
  }

  // Prefer gap containment: when the cursor is in the inter-section gap,
  // always pick the nearest section rather than falling through to distance
  // comparison where the sidebar zone's center might be closer.
  if (gapSection)
    return gapSection

  if (bestSection && bestSectionDist <= bestZoneDist)
    return bestSection
  if (bestZone && bestZoneDist < bestSectionDist)
    return bestZone
  return bestSection ?? bestZone
}

/**
 * Compute the insertion position (before/after) relative to a target section,
 * given the dragged section and all sections. Used as a fallback when the
 * indicator position is unavailable.
 */
export function computeInsertPosition(
  sectionId: string,
  targetSectionId: string,
  sections: Section[],
  draggable?: { transformed: { center: { y: number } }, layout: { height: number } },
  droppable?: { layout: { top: number } },
): 'before' | 'after' {
  const draggedSection = sections.find(s => s.id === sectionId)
  const targetSection = sections.find(s => s.id === targetSectionId)
  if (!draggedSection || !targetSection)
    return 'before'

  if (draggedSection.sidebar === targetSection.sidebar) {
    // Same sidebar: compare indices to determine direction
    const sidebarSections = sections
      .filter(s => s.sidebar === targetSection.sidebar)
      .sort((a, b) => a.position.localeCompare(b.position))
    const dragIdx = sidebarSections.findIndex(s => s.id === sectionId)
    const dropIdx = sidebarSections.findIndex(s => s.id === targetSectionId)
    return dragIdx < dropIdx ? 'after' : 'before'
  }

  // Cross-sidebar: compare the drag reference point (top of dragged element)
  // against the target section's header midpoint to determine before/after.
  if (draggable && droppable) {
    const refY = draggable.transformed.center.y - (draggable.layout.height / 2)
    const headerMidY = droppable.layout.top + headerHeightPx / 2
    return refY < headerMidY ? 'before' : 'after'
  }

  return 'before'
}

/**
 * Check if a pointer position is within the proximity zone of a drop indicator line.
 * The indicator line renders at the section top ('before') or bottom ('after').
 */
export function isNearIndicator(
  droppableLayout: DroppableLayout,
  position: 'before' | 'after',
  pointerX: number,
  pointerY: number,
): boolean {
  let zoneTop: number
  let zoneBottom: number
  if (position === 'before') {
    zoneTop = droppableLayout.top - PROXIMITY_BUFFER
    zoneBottom = droppableLayout.top + headerHeightPx + PROXIMITY_BUFFER
  }
  else {
    zoneBottom = droppableLayout.bottom + PROXIMITY_BUFFER
    zoneTop = droppableLayout.bottom - headerHeightPx - PROXIMITY_BUFFER
  }
  return pointerX >= droppableLayout.left - X_TOLERANCE && pointerX <= droppableLayout.right + X_TOLERANCE
    && pointerY >= zoneTop && pointerY <= zoneBottom
}
