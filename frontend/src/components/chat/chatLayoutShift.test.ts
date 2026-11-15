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
 * The next shift for a viewport top at content `c` of the previous snapshot, in a view of
 * `viewHeight` px. The scroll position is `c + p`, as the shift rule defines it.
 *
 * - `sawHeight` is the answer for every row: by default, the reader saw each row at its
 *   previous height.
 * - `enteredFromAbove` holds the ids of the rows that came into the view over its top
 *   edge: by default, no row did.
 *
 * Returns the shift, the number of `intraRowShift` calls, and the number of `sawHeight`
 * calls.
 */
function shiftFor(input: {
  previous: readonly RowSpec[] | undefined
  next: readonly RowSpec[]
  c: number
  p?: number
  gap?: number
  viewHeight?: number
  pinBottom?: boolean
  intraRowShift?: number
  sawHeight?: boolean
  enteredFromAbove?: readonly string[]
}) {
  const p = input.p ?? -40
  const gap = input.gap ?? 0
  const intraRowShift = vi.fn((_rowId: string) => input.intraRowShift ?? 0)
  const sawHeight = vi.fn((_rowId: string) => input.sawHeight ?? true)
  const enteredFromAbove = vi.fn((rowId: string) => input.enteredFromAbove?.includes(rowId) ?? false)
  const shift = nextLayoutShift({
    previous: input.previous === undefined
      ? undefined
      : snapshot(input.previous, gap),
    next: snapshot(input.next, gap),
    previousShift: p,
    scrollTop: input.c + p,
    viewHeight: input.viewHeight ?? 500,
    pinBottom: input.pinBottom ?? false,
    intraRowShift,
    sawHeight,
    enteredFromAbove,
  })
  return {
    shift,
    calls: intraRowShift.mock.calls.length,
    intraRowShift,
    sawCalls: sawHeight.mock.calls.length,
    sawHeight,
    enteredFromAbove,
  }
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

  it('keeps the content bottom while pinned, also when the row at the viewport top changes a height the reader did not see', () => {
    // Follow mode holds the content bottom, so the rule asks about no row.
    expect(shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r2', 180),
      c: 230,
      pinBottom: true,
      sawHeight: false,
    }))
      .toMatchObject({ shift: -40 - 80, calls: 0, sawCalls: 0 })
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

  it('holds the bottom edge of a measured row that changes a height the reader did not see', () => {
    // Row 2 spans 200 to 300, with a cached height that its mounted copy never showed: a
    // fling skeleton stood in for it. The first read of the copy gives 180px, and the rows
    // below row 2 keep their scroll coordinates.
    const result = shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r2', 180),
      c: 230,
      sawHeight: false,
    })
    expect(result).toMatchObject({ shift: -40 - 80, calls: 0, sawCalls: 1 })
    expect(result.sawHeight).toHaveBeenCalledWith('r2')
  })

  it('holds the bottom edge of a row that shrinks from a height the reader did not see', () => {
    // Row 2 spans 200 to 600, and content 500 is 100px above its bottom edge. The row
    // shrinks to 100px, so its bottom edge moves to 300 and the held point to 200.
    const previous = withHeight(rows(), 'r2', 400)
    expect(shiftFor({
      previous,
      next: withHeight(rows(), 'r2', 100),
      c: 500,
      sawHeight: false,
    }))
      .toMatchObject({ shift: -40 + 300, calls: 0, sawCalls: 1 })
  })

  it('holds the bottom edge of a row that a first read leaves with no height', () => {
    // Row 2 spans 200 to 300 and becomes an empty row. Its bottom edge moves to 200, and
    // the held point lies 70px above it, as before.
    expect(shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r2', 0),
      c: 230,
      sawHeight: false,
    }))
      .toMatchObject({ shift: -40 + 100, calls: 0, sawCalls: 1 })
  })

  it('holds the bottom edge of the last row when a first read changes it', () => {
    // Row 9 spans 900 to 1000 and grows to 250px. The content below the viewport top keeps
    // its scroll coordinate up to the content bottom.
    expect(shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r9', 250),
      c: 960,
      sawHeight: false,
    }))
      .toMatchObject({ shift: -40 - 150, calls: 0, sawCalls: 1 })
  })

  it('holds the bottom edge of the row at the viewport top when a row above it changes in the same step', () => {
    // Row 0 grows by 60px and row 2 by 30px, both at their first read, as two rows that
    // mount in one flush do. Row 2 then spans 260 to 390, and the rows below it keep their
    // scroll coordinates: the shift takes both changes.
    const next = withHeight(withHeight(rows(), 'r0', 160), 'r2', 130)
    expect(shiftFor({
      previous: rows(),
      next,
      c: 230,
      sawHeight: false,
    }))
      .toMatchObject({ shift: -40 - 60 - 30, calls: 0, sawCalls: 1 })
  })

  it('holds the bottom edge of an unmeasured row without asking whether the reader saw it', () => {
    const previous = withHeight(rows(), 'r2', 100, false)
    expect(shiftFor({
      previous,
      next: withHeight(rows(), 'r2', 180),
      c: 230,
      sawHeight: true,
    }))
      .toMatchObject({ shift: -40 - 80, calls: 0, sawCalls: 0 })
  })

  it('reads the pivot element, and not the edge rule, for a row at a height the reader saw', () => {
    expect(shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r2', 180),
      c: 230,
      intraRowShift: 25,
      sawHeight: true,
    }))
      .toMatchObject({ shift: -40 - 25, calls: 1, sawCalls: 1 })
  })

  it('does not ask whether the reader saw a row that keeps its height', () => {
    // Row 5 grows below the viewport top. Row 2 holds the viewport top and keeps its
    // height, so each edge of it gives the same shift, and only the pivot element can move.
    expect(shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r5', 180),
      c: 230,
      sawHeight: false,
    }))
      .toMatchObject({ shift: -40, calls: 1, sawCalls: 0 })
  })

  it('holds the top edge of a row at the viewport top, also at a height the reader did not see', () => {
    // A seek or a restore placed the top edge of row 2 at the viewport top.
    expect(shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r2', 180),
      c: 200,
      sawHeight: false,
    }))
      .toMatchObject({ shift: -40, calls: 0, sawCalls: 0 })
  })

  it('holds the bottom edge when the viewport top is in a gap', () => {
    // Row 2 spans 240 to 340, so content 350 is 10px into the gap below it.
    expect(shiftFor({
      previous: rows(),
      next: withHeight(rows(), 'r2', 150),
      c: 350,
      gap: 20,
      sawHeight: false,
    }))
      .toMatchObject({ shift: -40 - 50, calls: 0, sawCalls: 0 })
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
      sawHeight: false,
    })).toMatchObject({ shift: -40 + 100, calls: 0, sawCalls: 0 })
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

