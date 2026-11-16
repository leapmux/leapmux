import type { AdmissionTarget, AdmissionViewAnchor, AdmittedRangeInput } from './chatRowAdmission'
import type { VirtualItem } from './useChatVirtualizer'
import { batch, createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createRowAdmission, nextAdmittedRange, rowsWantedForTarget, TARGET_STAGING_ROWS } from './chatRowAdmission'
import { createRowHeightCache } from './chatRowHeightCache'

/** The row at index `i` of a list: its id is `r<i>`, and its height key is `key`. */
function row(i: number, key = 'a'): VirtualItem {
  return {
    id: `r${i}`,
    hasSpanLines: false,
    heightKey: key,
  }
}

/** `count` rows, from index `first` on. */
function rowsFrom(first: number, count: number, key = 'a'): VirtualItem[] {
  return Array.from({ length: count }, (_, i) => row(first + i, key))
}

/** The indexes `from` to `to`, both included. */
function span(from: number, to: number): number[] {
  return Array.from({ length: to - from + 1 }, (_, i) => from + i)
}

const tail: AdmissionTarget = { kind: 'tail' }
const top: AdmissionTarget = { kind: 'top' }
const rowTarget = (i: number, offset = 0): AdmissionTarget => ({ kind: 'row', id: `r${i}`, offset })

