import type { LayoutSnapshot } from './chatLayoutShift'
import { describe, expect, it, vi } from 'vitest'
import { nextLayoutShift, ROW_ALIGN_OFFSET_PX, rowCutAt, TOP_EDGE_SLACK_PX } from './chatLayoutShift'

interface RowSpec {
  id: string
  height: number
  /** False for a row whose height is an estimate. Default: true. */
  measured?: boolean
}

/** A snapshot of `rows` with `gap` px between two adjacent rows, as the virtualizer builds it. */
function snapshot(rows: readonly RowSpec[], gap = 0): LayoutSnapshot {
  const n = rows.length
  const offsets = new Float64Array(n + 1)
  const heights = new Float64Array(n)
  const measured = new Uint8Array(n)
  const indexById = new Map<string, number>()
  rows.forEach((row, i) => {
    heights[i] = row.height
    measured[i] = row.measured === false
      ? 0
      : 1
    indexById.set(row.id, i)
    const gapBelow = i < n - 1
      ? gap
      : 0
    offsets[i + 1] = (offsets[i] ?? 0) + row.height + gapBelow
  })
  return {
    list: rows.map(row => ({ id: row.id })),
    indexById,
    offsets,
    heights,
    measured,
    n,
  }
}

/** `count` rows of `height` px with the ids `r0`, `r1`, ... */
function rows(count = 10, height = 100): RowSpec[] {
  return Array.from({ length: count }, (_, i) => ({ id: `r${i}`, height }))
}

/** `list` with the height of row `id` replaced. */
function withHeight(list: readonly RowSpec[], id: string, height: number, measured = true): RowSpec[] {
  return list.map(row => (row.id === id
    ? { id, height, measured }
    : row))
}

/**
 * The next shift for a viewport top at content `c` of the previous snapshot. The scroll
 * position is `c + p`, as the shift rule defines it. Returns the shift and the number of
 * `intraRowShift` calls.
 */
function shiftFor(input: {
  previous: readonly RowSpec[] | undefined
  next: readonly RowSpec[]
  c: number
  p?: number
  gap?: number
  pinBottom?: boolean
  intraRowShift?: number
}) {
  const p = input.p ?? -40
  const gap = input.gap ?? 0
  const intraRowShift = vi.fn((_rowId: string) => input.intraRowShift ?? 0)
  const shift = nextLayoutShift({
    previous: input.previous === undefined
      ? undefined
      : snapshot(input.previous, gap),
    next: snapshot(input.next, gap),
    previousShift: p,
    scrollTop: input.c + p,
    pinBottom: input.pinBottom ?? false,
    intraRowShift,
  })
  return { shift, calls: intraRowShift.mock.calls.length, intraRowShift }
}

