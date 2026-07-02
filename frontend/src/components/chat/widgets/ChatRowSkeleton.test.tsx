import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import {
  ChatRowSkeleton,
  SKELETON_LINE_HEIGHT_PX,
  SKELETON_LINE_PITCH_PX,
  SKELETON_TILE_LINES,
  skeletonLineWidthPercent,
  skeletonMaskSvg,
} from './ChatRowSkeleton'

describe('chatrowskeleton', () => {
  it('derives deterministic, varied line widths within the 55-95% band', () => {
    const widths = Array.from({ length: 8 }, (_, i) => skeletonLineWidthPercent('m42', i))
    for (const width of widths) {
      expect(width).toBeGreaterThanOrEqual(55)
      expect(width).toBeLessThan(96)
    }
    // Deterministic: same (seed, index) -> same width, every time.
    expect(Array.from({ length: 8 }, (_, i) => skeletonLineWidthPercent('m42', i))).toEqual(widths)
    // Varied: a run of lines is not one uniform bar ("ragged text", not a slab).
    expect(new Set(widths).size).toBeGreaterThan(1)
    // Seeded per row: a different row id yields a different pattern.
    const other = Array.from({ length: 8 }, (_, i) => skeletonLineWidthPercent('m43', i))
    expect(other).not.toEqual(widths)
  })

  it('builds a repeat-y mask tile of seeded ragged bars', () => {
    const uri = skeletonMaskSvg('m7')
    expect(uri.startsWith('data:image/svg+xml,')).toBe(true)
    const svg = decodeURIComponent(uri.slice('data:image/svg+xml,'.length))
    // One bar per tile line, at the seeded width and the shared vertical rhythm.
    const rects = [...svg.matchAll(/<rect x='0' y='(\d+)' width='(\d+)' height='(\d+)'\/>/g)]
    expect(rects).toHaveLength(SKELETON_TILE_LINES)
    rects.forEach((m, i) => {
      expect(Number(m[1])).toBe(i * SKELETON_LINE_PITCH_PX)
      expect(Number(m[2])).toBe(skeletonLineWidthPercent('m7', i))
      expect(Number(m[3])).toBe(SKELETON_LINE_HEIGHT_PX)
    })
    // Percent-width mapping: viewBox is 100 wide, stretched to the element.
    expect(svg).toContain(`viewBox='0 0 100 ${SKELETON_TILE_LINES * SKELETON_LINE_PITCH_PX}'`)
    expect(svg).toContain(`preserveAspectRatio='none'`)
    // Deterministic per seed.
    expect(skeletonMaskSvg('m7')).toBe(uri)
    expect(skeletonMaskSvg('m8')).not.toBe(uri)
  })

  it('renders the container at the exact height with ONE masked Oat fill block', () => {
    const { container } = render(() => <ChatRowSkeleton height={120} seed="m7" />)
    const root = container.querySelector('[data-testid="row-skeleton"]') as HTMLElement
    expect(root).toBeInTheDocument()
    expect(root.style.height).toBe('120px')

    // A single fill block covers ANY height (the mask tiles the bars), so a
    // very tall row has no line-count cliff below which it goes blank.
    const fills = [...root.querySelectorAll('.skeleton.line')] as HTMLElement[]
    expect(fills).toHaveLength(1)
    // LOAD-BEARING: Oat's component selector is `[role=status].skeleton` — a
    // fill without the role renders 0-height and transparent (the
    // invisible-skeleton regression this pins against).
    expect(fills[0].getAttribute('role')).toBe('status')
    // The mask itself is a jsdom-invisible style (cssstyle drops mask-*);
    // its content is pinned via skeletonMaskSvg above.
  })
})
