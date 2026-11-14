import type { ContentMovedInfo, LayoutPivotDeps, PivotRow } from './chatLayoutPivot'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createLayoutPivot, selectPivotElement } from './chatLayoutPivot'

/** Give `el` a client rectangle from `top` to `bottom`, and count the reads in `reads`. */
function place(el: Element, top: number, bottom: number, reads?: { count: number }): void {
  el.getBoundingClientRect = () => {
    if (reads)
      reads.count++
    return new DOMRect(0, top, 400, bottom - top)
  }
}

/** A `div` with a client rectangle and the given children. */
function box(top: number, bottom: number, children: Element[] = [], reads?: { count: number }): HTMLDivElement {
  const el = document.createElement('div')
  place(el, top, bottom, reads)
  el.append(...children)
  return el
}

describe('selectPivotElement', () => {
  it('returns the first child that starts at the line', () => {
    const second = box(100, 200)
    const row = box(0, 300, [
      box(0, 100),
      second,
      box(200, 300),
    ])
    expect(selectPivotElement(row, 100, 600)).toBe(second)
  })

  it('returns the next sibling when the line cuts the first match', () => {
    const second = box(150, 250)
    const row = box(0, 250, [
      box(0, 150),
      second,
    ])
    expect(selectPivotElement(row, 100, 600)).toBe(second)
  })

  it('keeps the cut element when its next sibling starts below the viewport', () => {
    const first = box(0, 150)
    const row = box(0, 900, [
      first,
      box(700, 900),
    ])
    expect(selectPivotElement(row, 100, 600)).toBe(first)
  })

  it('descends into a child that the line cuts', () => {
    const target = box(900, 1100)
    const row = box(0, 2000, [
      box(0, 2000, [
        box(0, 900),
        target,
      ]),
    ])
    expect(selectPivotElement(row, 1000, 1500)).toBe(target)
  })

  it('descends through a wrapper with no box', () => {
    // An element with `display: contents` has no box: its rectangle is all zeros.
    const target = box(900, 1100)
    const wrapper = box(0, 0, [
      box(0, 900),
      target,
    ])
    wrapper.getBoundingClientRect = () => new DOMRect(0, 0, 0, 0)
    const row = box(0, 2000, [
      box(0, 2000, [wrapper]),
    ])
    expect(selectPivotElement(row, 1000, 1500)).toBe(target)
  })

  it('takes the next sibling after a wrapper with no box', () => {
    const cut = box(0, 150)
    const next = box(150, 250)
    const wrapper = box(0, 0, [cut])
    wrapper.getBoundingClientRect = () => new DOMRect(0, 0, 0, 0)
    const row = box(0, 250, [wrapper, next])
    expect(selectPivotElement(row, 100, 600)).toBe(next)
  })

  it('skips an empty element with no box', () => {
    const empty = box(0, 0)
    empty.getBoundingClientRect = () => new DOMRect(0, 0, 0, 0)
    const target = box(100, 200)
    const row = box(0, 300, [
      box(0, 100),
      empty,
      target,
    ])
    expect(selectPivotElement(row, 100, 600)).toBe(target)
  })

  it('takes the last of several side-by-side children', () => {
    const target = box(200, 300)
    const row = box(0, 500, [
      box(0, 500),
      box(0, 500, [target]),
    ])
    expect(selectPivotElement(row, 100, 600)).toBe(target)
  })

  it('steps across at most four side-by-side columns', () => {
    const columns = Array.from({ length: 6 }, () => box(0, 500))
    const row = box(0, 500, columns)
    expect(selectPivotElement(row, 100, 600)).toBe(columns[4])
  })

  it('returns the row element when no child ends below the line', () => {
    const row = box(0, 300, [
      box(0, 50),
      box(0, 50),
    ])
    expect(selectPivotElement(row, 100, 600)).toBe(row)
  })

  it('returns the row element when it has no child', () => {
    const row = box(0, 300)
    expect(selectPivotElement(row, 100, 600)).toBe(row)
  })

  it('stops after 8 levels', () => {
    const levels = [
      box(0, 1000),
    ]
    for (let level = 1; level < 10; level++) {
      const child = box(0, 1000)
      levels[level - 1]!.append(child)
      levels.push(child)
    }
    expect(selectPivotElement(levels[0]!, 100, 600)).toBe(levels[8])
  })

  it('finds the child among 100 children with a binary search', () => {
    const reads = { count: 0 }
    const children = Array.from({ length: 100 }, (_, i) => box(10 * i, 10 * i + 10, [], reads))
    const row = box(0, 1000, children)
    // A linear scan finds child 55 (550 to 560), which the line cuts, and returns its next
    // sibling, which starts at 560, inside the viewport.
    expect(selectPivotElement(row, 555, 1000)).toBe(children[56])
    expect(reads.count).toBeLessThan(20)
  })
})