// A late row waited behind a placeholder for its content, and the reader scrolled up to it:
// it came into the view over the top edge, and this step corrects a height that the reader
// did not see. In each case below, the rows are 100px tall with no gap, the shift is -40,
// and the view is 500px tall unless the case says otherwise.
describe('nextLayoutShift with a late row inside the view', () => {
  it('holds the bottom edge of a late row when more of the view lies below it than above it', () => {
    // The view is [230, 730]: 70px of it above r3, and 330px below r3.
    const before = withHeight(rows(), 'r3', 100, false)
    const after = withHeight(rows(), 'r3', 1000)

    const result = shiftFor({
      previous: before,
      next: after,
      c: 230,
      enteredFromAbove: ['r3'],
    })

    // The bottom edge of r3 moves from 400 to 1300 in the content, and stays on screen.
    expect(result).toMatchObject({ shift: -940, calls: 0 })
  })

  it('holds the viewport top for a row that did not come into the view over the top edge', () => {
    const before = withHeight(rows(), 'r3', 100, false)
    const after = withHeight(rows(), 'r3', 1000)

    expect(shiftFor({ previous: before, next: after, c: 230 }).shift).toBe(-40)
  })

  it('holds the viewport top when more of the view lies above the late row than below it', () => {
    // 370px of the view above r6, and 30px below it.
    const before = withHeight(rows(), 'r6', 100, false)
    const after = withHeight(rows(), 'r6', 1000)

    expect(shiftFor({
      previous: before,
      next: after,
      c: 230,
      enteredFromAbove: ['r6'],
    }).shift).toBe(-40)
  })

  it('holds the viewport top when as much of the view lies above the late row as below it', () => {
    // The view is [250, 650]: 150px above r4, and 150px below it.
    const before = withHeight(rows(), 'r4', 100, false)
    const after = withHeight(rows(), 'r4', 1000)

    expect(shiftFor({
      previous: before,
      next: after,
      c: 250,
      viewHeight: 400,
      enteredFromAbove: ['r4'],
    }).shift).toBe(-40)
  })

  it('holds the bottom edge of a late row with a measured height that the reader did not see', () => {
    const after = withHeight(rows(), 'r3', 1000)

    const result = shiftFor({
      previous: rows(),
      next: after,
      c: 230,
      sawHeight: false,
      enteredFromAbove: ['r3'],
    })

    expect(result.shift).toBe(-940)
    expect(result.sawHeight).toHaveBeenCalledWith('r3')
  })

  it('holds the viewport top for a row that changes a height the reader saw', () => {
    const after = withHeight(rows(), 'r3', 1000)

    const result = shiftFor({
      previous: rows(),
      next: after,
      c: 230,
      sawHeight: true,
      enteredFromAbove: ['r3'],
    })

    expect(result.shift).toBe(-40)
    expect(result.sawHeight).toHaveBeenCalledWith('r3')
  })

  it('holds the bottom edge of a late row that shrinks', () => {
    // r3 is [300, 600] before: 70px of the view above it, and 130px below it.
    const before = withHeight(rows(), 'r3', 300)
    const after = withHeight(rows(), 'r3', 100)

    const result = shiftFor({
      previous: before,
      next: after,
      c: 230,
      sawHeight: false,
      enteredFromAbove: ['r3'],
    })

    // The bottom edge of r3 moves from 600 to 400 in the content.
    expect(result.shift).toBe(160)
  })

  it('holds the bottom edge of a late row at the viewport top', () => {
    // The viewport top is at the top edge of r3, so nothing of the view lies above r3.
    const before = withHeight(rows(), 'r3', 100, false)
    const after = withHeight(rows(), 'r3', 1000)

    expect(shiftFor({
      previous: before,
      next: after,
      c: 300,
      enteredFromAbove: ['r3'],
    }).shift).toBe(-940)
  })

  it('holds the bottom edge of a late row whose top edge lies just below the viewport top', () => {
    const before = withHeight(rows(), 'r3', 100, false)
    const after = withHeight(rows(), 'r3', 1000)

    const result = shiftFor({
      previous: before,
      next: after,
      c: 300 - ROW_ALIGN_OFFSET_PX,
      enteredFromAbove: ['r3'],
    })

    expect(result.shift).toBe(-940)
  })

  it('holds the bottom edge of the late row above the largest block between two late rows', () => {
    // The view is [230, 730]: 70px above r3, 200px between r3 and r6, and 30px below r6.
    const before = withHeight(withHeight(rows(), 'r3', 100, false), 'r6', 100, false)
    const after = withHeight(withHeight(rows(), 'r3', 1000), 'r6', 600)

    const result = shiftFor({
      previous: before,
      next: after,
      c: 230,
      enteredFromAbove: ['r3', 'r6'],
    })

    // r3 grows above itself, and r6 grows at its bottom.
    expect(result.shift).toBe(-940)
  })

  it('holds the bottom edge of the lower one of two adjacent late rows', () => {
    // 70px above r3, nothing between r3 and r4, and 230px below r4.
    const before = withHeight(withHeight(rows(), 'r3', 100, false), 'r4', 100, false)
    const after = withHeight(withHeight(rows(), 'r3', 1000), 'r4', 600)

    const result = shiftFor({
      previous: before,
      next: after,
      c: 230,
      enteredFromAbove: ['r3', 'r4'],
    })

    // The bottom edge of r4 moves from 500 to 1900 in the content.
    expect(result.shift).toBe(-1440)
  })

  it('does not count a row that did not come over the top edge as a late row', () => {
    // r3 is late. r6 corrects a guess too, but the reader did not reach it from below, so it
    // divides no block: 70px lie above r3, and 330px below it.
    const before = withHeight(withHeight(rows(), 'r3', 100, false), 'r6', 100, false)
    const after = withHeight(withHeight(rows(), 'r3', 1000), 'r6', 600)

    expect(shiftFor({
      previous: before,
      next: after,
      c: 230,
      enteredFromAbove: ['r3'],
    }).shift).toBe(-940)
  })

  it('holds the bottom edge of the row at the viewport top when the block below it is the largest', () => {
    // The viewport top is inside r2, whose height is an estimate. r5 is late: 270px of the
    // view lie above it, and 130px below it.
    const before = withHeight(withHeight(rows(), 'r2', 100, false), 'r5', 100, false)
    const after = withHeight(withHeight(rows(), 'r2', 300), 'r5', 1000)

    const result = shiftFor({
      previous: before,
      next: after,
      c: 230,
      enteredFromAbove: ['r5'],
    })

    // The bottom edge of r2 moves from 300 to 500 in the content.
    expect(result.shift).toBe(-240)
  })

  it('holds the bottom edge of a late row below a row at the viewport top that changes too', () => {
    // The viewport top is inside r2, whose height is an estimate. r3 is late: 70px of the
    // view lie above it, and 330px below it.
    const before = withHeight(withHeight(rows(), 'r2', 100, false), 'r3', 100, false)
    const after = withHeight(withHeight(rows(), 'r2', 300), 'r3', 1000)

    const result = shiftFor({
      previous: before,
      next: after,
      c: 230,
      enteredFromAbove: ['r3'],
    })

    // The bottom edge of r3 moves from 400 to 1500 in the content.
    expect(result.shift).toBe(-1140)
  })

  it('holds the viewport top for a late row whose top edge is at the bottom of the view', () => {
    // The view is [230, 700], and r7 starts at 700.
    const before = withHeight(rows(), 'r7', 100, false)
    const after = withHeight(rows(), 'r7', 1000)

    const result = shiftFor({
      previous: before,
      next: after,
      c: 230,
      viewHeight: 470,
      enteredFromAbove: ['r7'],
    })

    expect(result.shift).toBe(-40)
    expect(result.enteredFromAbove).not.toHaveBeenCalled()
  })

  it('holds the viewport top for a late row that the bottom of the view cuts', () => {
    // r7 is [700, 800], and the view ends at 730: nothing of the view lies below r7.
    const before = withHeight(rows(), 'r7', 100, false)
    const after = withHeight(rows(), 'r7', 1000)

    expect(shiftFor({
      previous: before,
      next: after,
      c: 230,
      enteredFromAbove: ['r7'],
    }).shift).toBe(-40)
  })

  it('holds the viewport top for a late row above the view', () => {
    // The viewport top is inside r4, so r3 lies above the view: its correction moves no row
    // of the view either way, and the rule for the row at the viewport top applies.
    const before = withHeight(rows(), 'r3', 100, false)
    const after = withHeight(rows(), 'r3', 1000)

    const result = shiftFor({
      previous: before,
      next: after,
      c: 430,
      enteredFromAbove: ['r3'],
    })

    // The offset inside r4 holds: r4 moves from 400 to 1300 in the content.
    expect(result.shift).toBe(-940)
    expect(result.enteredFromAbove).not.toHaveBeenCalled()
  })

  it('holds the viewport top in a view with no height', () => {
    const before = withHeight(rows(), 'r3', 100, false)
    const after = withHeight(rows(), 'r3', 1000)

    expect(shiftFor({
      previous: before,
      next: after,
      c: 230,
      viewHeight: 0,
      enteredFromAbove: ['r3'],
    }).shift).toBe(-40)
  })

  it('asks about no row that keeps its height', () => {
    const result = shiftFor({
      previous: rows(),
      next: rows(),
      c: 230,
      sawHeight: false,
      enteredFromAbove: ['r3', 'r4'],
    })

    expect(result).toMatchObject({ shift: -40, sawCalls: 0 })
    expect(result.enteredFromAbove).not.toHaveBeenCalled()
  })

  it('does not ask whether the reader saw an unmeasured late row', () => {
    const before = withHeight(rows(), 'r3', 100, false)
    const after = withHeight(rows(), 'r3', 1000)

    expect(shiftFor({
      previous: before,
      next: after,
      c: 230,
      enteredFromAbove: ['r3'],
    }).sawCalls).toBe(0)
  })

  it('holds the viewport top for a late row that left the list', () => {
    const before = withHeight(rows(), 'r3', 100, false)
    const after = rows().filter(row => row.id !== 'r3')

    // r2, the row at the viewport top, keeps its place.
    expect(shiftFor({
      previous: before,
      next: after,
      c: 230,
      enteredFromAbove: ['r3'],
    }).shift).toBe(-40)
  })

  it('keeps the content bottom while pinned, also with a late row in the view', () => {
    const before = withHeight(rows(), 'r3', 100, false)
    const after = withHeight(rows(), 'r3', 1000)

    const result = shiftFor({
      previous: before,
      next: after,
      c: 230,
      pinBottom: true,
      enteredFromAbove: ['r3'],
    })

    // The content grows by 900, and its bottom stays.
    expect(result.shift).toBe(-940)
    expect(result.enteredFromAbove).not.toHaveBeenCalled()
  })

  it('keeps the spacer height at 0 or more when it holds the bottom edge of a late row', () => {
    // A shift below the negative total would give a spacer below 0.
    const before = withHeight(rows(3), 'r1', 100, false)
    const after = withHeight(rows(3), 'r1', 1000)

    const result = shiftFor({
      previous: before,
      next: after,
      c: 50,
      p: -500,
      enteredFromAbove: ['r1'],
    })

    // The bottom edge of r1 would need a shift of -1400, and the content is 1200 tall.
    expect(result.shift).toBe(-1200)
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
