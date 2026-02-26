import type { Droppable, DroppableLayout } from '~/components/shell/sectionDragUtils'
import { describe, expect, it } from 'vitest'
import {
  computeInsertPosition,
  findClosestSectionDroppable,
  isNearIndicator,
  PROXIMITY_BUFFER,
  SECTION_DRAG_PREFIX,
  SIDEBAR_ZONE_PREFIX,
  X_TOLERANCE,
} from '~/components/shell/sectionDragUtils'
import { Sidebar } from '~/generated/leapmux/v1/section_pb'
import { headerHeightPx } from '~/styles/tokens'

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

/** Create a section droppable with the given bounding box. */
function section(id: string, top: number, bottom: number, left = 0, right = 200): Droppable {
  return {
    id: `${SECTION_DRAG_PREFIX}${id}`,
    layout: {
      top,
      bottom,
      left,
      right,
      center: { x: (left + right) / 2, y: (top + bottom) / 2 },
    },
  }
}

/** Create a sidebar zone droppable covering the full sidebar area. */
function zone(side: 'left' | 'right', top = 0, bottom = 400, left = 0, right = 200): Droppable {
  return {
    id: `${SIDEBAR_ZONE_PREFIX}${side}`,
    layout: {
      top,
      bottom,
      left,
      right,
      center: { x: (left + right) / 2, y: (top + bottom) / 2 },
    },
  }
}

/** Create a minimal Section object for computeInsertPosition tests. */
function makeSection(id: string, sidebar: Sidebar, position: string) {
  return { id, sidebar, position } as any
}

// ---------------------------------------------------------------------------
// findClosestSectionDroppable
// ---------------------------------------------------------------------------

describe('findClosestSectionDroppable', () => {
  const dragId = `${SECTION_DRAG_PREFIX}dragged`

  describe('exact containment', () => {
    it('should return the section when ref is inside its bounding box', () => {
      const s = section('target', 100, 300)
      const result = findClosestSectionDroppable(dragId, [s], { x: 100, y: 200 })
      expect(result).toBe(s)
    })

    it('should return the section when ref is exactly on the boundary', () => {
      const s = section('target', 100, 300, 0, 200)
      expect(findClosestSectionDroppable(dragId, [s], { x: 0, y: 100 })).toBe(s)
      expect(findClosestSectionDroppable(dragId, [s], { x: 200, y: 300 })).toBe(s)
    })

    it('should pick the first section containing the ref when multiple overlap', () => {
      const s1 = section('a', 100, 300)
      const s2 = section('b', 200, 400)
      // ref at (100, 250) is inside both; s1 should win (first in array)
      const result = findClosestSectionDroppable(dragId, [s1, s2], { x: 100, y: 250 })
      expect(result).toBe(s1)
    })
  })

  describe('skip self', () => {
    it('should skip the dragged section', () => {
      const self = section('dragged', 100, 300)
      const other = section('other', 400, 500)
      const result = findClosestSectionDroppable(dragId, [self, other], { x: 100, y: 200 })
      expect(result).toBe(other)
    })
  })

  describe('gap containment', () => {
    // Two sections with an 8px gap between them (resize handle).
    const sA = section('a', 0, 200, 0, 200)
    const sB = section('b', 208, 408, 0, 200)
    const sideZone = zone('left', 0, 408)

    it('should prefer the nearest section edge in the inter-section gap', () => {
      // Cursor at y=202, which is 2px below A's bottom edge
      const result = findClosestSectionDroppable(dragId, [sA, sB, sideZone], { x: 100, y: 202 })
      expect(String(result!.id)).toBe(`${SECTION_DRAG_PREFIX}a`)
    })

    it('should prefer the closer section when cursor is in the middle of the gap', () => {
      // Cursor at y=204 (midpoint of gap: 200..208)
      const result = findClosestSectionDroppable(dragId, [sA, sB, sideZone], { x: 100, y: 204 })
      // Both are 4px away; last one wins in tie → B
      expect(String(result!.id)).toMatch(/^sidebar-section:/)
    })

    it('should prefer section B when cursor is closer to B top', () => {
      // Cursor at y=206, 6px from A bottom, 2px from B top
      const result = findClosestSectionDroppable(dragId, [sA, sB, sideZone], { x: 100, y: 206 })
      expect(String(result!.id)).toBe(`${SECTION_DRAG_PREFIX}b`)
    })

    it('should win over zone even when zone center is closer', () => {
      // The zone center is at y=204, exactly where the gap is.
      // Without gap containment, the zone would win the distance comparison.
      const result = findClosestSectionDroppable(dragId, [sA, sB, sideZone], { x: 100, y: 204 })
      expect(String(result!.id)).not.toContain(SIDEBAR_ZONE_PREFIX)
    })

    it('should require cursor to be within X bounds (+ tolerance)', () => {
      // Cursor is outside the section X bounds by more than X_TOLERANCE
      const result = findClosestSectionDroppable(dragId, [sA, sB, sideZone], { x: 200 + X_TOLERANCE + 1, y: 204 })
      // Gap check fails, falls to distance comparison
      expect(String(result!.id)).not.toBe(`${SECTION_DRAG_PREFIX}a`)
    })

    it('should allow cursor on the sidebar resize handle (within X tolerance)', () => {
      // Cursor is just outside X bounds but within tolerance
      const result = findClosestSectionDroppable(dragId, [sA, sB, sideZone], { x: 200 + X_TOLERANCE, y: 202 })
      expect(String(result!.id)).toBe(`${SECTION_DRAG_PREFIX}a`)
    })
  })

  describe('distance fallback', () => {
    it('should prefer section over zone when equidistant', () => {
      const s = section('s', 100, 200, 0, 200)
      const z = zone('left', 0, 400)
      // Place cursor outside section, equidistant from section center and zone center
      const sCenterY = 150
      const zCenterY = 200
      const y = (sCenterY + zCenterY) / 2 // exactly between the two centers
      const result = findClosestSectionDroppable(dragId, [s, z], { x: 100, y })
      // Section should be preferred (bestSectionDist <= bestZoneDist)
      expect(String(result!.id)).toBe(`${SECTION_DRAG_PREFIX}s`)
    })

    it('should pick zone when its center is strictly closer', () => {
      // Section far away at top, zone centered in middle
      const s = section('s', 0, 36, 0, 200)
      const z = zone('left', 0, 400)
      // Cursor near the zone center (200), far from section center (18)
      const result = findClosestSectionDroppable(dragId, [s, z], { x: 100, y: 300 })
      expect(String(result!.id)).toBe(`${SIDEBAR_ZONE_PREFIX}left`)
    })
  })

  describe('edge cases', () => {
    it('should return null when no droppables', () => {
      expect(findClosestSectionDroppable(dragId, [], { x: 0, y: 0 })).toBeNull()
    })

    it('should return null when only the dragged section is present', () => {
      const self = section('dragged', 0, 100)
      expect(findClosestSectionDroppable(dragId, [self], { x: 50, y: 50 })).toBeNull()
    })

    it('should return zone when no sections exist', () => {
      const z = zone('right')
      const result = findClosestSectionDroppable(dragId, [z], { x: 100, y: 200 })
      expect(String(result!.id)).toBe(`${SIDEBAR_ZONE_PREFIX}right`)
    })
  })
})