describe('nextAdmittedRange', () => {
  /**
   * The input of the rule for `rows` (20 rows of 100px by default), with a view of 500px.
   * Only the rows at the indexes of `measured` are measured: with a previous range and no
   * list, exactly the rows of that range. `previous` gives a range `a to b` that holds the
   * rows `a` to `b - 1`. No row is mounted, and no view anchor is set, unless stated.
   */
  function input(opts: {
    rows?: VirtualItem[]
    measured?: readonly number[]
    heights?: Record<number, number>
    previous?: [start: number, end: number]
    previousIds?: { firstId: string, lastId: string }
    viewAnchor?: AdmissionViewAnchor
    mounted?: readonly number[]
    target?: AdmissionTarget
    viewHeight?: number
  } = {}): AdmittedRangeInput {
    const rows = opts.rows ?? rowsFrom(0, 20)
    const measuredIndexes = opts.measured ?? (opts.previous
      ? span(opts.previous[0], opts.previous[1] - 1)
      : [])
    const measured = new Set(measuredIndexes.map(i => `r${i}`))
    const mounted = new Set((opts.mounted ?? []).map(i => `r${i}`))
    return {
      rows,
      isMeasured: item => measured.has(item.id),
      heightOf: (item) => {
        // The rule may ask for the height of a measured row only.
        if (!measured.has(item.id))
          throw new Error(`the rule asked for the height of ${item.id}, which is not measured`)
        return opts.heights?.[Number(item.id.slice(1))] ?? 100
      },
      previous: opts.previousIds ?? (opts.previous
        ? { firstId: `r${opts.previous[0]}`, lastId: `r${opts.previous[1] - 1}` }
        : undefined),
      viewAnchor: opts.viewAnchor,
      isMounted: id => mounted.has(id),
      target: opts.target,
      viewHeight: opts.viewHeight ?? 500,
    }
  }

  it('gives an empty range for no loaded row, with the tail and the top reached and a row gone', () => {
    expect(nextAdmittedRange(input({ rows: [] }))).toEqual({ start: 0, end: 0, targetState: 'none' })
    expect(nextAdmittedRange(input({ rows: [], target: tail }))).toEqual({ start: 0, end: 0, targetState: 'reached' })
    expect(nextAdmittedRange(input({ rows: [], target: top }))).toEqual({ start: 0, end: 0, targetState: 'reached' })
    expect(nextAdmittedRange(input({
      rows: [],
      target: rowTarget(3),
    }))).toEqual({ start: 0, end: 0, targetState: 'gone' })
  })

  it('waits for the first view while nothing is measured', () => {
    expect(nextAdmittedRange(input({ target: tail }))).toEqual({ start: 0, end: 0, targetState: 'waiting' })
  })

  it('waits for the first view while the measured rows at the tail do not fill the view', () => {
    // One row of 100px in a view of 500px.
    expect(nextAdmittedRange(input({
      target: tail,
      measured: [19],
    }))).toEqual({ start: 0, end: 0, targetState: 'waiting' })
  })

  it('reaches the first view when the last rows fill the view', () => {
    expect(nextAdmittedRange(input({
      target: tail,
      measured: span(15, 19),
    }))).toEqual({ start: 15, end: 20, targetState: 'reached' })
  })

  it('reaches the first view of a short chat whose rows are all measured', () => {
    const rows = rowsFrom(0, 3)
    expect(nextAdmittedRange(input({
      rows,
      target: tail,
      measured: [0, 1, 2],
    }))).toEqual({ start: 0, end: 3, targetState: 'reached' })
  })

  it('waits for the first view while a row inside it is not measured', () => {
    // The run of row 19 is the rows 18 and 19: row 17 is the hole.
    expect(nextAdmittedRange(input({
      target: tail,
      measured: [14, 15, 16, 18, 19],
    }))).toEqual({ start: 0, end: 0, targetState: 'waiting' })
  })

  it('reaches the first view with one last row that is taller than the view', () => {
    expect(nextAdmittedRange(input({
      target: tail,
      measured: [19],
      heights: { 19: 600 },
    }))).toEqual({ start: 19, end: 20, targetState: 'reached' })
  })

  it('reaches no target in a view with no height', () => {
    const measured = span(15, 19)
    expect(nextAdmittedRange(input({ target: tail, measured, viewHeight: 0 }))).toEqual({ start: 0, end: 0, targetState: 'waiting' })
    expect(nextAdmittedRange(input({ target: tail, measured, viewHeight: 500 }))).toEqual({ start: 15, end: 20, targetState: 'reached' })
  })

  it('grows at both ends as the next rows measure', () => {
    expect(nextAdmittedRange(input({
      previous: [10, 15],
      measured: span(8, 16),
    }))).toEqual({ start: 8, end: 17, targetState: 'none' })
  })

  it('does not grow across a row that is not measured', () => {
    expect(nextAdmittedRange(input({
      previous: [10, 15],
      measured: [
        8,
        ...span(10, 14),
      ],
    }))).toEqual({ start: 10, end: 15, targetState: 'none' })
  })

  it('keeps the part that holds the row of the view anchor when a row inside the range loses its height', () => {
    const measured = span(5, 14).filter(i => i !== 9)
    expect(nextAdmittedRange(input({
      previous: [5, 15],
      measured,
      viewAnchor: { kind: 'row', id: 'r12' },
    }))).toEqual({ start: 10, end: 15, targetState: 'none' })
  })

  it('keeps the part at the tail for a reader who follows it', () => {
    const measured = span(5, 14).filter(i => i !== 9)
    expect(nextAdmittedRange(input({
      previous: [5, 15],
      measured,
      viewAnchor: { kind: 'tail' },
    }))).toEqual({ start: 10, end: 15, targetState: 'none' })
  })

  it('keeps the part of the first mounted row when the row of the view anchor is not measured', () => {
    const measured = span(5, 14).filter(i => i !== 9 && i !== 12)
    expect(nextAdmittedRange(input({
      previous: [5, 15],
      measured,
      viewAnchor: { kind: 'row', id: 'r12' },
      mounted: [13],
    }))).toEqual({ start: 13, end: 15, targetState: 'none' })
  })

  it('keeps the part of a mounted row when the reader has no view anchor', () => {
    const measured = span(5, 14).filter(i => i !== 9)
    expect(nextAdmittedRange(input({
      previous: [5, 15],
      measured,
      mounted: [7],
    }))).toEqual({ start: 5, end: 9, targetState: 'none' })
  })

  it('keeps the part of the first measured row with no view anchor and no mounted row', () => {
    const measured = span(5, 14).filter(i => i !== 9)
    expect(nextAdmittedRange(input({
      previous: [5, 15],
      measured,
    }))).toEqual({ start: 5, end: 9, targetState: 'none' })
  })

  it('keeps the part of a mounted row for a follower whose last row lost its height', () => {
    const measured = span(5, 13)
    expect(nextAdmittedRange(input({
      previous: [5, 15],
      measured,
      viewAnchor: { kind: 'tail' },
      mounted: [11],
    }))).toEqual({ start: 5, end: 14, targetState: 'none' })
  })

  it('gives an empty range when every row of the range lost its height', () => {
    expect(nextAdmittedRange(input({
      previous: [5, 15],
      measured: [],
    }))).toEqual({ start: 0, end: 0, targetState: 'none' })
  })

  it('continues from the last edge row when the first edge row left the list', () => {
    // The rows are the old rows 3 to 19, so the old row 9 is at index 6.
    const rows = rowsFrom(3, 17)
    const result = nextAdmittedRange(input({
      rows,
      previousIds: { firstId: 'r0', lastId: 'r9' },
      measured: [3, 4, 5, 7, 8, 9],
      viewAnchor: { kind: 'row', id: 'r8' },
    }))
    // The old rows 7 to 9.
    expect(result).toEqual({ start: 4, end: 7, targetState: 'none' })
  })

  it('continues from the first edge row when the last edge row left the list', () => {
    const rows = rowsFrom(0, 15)
    const result = nextAdmittedRange(input({
      rows,
      previousIds: { firstId: 'r5', lastId: 'r19' },
      measured: span(5, 14).filter(i => i !== 9),
      mounted: [12],
    }))
    expect(result).toEqual({ start: 10, end: 15, targetState: 'none' })
  })

  it('forms the range of the target when both edge rows left the list', () => {
    const result = nextAdmittedRange(input({
      previousIds: { firstId: 'gone-first', lastId: 'gone-last' },
      measured: span(15, 19),
      target: tail,
    }))
    expect(result).toEqual({ start: 15, end: 20, targetState: 'reached' })
  })

  it('gives an empty range when both edge rows left the list and no target is pending', () => {
    const result = nextAdmittedRange(input({
      previousIds: { firstId: 'gone-first', lastId: 'gone-last' },
      measured: span(15, 19),
    }))
    expect(result).toEqual({ start: 0, end: 0, targetState: 'none' })
  })

  it('changes to the rows of a row target that is measured and filled', () => {
    const measured = [
      ...span(2, 8),
      ...span(15, 19),
    ]
    expect(nextAdmittedRange(input({
      previous: [15, 20],
      measured,
      target: rowTarget(3),
    }))).toEqual({ start: 2, end: 9, targetState: 'reached' })
  })

  it('keeps the range while a measured row target is not filled', () => {
    const measured = [
      3,
      4,
      ...span(15, 19),
    ]
    expect(nextAdmittedRange(input({
      previous: [15, 20],
      measured,
      target: rowTarget(3),
    }))).toEqual({ start: 15, end: 20, targetState: 'waiting' })
  })

  it('keeps the range while the row of a row target is not measured', () => {
    expect(nextAdmittedRange(input({
      previous: [15, 20],
      target: rowTarget(3),
    }))).toEqual({ start: 15, end: 20, targetState: 'waiting' })
  })

  it('fills a row target near the end with the rows above it', () => {
    // From row 18 down, 200px. The whole run must then fill the view.
    expect(nextAdmittedRange(input({
      measured: span(16, 19),
      target: rowTarget(18),
    }))).toEqual({ start: 0, end: 0, targetState: 'waiting' })
    expect(nextAdmittedRange(input({
      measured: span(15, 19),
      target: rowTarget(18),
    }))).toEqual({ start: 15, end: 20, targetState: 'reached' })
  })

  it('reaches a row target whose run holds every loaded row', () => {
    const rows = rowsFrom(0, 3)
    expect(nextAdmittedRange(input({
      rows,
      measured: [0, 1, 2],
      target: rowTarget(1),
    }))).toEqual({ start: 0, end: 3, targetState: 'reached' })
  })

  it('counts the offset of a row target', () => {
    // 250px into row 3, the view needs 750px from the top edge of the row.
    expect(nextAdmittedRange(input({
      measured: span(3, 9),
      target: rowTarget(3, 250),
    }))).toMatchObject({ targetState: 'waiting' })
    expect(nextAdmittedRange(input({
      measured: span(3, 10),
      target: rowTarget(3, 250),
    }))).toEqual({ start: 3, end: 11, targetState: 'reached' })
  })

  it('reaches a row target inside the previous range at once', () => {
    expect(nextAdmittedRange(input({
      previous: [0, 20],
      target: rowTarget(5),
    }))).toEqual({ start: 0, end: 20, targetState: 'reached' })
  })

  it('reports a row target whose row left the list as gone, and keeps the range', () => {
    expect(nextAdmittedRange(input({
      previous: [15, 20],
      target: { kind: 'row', id: 'left', offset: 0 },
    }))).toEqual({ start: 15, end: 20, targetState: 'gone' })
  })

  it('reaches the top target when the first rows fill the view', () => {
    expect(nextAdmittedRange(input({
      measured: span(0, 4),
      target: top,
    }))).toEqual({ start: 0, end: 5, targetState: 'reached' })
    expect(nextAdmittedRange(input({
      measured: span(0, 3),
      target: top,
    }))).toEqual({ start: 0, end: 0, targetState: 'waiting' })
  })

  it('reaches the top target of a short chat whose rows are all measured', () => {
    const rows = rowsFrom(0, 3)
    expect(nextAdmittedRange(input({
      rows,
      measured: [0, 1, 2],
      target: top,
    }))).toEqual({ start: 0, end: 3, targetState: 'reached' })
  })

  it('changes to the rows of the tail from a previous range elsewhere', () => {
    const measured = [
      ...span(2, 7),
      ...span(15, 19),
    ]
    expect(nextAdmittedRange(input({
      previous: [2, 8],
      measured,
      target: tail,
    }))).toEqual({ start: 15, end: 20, targetState: 'reached' })
  })

  it('reaches the tail target that the previous range already holds', () => {
    expect(nextAdmittedRange(input({
      previous: [10, 20],
      target: tail,
    }))).toEqual({ start: 10, end: 20, targetState: 'reached' })
  })

  it('gives the same range when it runs again on its own result', () => {
    // The admission carries the range from one run to the next, so the rule must be stable.
    const base = {
      measured: [
        ...span(2, 7),
        ...span(15, 19),
      ],
      target: tail,
    }
    const first = nextAdmittedRange(input({
      ...base,
      previous: [2, 8],
    }))
    const second = nextAdmittedRange(input({
      ...base,
      previous: [first.start, first.end],
    }))
    expect(second).toEqual(first)
  })
})