describe('nextLayoutShift', () => {
  it('returns 0 without a previous snapshot', () => {
    expect(shiftFor({
      previous: undefined,
      next: rows(),
      c: 230,
    })).toMatchObject({ shift: 0, calls: 0 })
  })

  it('returns 0 for an empty next snapshot', () => {
    expect(shiftFor({
      previous: rows(),
      next: [],
      c: 230,
    })).toMatchObject({ shift: 0, calls: 0 })
  })

  it('returns 0 when the previous snapshot has no row', () => {
    expect(shiftFor({
      previous: [],
      next: rows(),
      c: 230,
    })).toMatchObject({ shift: 0, calls: 0 })
  })

  it('keeps the content bottom while pinned', () => {
    expect(shiftFor({
      previous: rows(10),
      next: rows(13),
      c: 300,
      p: -200,
      pinBottom: true,
    }))
      .toMatchObject({ shift: -500, calls: 0 })
  })

  it('keeps the spacer height at 0 or more while pinned', () => {
    // A previous spacer of 1000 - 1200 is below 0 only for a shift that no rule produced.
    expect(shiftFor({
      previous: rows(10),
      next: rows(1),
      c: 300,
      p: -1200,
      pinBottom: true,
    }))
      .toMatchObject({ shift: -100, calls: 0 })
  })

  it('holds the top edge of a row at the viewport top', () => {
    expect(shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r1', 140),
      c: 300,
      p: 0,
    }))
      .toMatchObject({ shift: -40, calls: 0 })
  })

  it('holds the top edge of a held row that grows', () => {
    // Row 1 grows by 40, and row 3, the held row, grows by 60. Row 3 spans 300 to 400, so
    // the top-edge case gives c' = top'(3) + (300 - 300) = 340 and a shift of
    // 300 - 340 = -40. The bottom-edge formula gives c' = bottom'(3) + (300 - 400) =
    // 500 - 100 = 400 and a shift of -100.
    expect(shiftFor({
      previous: rows(),
      next: withHeight(withHeight(rows(), 'r1', 140), 'r3', 160),
      c: 300,
      p: 0,
    }))
      .toMatchObject({ shift: -40, calls: 0 })
  })

  it('holds the top edge within the slack of one pixel', () => {
    expect(shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r1', 140),
      c: 301,
      p: 0,
    }))
      .toMatchObject({ shift: -40, calls: 0 })
  })

  it('holds the top edge of the first row when the viewport top is above it', () => {
    // A positive shift leaves a blank band above the first row: content -30 is in it.
    expect(shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r1', 140),
      c: -30,
      p: 50,
    }))
      .toMatchObject({ shift: 50, calls: 0 })
  })

  it('holds the bottom edge of an unmeasured row', () => {
    const previous = withHeight(rows(), 'r2', 100, false)
    expect(shiftFor({
      previous,
      next: withHeight(rows(), 'r2', 180),
      c: 230,
    }))
      .toMatchObject({ shift: -40 - 80, calls: 0 })
  })

  it('holds the bottom edge when the viewport top is in a gap', () => {
    // Row 2 spans 240 to 340, so content 350 is 10px into the gap below it.
    expect(shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r2', 150),
      c: 350,
      gap: 20,
    }))
      .toMatchObject({ shift: -40 - 50, calls: 0 })
  })

  it('holds the top edge of a row that a seek placed just below the viewport top', () => {
    // Two bands overlap by their border (a gap of -1): row 1 spans 99 to 199. A seek
    // places row 1 ROW_ALIGN_OFFSET_PX below the viewport top, inside the body of row 0.
    // Row 0 then grows by 60px, and row 1 must keep its scroll coordinate.
    expect(shiftFor({
      previous: rows(3),
      next: withHeight(rows(3), 'r0', 160),
      c: 99 - ROW_ALIGN_OFFSET_PX,
      gap: -1,
    }))
      .toMatchObject({ shift: -40 - 60, calls: 0 })
  })

  it('holds the offset inside a measured row that grows at its bottom', () => {
    const result = shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r2', 200),
      c: 230,
    })
    expect(result).toMatchObject({ shift: -40, calls: 1 })
    expect(result.intraRowShift).toHaveBeenCalledWith('r2')
  })

  it('adds the measured displacement inside the row', () => {
    expect(shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r2', 200),
      c: 230,
      intraRowShift: 25,
    }))
      .toMatchObject({ shift: -40 - 25, calls: 1 })
  })

  it('clamps the held offset to the new height', () => {
    // Row 2 spans 200 to 600, so content 500 is 300px into it. It shrinks to 100px.
    const previous = withHeight(rows(), 'r2', 400)
    expect(shiftFor({
      previous,
      next: withHeight(rows(), 'r2', 100),
      c: 500,
    }))
      .toMatchObject({ shift: -40 + 200, calls: 1 })
  })

  it('clamps a negative held offset to the top edge', () => {
    const result = shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r2', 200),
      c: 230,
      intraRowShift: -50,
    })
    // The content inside the row moved up by 50px, more than the 30px between the row top
    // and the viewport top, so the viewport top holds the top edge of the row.
    expect(result).toMatchObject({ shift: -40 + 30, calls: 1 })
  })

  it('keeps the rows in place across a prepend', () => {
    const prepended = [
      ...rows(5).map(row => ({ ...row, id: `old-${row.id}` })),
      ...rows(),
    ]
    expect(shiftFor({
      previous: rows(),
      next: prepended,
      c: 250,
    }))
      .toMatchObject({ shift: -40 - 500, calls: 1 })
  })

  it('keeps the rows in place across a trim above', () => {
    expect(shiftFor({
      previous: rows(),
      next: rows().slice(2),
      c: 450,
    }))
      .toMatchObject({ shift: -40 + 200, calls: 1 })
  })

  it('uses the nearest lower row when the pivot row is removed', () => {
    const next = rows().filter(row => row.id !== 'r2')
    expect(shiftFor({
      previous: rows(),
      next,
      c: 250,
    })).toMatchObject({ shift: -40 + 100, calls: 0 })
  })

  it('uses the nearest upper row when no lower row remains', () => {
    expect(shiftFor({
      previous: rows(),
      next: rows().slice(0, 8),
      c: 850,
    })).toMatchObject({ shift: -40, calls: 0 })
  })

  it('holds the bottom edge of the nearest upper row that grows', () => {
    // Rows 8 and 9 are removed, and row 7 grows by 60. Row 7 spans 700 to 800 before and
    // 700 to 860 after, so c' = bottom'(7) + (850 - 800) = 860 + 50 = 910. The scroll
    // position is 850 - 40 = 810, so the shift is 810 - 910 = -100: `p` minus the growth.
    // The top-edge formula gives c' = top'(7) + (850 - 700) = 850 and a shift of -40.
    expect(shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r7', 160).slice(0, 8),
      c: 850,
    })).toMatchObject({ shift: -100, calls: 0 })
  })

  it('returns 0 when no row exists in both snapshots', () => {
    const next = rows().map(row => ({ ...row, id: `new-${row.id}` }))
    expect(shiftFor({
      previous: rows(),
      next,
      c: 250,
    })).toMatchObject({ shift: 0, calls: 0 })
  })

  it('keeps the spacer height at 0 or more', () => {
    expect(shiftFor({
      previous: rows(),
      next: rows().slice(0, 1),
      c: 950,
      p: -940,
    }))
      .toMatchObject({ shift: -100, calls: 0 })
  })
})