// ---------------------------------------------------------------------------
// isNearIndicator
// ---------------------------------------------------------------------------

describe('isNearIndicator', () => {
  // A section from (10, 100) to (210, 300)
  const layout: DroppableLayout = {
    top: 100,
    bottom: 300,
    left: 10,
    right: 210,
    center: { x: 110, y: 200 },
  }

  describe('before position', () => {
    // Zone: top - PROXIMITY_BUFFER to top + headerHeightPx + PROXIMITY_BUFFER
    const zoneTop = layout.top - PROXIMITY_BUFFER
    const zoneBottom = layout.top + headerHeightPx + PROXIMITY_BUFFER

    it('should return true when cursor is at the section top', () => {
      expect(isNearIndicator(layout, 'before', 100, layout.top)).toBe(true)
    })

    it('should return true when cursor is within the zone bounds', () => {
      expect(isNearIndicator(layout, 'before', 100, zoneTop)).toBe(true)
      expect(isNearIndicator(layout, 'before', 100, zoneBottom)).toBe(true)
    })

    it('should return false when cursor is above the zone', () => {
      expect(isNearIndicator(layout, 'before', 100, zoneTop - 1)).toBe(false)
    })

    it('should return false when cursor is below the zone', () => {
      expect(isNearIndicator(layout, 'before', 100, zoneBottom + 1)).toBe(false)
    })

    it('should return false when cursor X is outside section bounds', () => {
      expect(isNearIndicator(layout, 'before', layout.right + X_TOLERANCE + 1, layout.top)).toBe(false)
      expect(isNearIndicator(layout, 'before', layout.left - X_TOLERANCE - 1, layout.top)).toBe(false)
    })

    it('should return true when cursor X is within X_TOLERANCE of section edge', () => {
      expect(isNearIndicator(layout, 'before', layout.right + X_TOLERANCE, layout.top)).toBe(true)
      expect(isNearIndicator(layout, 'before', layout.left - X_TOLERANCE, layout.top)).toBe(true)
    })
  })

  describe('after position', () => {
    // Zone: bottom - headerHeightPx - PROXIMITY_BUFFER to bottom + PROXIMITY_BUFFER
    const zoneTop = layout.bottom - headerHeightPx - PROXIMITY_BUFFER
    const zoneBottom = layout.bottom + PROXIMITY_BUFFER

    it('should return true when cursor is at the section bottom', () => {
      expect(isNearIndicator(layout, 'after', 100, layout.bottom)).toBe(true)
    })

    it('should return true when cursor is within the zone bounds', () => {
      expect(isNearIndicator(layout, 'after', 100, zoneTop)).toBe(true)
      expect(isNearIndicator(layout, 'after', 100, zoneBottom)).toBe(true)
    })

    it('should return false when cursor is above the zone', () => {
      expect(isNearIndicator(layout, 'after', 100, zoneTop - 1)).toBe(false)
    })

    it('should return false when cursor is below the zone', () => {
      expect(isNearIndicator(layout, 'after', 100, zoneBottom + 1)).toBe(false)
    })
  })

  describe('expanded vs collapsed sections', () => {
    it('should have non-overlapping zones for a tall expanded section', () => {
      // Expanded section: 300px tall (100..400)
      const expanded: DroppableLayout = {
        top: 100,
        bottom: 400,
        left: 0,
        right: 200,
        center: { x: 100, y: 250 },
      }
      const midY = (expanded.top + expanded.bottom) / 2
      // At the midpoint, neither zone should match
      expect(isNearIndicator(expanded, 'before', 100, midY)).toBe(false)
      expect(isNearIndicator(expanded, 'after', 100, midY)).toBe(false)
    })

    it('should have overlapping zones for a collapsed section', () => {
      // Collapsed section: 36px tall (100..136)
      const collapsed: DroppableLayout = {
        top: 100,
        bottom: 136,
        left: 0,
        right: 200,
        center: { x: 100, y: 118 },
      }
      const midY = (collapsed.top + collapsed.bottom) / 2
      // At the midpoint of a collapsed section, both zones should match
      expect(isNearIndicator(collapsed, 'before', 100, midY)).toBe(true)
      expect(isNearIndicator(collapsed, 'after', 100, midY)).toBe(true)
    })
  })
})

