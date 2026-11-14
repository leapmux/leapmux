import type { LayoutSnapshot } from './chatLayoutShift'
import { describe, expect, it, vi } from 'vitest'
import { nextLayoutShift } from './chatLayoutShift'

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