/** A scroll element, one row with one pivot child, and a manual frame queue. */
function pivotHarness() {
  const state = {
    clientHeight: 500,
    scrollTop: 1000,
    /** The scroll-coordinate top of the row. */
    rowTop: 1000,
    /** The client top of the row element. */
    rowClientTop: -280,
    /** The distance from the row top to the pivot child. */
    inRowTop: 300,
    rowRectHasTop: true,
    suspended: false,
    uncommitted: false,
    pivotRowPresent: true,
  }
  const scroll = document.createElement('div')
  Object.defineProperty(scroll, 'clientHeight', {
    configurable: true,
    get: () => state.clientHeight,
  })
  Object.defineProperty(scroll, 'scrollHeight', {
    configurable: true,
    get: () => 10_000,
  })
  Object.defineProperty(scroll, 'scrollTop', {
    configurable: true,
    get: () => state.scrollTop,
  })
  scroll.getBoundingClientRect = () => new DOMRect(0, 0, 400, state.clientHeight)
  const row = document.createElement('div')
  row.getBoundingClientRect = () => (state.rowRectHasTop
    ? new DOMRect(0, state.rowClientTop, 400, 1000)
    : { height: 1000 } as DOMRect)
  const pivotChild = document.createElement('div')
  pivotChild.getBoundingClientRect = () => new DOMRect(0, state.rowClientTop + state.inRowTop, 400, 50)
  row.append(pivotChild)
  scroll.append(row)
  document.body.append(scroll)

  const frames = new Map<number, () => void>()
  let nextHandle = 1
  const requestFrame = vi.fn((cb: () => void) => {
    const handle = nextHandle++
    frames.set(handle, cb)
    return handle
  })
  const cancelFrame = vi.fn((handle: number) => {
    frames.delete(handle)
  })
  const runFrames = () => {
    const pending = [
      ...frames.values(),
    ]
    frames.clear()
    for (const cb of pending)
      cb()
  }
  const onContentMoved = vi.fn((_info: ContentMovedInfo) => {})
  const deps: LayoutPivotDeps = {
    scrollElement: () => scroll,
    pivotRow: (): PivotRow | undefined => (state.pivotRowPresent
      ? { id: 'r1', element: row, top: state.rowTop }
      : undefined),
    hasUncommittedHeight: () => state.uncommitted,
    isSuspended: () => state.suspended,
    requestFrame,
    cancelFrame,
    onContentMoved,
  }
  const layoutPivot = createLayoutPivot(deps)
  /** Make a record of the current layout. */
  const record = () => {
    layoutPivot.scheduleRecord()
    runFrames()
  }
  return {
    state,
    scroll,
    row,
    pivotChild,
    layoutPivot,
    requestFrame,
    cancelFrame,
    runFrames,
    record,
    onContentMoved,
  }
}