// ---------------------------------------------------------------------------
// computeInsertPosition
// ---------------------------------------------------------------------------

describe('computeInsertPosition', () => {
  describe('same sidebar', () => {
    const sections = [
      makeSection('a', Sidebar.LEFT, 'aaa'),
      makeSection('b', Sidebar.LEFT, 'bbb'),
      makeSection('c', Sidebar.LEFT, 'ccc'),
    ]

    it('should return "after" when dragged section is above the target', () => {
      expect(computeInsertPosition('a', 'b', sections)).toBe('after')
      expect(computeInsertPosition('a', 'c', sections)).toBe('after')
      expect(computeInsertPosition('b', 'c', sections)).toBe('after')
    })

    it('should return "before" when dragged section is below the target', () => {
      expect(computeInsertPosition('c', 'b', sections)).toBe('before')
      expect(computeInsertPosition('c', 'a', sections)).toBe('before')
      expect(computeInsertPosition('b', 'a', sections)).toBe('before')
    })
  })

  describe('cross-sidebar with draggable/droppable', () => {
    const sections = [
      makeSection('a', Sidebar.LEFT, 'aaa'),
      makeSection('b', Sidebar.RIGHT, 'bbb'),
    ]

    it('should return "before" when drag ref is above the header midpoint', () => {
      const draggable = { transformed: { center: { y: 100 } }, layout: { height: 36 } }
      const droppable = { layout: { top: 100 } }
      // refY = 100 - 18 = 82, headerMidY = 100 + 18 = 118
      expect(computeInsertPosition('a', 'b', sections, draggable, droppable)).toBe('before')
    })

    it('should return "after" when drag ref is below the header midpoint', () => {
      const draggable = { transformed: { center: { y: 200 } }, layout: { height: 36 } }
      const droppable = { layout: { top: 100 } }
      // refY = 200 - 18 = 182, headerMidY = 100 + 18 = 118
      expect(computeInsertPosition('a', 'b', sections, draggable, droppable)).toBe('after')
    })
  })

  describe('missing sections', () => {
    it('should return "before" when dragged section is not found', () => {
      const sections = [makeSection('b', Sidebar.LEFT, 'bbb')]
      expect(computeInsertPosition('nonexistent', 'b', sections)).toBe('before')
    })

    it('should return "before" when target section is not found', () => {
      const sections = [makeSection('a', Sidebar.LEFT, 'aaa')]
      expect(computeInsertPosition('a', 'nonexistent', sections)).toBe('before')
    })
  })

  describe('cross-sidebar without draggable/droppable', () => {
    it('should return "before" as default fallback', () => {
      const sections = [
        makeSection('a', Sidebar.LEFT, 'aaa'),
        makeSection('b', Sidebar.RIGHT, 'bbb'),
      ]
      expect(computeInsertPosition('a', 'b', sections)).toBe('before')
    })
  })
})