describe('rowCutAt', () => {
  // Rows of 100px with a gap of 20px: row i spans 120 * i to 120 * i + 100.
  const cutAt = (contentTop: number, list: readonly RowSpec[] = rows(3)) => rowCutAt(snapshot(list, 20), contentTop)

  it('returns the top edge at the top of a row and within the slack below it', () => {
    expect(cutAt(120)).toEqual({
      kind: 'top-edge',
      index: 1,
      top: 120,
      bottom: 220,
    })
    expect(cutAt(120 + TOP_EDGE_SLACK_PX).kind).toBe('top-edge')
  })

  it('returns the top edge of the first row for a viewport top above that row', () => {
    expect(cutAt(-50)).toEqual({
      kind: 'top-edge',
      index: 0,
      top: 0,
      bottom: 100,
    })
  })

  it('returns the body below the slack inside a measured row', () => {
    expect(cutAt(120 + TOP_EDGE_SLACK_PX + 0.5)).toEqual({
      kind: 'body',
      index: 1,
      top: 120,
      bottom: 220,
    })
    expect(cutAt(219.5).kind).toBe('body')
  })

  it('returns the bottom edge from the bottom of a row through the gap below it', () => {
    expect(cutAt(220)).toEqual({
      kind: 'bottom-edge',
      index: 1,
      top: 120,
      bottom: 220,
    })
    // Just above the alignment distance from the top edge of the next row.
    expect(cutAt(240 - ROW_ALIGN_OFFSET_PX - TOP_EDGE_SLACK_PX - 0.5)).toMatchObject({ kind: 'bottom-edge', index: 1 })
  })

  it('returns the top edge of the next row within the alignment distance above it', () => {
    expect(cutAt(240 - ROW_ALIGN_OFFSET_PX)).toEqual({
      kind: 'top-edge',
      index: 2,
      top: 240,
      bottom: 340,
    })
    expect(cutAt(240 - ROW_ALIGN_OFFSET_PX - TOP_EDGE_SLACK_PX)).toMatchObject({ kind: 'top-edge', index: 2 })
    expect(cutAt(239.5)).toMatchObject({ kind: 'top-edge', index: 2 })
  })

  it('returns the top edge of the next row inside the body of a band above it', () => {
    // Two bands overlap by their border: row 1 spans 99 to 199.
    const cut = rowCutAt(snapshot(rows(3), -1), 99 - ROW_ALIGN_OFFSET_PX)
    expect(cut).toEqual({
      kind: 'top-edge',
      index: 1,
      top: 99,
      bottom: 199,
    })
  })

  it('returns the bottom edge anywhere below the slack in a row whose height is an estimate', () => {
    const list = withHeight(rows(3), 'r1', 100, false)
    expect(cutAt(120, list).kind).toBe('top-edge')
    expect(cutAt(170, list)).toEqual({
      kind: 'bottom-edge',
      index: 1,
      top: 120,
      bottom: 220,
    })
  })

  it('returns the bottom edge of the last row below the content', () => {
    expect(cutAt(500)).toEqual({
      kind: 'bottom-edge',
      index: 2,
      top: 240,
      bottom: 340,
    })
  })
})