describe('rowsWantedForTarget', () => {
  /** The wanted rows for `count` rows of which the rows at `measured` are measured, for a waiting `target`. */
  function wanted(count: number, target: AdmissionTarget | undefined, measured: readonly number[] = [], targetState: 'none' | 'waiting' | 'reached' | 'gone' = 'waiting'): number[] {
    const measuredIds = new Set(measured.map(i => `r${i}`))
    return rowsWantedForTarget({
      rows: rowsFrom(0, count),
      isMeasured: item => measuredIds.has(item.id),
      target,
      targetState,
    })
  }

  it('wants the rows up from the tail', () => {
    expect(wanted(30, tail)).toEqual(span(18, 29).reverse())
  })

  it('skips the measured rows of the scan', () => {
    expect(wanted(30, tail, [27, 28])).toEqual([
      29,
      ...span(16, 26).reverse(),
    ])
  })

  it('wants the rows down from the top', () => {
    expect(wanted(30, top)).toEqual(span(0, 11))
  })

  it('wants the rows down from a row target', () => {
    expect(wanted(30, rowTarget(10))).toEqual(span(10, 21))
  })

  it('wants the rows down and then up from a row target near the end', () => {
    expect(wanted(20, rowTarget(17))).toEqual([
      17,
      18,
      19,
      ...span(8, 16).reverse(),
    ])
  })

  it('scans as far as the first row for an unmeasured row', () => {
    // A limit on the scan would leave this target with no row to measure.
    expect(wanted(100, tail, span(1, 99))).toEqual([0])
  })

  it('never wants more rows than one frame measures', () => {
    expect(wanted(100, tail)).toHaveLength(TARGET_STAGING_ROWS)
  })

  it('wants no row for a reached target, a gone target, and no target', () => {
    expect(wanted(30, tail, [], 'reached')).toEqual([])
    expect(wanted(30, { kind: 'row', id: 'left', offset: 0 }, [], 'gone')).toEqual([])
    expect(wanted(30, undefined, [], 'none')).toEqual([])
  })

  it('wants no row for a row target whose row is not loaded', () => {
    expect(wanted(30, { kind: 'row', id: 'left', offset: 0 })).toEqual([])
  })
})