describe('createLayoutPivot', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  it('returns 0 before any record', () => {
    const { layoutPivot } = pivotHarness()
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(0)
  })

  it('returns the displacement inside the row once', () => {
    const { state, layoutPivot, record } = pivotHarness()
    record()
    state.inRowTop = 340
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(40)
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(0)
  })

  it('ignores a move of the whole row', () => {
    const { state, layoutPivot, record } = pivotHarness()
    record()
    state.rowClientTop += 200
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(0)
  })

  it('returns 0 for another row and for a detached element', () => {
    const { state, layoutPivot, record, pivotChild, row } = pivotHarness()
    record()
    state.inRowTop = 340
    expect(layoutPivot.takeIntraRowShift('r2')).toBe(0)
    pivotChild.remove()
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(0)
    row.append(pivotChild)
    row.remove()
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(0)
  })

  it('uses a record of an earlier scroll position', () => {
    const { state, layoutPivot, record } = pivotHarness()
    record()
    state.scrollTop = 1300
    state.rowClientTop -= 300
    state.inRowTop = 340
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(40)
  })

  it('keeps the previous record while the row has an uncommitted height change', () => {
    const { state, layoutPivot, record } = pivotHarness()
    record()
    state.uncommitted = true
    state.inRowTop = 340
    record()
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(40)
  })

  it('records again when the measurer has nothing to commit', () => {
    const { state, layoutPivot, record } = pivotHarness()
    record()
    state.uncommitted = true
    state.inRowTop = 340
    record()
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(40)
    state.uncommitted = false
    state.inRowTop = 400
    record()
    // A frame that kept the record would give 60.
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(0)
  })

  it('returns 0 when a rectangle value is not finite', () => {
    const { state, layoutPivot, record } = pivotHarness()
    state.rowRectHasTop = false
    record()
    state.rowRectHasTop = true
    state.inRowTop = 340
    // No record was stored: a stored one would measure the 40px move.
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(0)
  })

  it('makes one record for several scheduleRecord calls', () => {
    const { layoutPivot, requestFrame } = pivotHarness()
    layoutPivot.scheduleRecord()
    layoutPivot.scheduleRecord()
    layoutPivot.scheduleRecord()
    expect(requestFrame).toHaveBeenCalledOnce()
  })

  it('schedules a new frame after the frame ran', () => {
    const { layoutPivot, requestFrame, runFrames } = pivotHarness()
    layoutPivot.scheduleRecord()
    runFrames()
    layoutPivot.scheduleRecord()
    expect(requestFrame).toHaveBeenCalledTimes(2)
  })

  it('records once when the frame runs inside requestFrame', () => {
    const { state, scroll, row } = pivotHarness()
    const layoutPivot = createLayoutPivot({
      scrollElement: () => scroll,
      pivotRow: () => ({ id: 'r1', element: row, top: state.rowTop }),
      hasUncommittedHeight: () => false,
      isSuspended: () => false,
      requestFrame: (cb) => {
        cb()
        return 7
      },
      cancelFrame: () => {},
    })
    layoutPivot.scheduleRecord()
    state.inRowTop = 340
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(40)
    // The synchronous frame left nothing pending, so the next call records again.
    layoutPivot.scheduleRecord()
    state.inRowTop = 360
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(20)
  })

  it.each([
    [
      'suspended during a scroll',
      (state: ReturnType<typeof pivotHarness>['state']) => {
        state.suspended = true
        state.scrollTop += 40
      },
    ],
    [
      'hidden',
      (state: ReturnType<typeof pivotHarness>['state']) => { state.clientHeight = 0 },
    ],
    [
      'without a pivot row',
      (state: ReturnType<typeof pivotHarness>['state']) => { state.pivotRowPresent = false },
    ],
  ])('drops the record while %s', (_name, apply) => {
    const { state, layoutPivot, record } = pivotHarness()
    record()
    apply(state)
    record()
    state.inRowTop = 340
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(0)
  })

  it('records while suspended once the scroll position holds for a frame', () => {
    const { state, layoutPivot, record, runFrames, requestFrame } = pivotHarness()
    state.suspended = true
    state.scrollTop = 1040
    // The frame after the last scroll event sees a new position: no record yet, and one
    // more frame to learn whether the scroller stopped.
    record()
    expect(requestFrame).toHaveBeenCalledTimes(2)
    state.inRowTop = 340
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(0)

    // That frame sees the same position: the scroller stopped, so the frame records
    // although the fast scroll has not ended, and it asks for no further frame.
    state.inRowTop = 300
    runFrames()
    expect(requestFrame).toHaveBeenCalledTimes(2)
    state.inRowTop = 340
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(40)
  })

  it('keeps the record absent while a suspended scroll moves', () => {
    const { state, layoutPivot, record, runFrames, requestFrame } = pivotHarness()
    state.suspended = true
    record()
    state.scrollTop += 40
    runFrames()
    state.scrollTop += 40
    runFrames()
    // Each frame that sees a new position asks for one more frame and reads no rectangle.
    expect(requestFrame).toHaveBeenCalledTimes(4)
    state.inRowTop = 340
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(0)
  })

  it('reports an element that moved on screen without a scroll', () => {
    const { state, record, onContentMoved } = pivotHarness()
    state.rowClientTop = -200
    record()
    state.inRowTop = 303
    record()
    expect(onContentMoved).toHaveBeenCalledOnce()
    expect(onContentMoved).toHaveBeenCalledWith({ rowId: 'r1', movedPx: 3, scrollTop: 1000 })
  })

  it('reports nothing for a move that the layout made', () => {
    const { state, record, onContentMoved } = pivotHarness()
    state.rowClientTop = -200
    record()
    state.rowClientTop += 200
    state.rowTop += 200
    record()
    expect(onContentMoved).not.toHaveBeenCalled()
  })

  it('reports nothing below 2px', () => {
    const { state, record, onContentMoved } = pivotHarness()
    state.rowClientTop = -200
    record()
    state.inRowTop = 301
    record()
    expect(onContentMoved).not.toHaveBeenCalled()
  })

  it('reports nothing after a scroll', () => {
    const { state, record, onContentMoved } = pivotHarness()
    state.rowClientTop = -200
    record()
    state.scrollTop += 5
    state.inRowTop = 303
    record()
    expect(onContentMoved).not.toHaveBeenCalled()
  })

  it('cancels the pending frame on dispose', () => {
    const { layoutPivot, requestFrame, cancelFrame, runFrames, state } = pivotHarness()
    layoutPivot.scheduleRecord()
    const handle = requestFrame.mock.results[0]?.value as number
    layoutPivot.dispose()
    expect(cancelFrame).toHaveBeenCalledWith(handle)
    runFrames()
    state.inRowTop = 340
    expect(layoutPivot.takeIntraRowShift('r1')).toBe(0)
  })
})