describe('createRowAdmission', () => {
  /**
   * An admission over `count` rows (20 by default) of which none is measured, with a real
   * height cache and a view of 500px. `measure` commits 100px for each row at an index,
   * and `setKeys` gives every row a new height key, as a width change does.
   */
  function mount(opts: { count?: number, tailLoaded?: boolean } = {}) {
    const [rows, setRows] = createSignal<VirtualItem[]>(rowsFrom(0, opts.count ?? 20))
    const [viewHeight, setViewHeight] = createSignal(500)
    const [tailLoaded, setTailLoaded] = createSignal(opts.tailLoaded ?? true)
    const heights = createRowHeightCache()
    let viewAnchor: AdmissionViewAnchor | undefined
    let viewAnchorReads = 0
    let dispose!: () => void
    const admission = createRoot((disposeRoot) => {
      dispose = disposeRoot
      return createRowAdmission({
        rows,
        heights,
        viewHeight,
        viewAnchor: () => {
          viewAnchorReads++
          return viewAnchor
        },
        tailLoaded,
      })
    })
    const measure = (indexes: readonly number[], height = 100): void => {
      const list = rows()
      batch(() => {
        for (const i of indexes) {
          const item = list[i]!
          heights.commit(item, height, item.heightKey, 'premeasure')
        }
      })
    }
    return {
      admission,
      heights,
      rows,
      setRows,
      setViewHeight,
      setTailLoaded,
      measure,
      setKeys: (key: string) => setRows(rows().map(item => ({ ...item, heightKey: key }))),
      setViewAnchor: (anchor: AdmissionViewAnchor | undefined) => {
        viewAnchor = anchor
      },
      viewAnchorReads: () => viewAnchorReads,
      dispose,
    }
  }

  /** An admission whose 20 rows are all measured and admitted through a tail request. */
  function mountAtTail() {
    const h = mount()
    h.measure(span(0, 19))
    const request = h.admission.request(tail)
    return { ...h, request }
  }

  it('admits the first view when its rows are measured', () => {
    const h = mount()
    h.admission.request(tail)
    expect(h.admission.range()).toEqual({ start: 0, end: 0 })
    expect(h.admission.awaitingFirstView()).toBe(true)
    expect(h.admission.stagedAbove()).toBe(20)
    expect(h.admission.stagedBelow()).toBe(0)

    h.measure(span(15, 19))

    expect(h.admission.range()).toEqual({ start: 15, end: 20 })
    expect(h.admission.awaitingFirstView()).toBe(false)
    expect(h.admission.stagedAbove()).toBe(15)
    expect(h.admission.stagedBelow()).toBe(0)
    h.dispose()
  })

  it('turns the state of a request to none when another request replaces it', () => {
    const h = mount()
    const first = h.admission.request(tail)
    expect(first.state()).toBe('waiting')

    const second = h.admission.request(top)

    expect(first.state()).toBe('none')
    expect(second.state()).toBe('waiting')
    h.dispose()
  })

  it('changes nothing when a replaced request ends', () => {
    const h = mount()
    const first = h.admission.request(tail)
    const second = h.admission.request(top)
    const wantedBefore = h.admission.wantedRows()

    first.end()

    expect(second.state()).toBe('waiting')
    expect(h.admission.wantedRows()).toEqual(wantedBefore)
    expect(h.admission.wantedRows()).toEqual(span(0, 11))
    h.dispose()
  })

  it('reports that the admitted rows hold the tail when their last row is the last loaded row', () => {
    const h = mountAtTail()
    expect(h.admission.range()).toEqual({ start: 0, end: 20 })
    expect(h.admission.holdsTail()).toBe(true)
    h.dispose()
  })

  it('keeps holding the tail while a new message is staged', () => {
    const h = mountAtTail()

    h.setRows([
      ...h.rows(),
      row(20),
    ])
    expect(h.admission.stagedBelow()).toBe(1)
    expect(h.admission.holdsTail()).toBe(true)

    h.measure([20])
    expect(h.admission.stagedBelow()).toBe(0)
    expect(h.admission.holdsTail()).toBe(true)
    h.dispose()
  })

  it('keeps holding the tail while a burst of new messages measures row by row', () => {
    const h = mountAtTail()

    h.setRows([
      ...h.rows(),
      row(20),
      row(21),
    ])
    h.measure([20])

    expect(h.admission.range()).toEqual({ start: 0, end: 21 })
    expect(h.admission.stagedBelow()).toBe(1)
    expect(h.admission.holdsTail()).toBe(true)
    h.dispose()
  })

  it('does not hold the tail while rows that were admitted before are staged below', () => {
    const h = mountAtTail()
    for (const i of span(8, 12))
      h.heights.mountedIds.add(`r${i}`)

    // A width change: only the mounted rows keep a height.
    h.setKeys('b')
    expect(h.admission.range()).toEqual({ start: 8, end: 13 })
    expect(h.admission.holdsTail()).toBe(false)

    h.measure(span(13, 19))
    expect(h.admission.range()).toEqual({ start: 8, end: 20 })
    expect(h.admission.holdsTail()).toBe(true)
    h.dispose()
  })

  it('does not hold the tail while a new message that was admitted before is staged below', () => {
    const h = mountAtTail()
    h.setRows([
      ...h.rows(),
      row(20),
      row(21),
    ])
    h.measure([20])
    expect(h.admission.holdsTail()).toBe(true)
    for (const i of span(8, 12))
      h.heights.mountedIds.add(`r${i}`)

    h.setKeys('b')
    h.measure(span(13, 19))

    // Row 20 was admitted before, so it is no new message below the admitted rows.
    expect(h.admission.range()).toEqual({ start: 8, end: 20 })
    expect(h.admission.holdsTail()).toBe(false)
    h.dispose()
  })

  it('does not count the rows of a newer page as new messages', () => {
    const h = mount({ tailLoaded: false })
    h.measure(span(0, 19))
    h.admission.request(tail)
    expect(h.admission.range()).toEqual({ start: 0, end: 20 })

    // A newer history page arrives. The store clears its newer-pages flag afterwards.
    h.setRows([
      ...h.rows(),
      ...rowsFrom(20, 5),
    ])
    h.setTailLoaded(true)
    expect(h.admission.stagedBelow()).toBe(5)
    expect(h.admission.holdsTail()).toBe(false)

    h.measure(span(20, 24))
    expect(h.admission.holdsTail()).toBe(true)
    h.dispose()
  })

  it('forgets the kept row when the loaded window stops holding the live tail', () => {
    const h = mountAtTail()
    expect(h.admission.holdsTail()).toBe(true)

    // The window is paged away, and rows of a newer page join below the admitted rows.
    batch(() => {
      h.setTailLoaded(false)
      h.setRows([
        ...h.rows(),
        row(20),
      ])
    })
    expect(h.admission.holdsTail()).toBe(false)

    // The tail is loaded again, but the row below was no new message.
    h.setTailLoaded(true)
    expect(h.admission.holdsTail()).toBe(false)
    h.dispose()
  })

  it('counts a kept row that left the loaded rows for nothing', () => {
    const h = mountAtTail()
    h.setRows([
      ...h.rows(),
      row(20),
    ])
    expect(h.admission.holdsTail()).toBe(true)

    // Every row is replaced. The rows 5 to 9 of the new window are measured.
    const replaced = Array.from({ length: 20 }, (_, i): VirtualItem => ({ id: `n${i}`, hasSpanLines: false, heightKey: 'a' }))
    batch(() => {
      h.setRows(replaced)
      for (const item of replaced.slice(5, 10))
        h.heights.commit(item, 100, item.heightKey, 'premeasure')
      h.admission.request({ kind: 'row', id: 'n5', offset: 0 })
    })

    expect(h.admission.range()).toEqual({ start: 5, end: 10 })
    expect(h.admission.holdsTail()).toBe(false)
    h.dispose()
  })

  it('does not hold the tail with no admitted row', () => {
    const h = mount()
    h.admission.request(tail)
    expect(h.admission.holdsTail()).toBe(false)
    h.dispose()
  })

  it('keeps the range when a reached request ends', () => {
    const h = mountAtTail()
    const request = h.admission.request(rowTarget(5))
    expect(request.state()).toBe('reached')

    request.end()

    expect(request.state()).toBe('none')
    expect(h.admission.range()).toEqual({ start: 0, end: 20 })
    h.dispose()
  })

  it('wants no row after a waiting request ends', () => {
    const h = mount()
    h.measure(span(15, 19))
    h.admission.request(tail).end()
    expect(h.admission.range()).toEqual({ start: 15, end: 20 })
    const request = h.admission.request(rowTarget(3))
    expect(request.state()).toBe('waiting')
    expect(h.admission.wantedRows()).toEqual(span(3, 14))

    request.end()

    expect(h.admission.wantedRows()).toEqual([])
    expect(h.admission.range()).toEqual({ start: 15, end: 20 })
    h.dispose()
  })

  it('shrinks to the mounted rows when every height key changes', () => {
    const h = mountAtTail()
    for (const i of span(8, 12))
      h.heights.mountedIds.add(`r${i}`)

    h.setKeys('b')

    expect(h.admission.range()).toEqual({ start: 8, end: 13 })
    expect(h.admission.stagedAbove()).toBe(8)
    expect(h.admission.stagedBelow()).toBe(7)
    h.dispose()
  })

  it('drops a mounted row whose provisional height ends at its unmount', () => {
    const h = mountAtTail()
    for (const i of span(8, 12))
      h.heights.mountedIds.add(`r${i}`)
    h.setKeys('b')
    expect(h.admission.range()).toEqual({ start: 8, end: 13 })

    h.heights.mountedIds.delete('r8')
    h.heights.noteUnmounted(h.rows()[8]!)

    expect(h.admission.range()).toEqual({ start: 9, end: 13 })
    h.dispose()
  })

  it('admits nothing with no request', () => {
    const h = mount()
    h.measure(span(15, 19))

    expect(h.admission.range()).toEqual({ start: 0, end: 0 })
    expect(h.admission.awaitingFirstView()).toBe(true)
    expect(h.admission.wantedRows()).toEqual([])
    h.dispose()
  })

  it('waits for a new first view when every loaded row is replaced', () => {
    const h = mount()
    h.measure(span(15, 19))
    h.admission.request(tail).end()
    expect(h.admission.range()).toEqual({ start: 15, end: 20 })

    const replaced = Array.from({ length: 20 }, (_, i): VirtualItem => ({ id: `n${i}`, hasSpanLines: false, heightKey: 'a' }))
    h.setRows(replaced)
    expect(h.admission.range()).toEqual({ start: 0, end: 0 })
    expect(h.admission.awaitingFirstView()).toBe(true)

    h.admission.request(tail)
    batch(() => {
      for (const item of replaced.slice(15))
        h.heights.commit(item, 100, item.heightKey, 'premeasure')
    })
    expect(h.admission.range()).toEqual({ start: 15, end: 20 })
    expect(h.admission.awaitingFirstView()).toBe(false)
    h.dispose()
  })

  it('reads the view anchor at the run that needs it', () => {
    const h = mount()
    h.measure(span(5, 14))
    h.admission.request(rowTarget(5)).end()
    expect(h.admission.range()).toEqual({ start: 5, end: 15 })
    h.setViewAnchor({ kind: 'row', id: 'r12' })
    const readsBefore = h.viewAnchorReads()

    // Row 9 loses its height: its key changes, and it is not mounted.
    h.setRows(h.rows().map((item, i) => (i === 9
      ? { ...item, heightKey: 'b' }
      : item)))

    expect(h.admission.range()).toEqual({ start: 10, end: 15 })
    expect(h.viewAnchorReads()).toBeGreaterThan(readsBefore)
    h.dispose()
  })

  it('reaches no target while the view has no height, and reaches it when the view has one', () => {
    const h = mount()
    h.setViewHeight(0)
    h.measure(span(15, 19))
    const request = h.admission.request(tail)
    expect(request.state()).toBe('waiting')
    expect(h.admission.range()).toEqual({ start: 0, end: 0 })

    h.setViewHeight(500)

    expect(request.state()).toBe('reached')
    expect(h.admission.range()).toEqual({ start: 15, end: 20 })
    h.dispose()
  })

  it('reports no staged row and no first view for a list with no row', () => {
    const h = mount({ count: 0 })
    const request = h.admission.request(tail)

    expect(request.state()).toBe('reached')
    expect(h.admission.awaitingFirstView()).toBe(false)
    expect(h.admission.stagedAbove()).toBe(0)
    expect(h.admission.stagedBelow()).toBe(0)
    expect(h.admission.holdsTail()).toBe(false)
    h.dispose()
  })
})
