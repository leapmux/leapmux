import type { Accessor } from 'solid-js'
import type { RowHeightCache } from './chatRowHeightCache'
import type { ChatRowItem } from './chatRowItem'
import { batch, createEffect, createMemo, createRenderEffect, createRoot, createSignal, on } from 'solid-js'
import { afterEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { MAX_LOADED_CHAT_MESSAGES_CEILING } from '~/stores/chat.store'
import { createManualFrames } from '~/test-support/manualFrames'
import { BAND_BORDER_PX } from './chatRowGeometry'
import { HEIGHT_CACHE_MAX } from './chatRowHeightCache'
import { warnSlowScrollPhase } from './chatScrollGeometry'
import { MEASURE_EPSILON_PX } from './createRowMeasurer'
import { createRenderReadinessScope } from './renderReadiness'
import { fakeRow, makeItems, noLayoutRow, plainItems, setup, spyOnUnmeasuredRowReports } from './useChatVirtualizer.testkit'

// The virtualizer reports the time of each rebuild of its offset map to
// `warnSlowScrollPhase`, so a spy on it counts the rebuilds.
vi.mock('./chatScrollGeometry', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./chatScrollGeometry')>()
  return {
    ...actual,
    warnSlowScrollPhase: vi.fn(actual.warnSlowScrollPhase),
  }
})

/** The rebuilds of the offset map since the last `mockClear` of the spy. */
function geomRebuilds(): number {
  return vi.mocked(warnSlowScrollPhase).mock.calls.filter(([phase]) => phase === 'geomRebuild').length
}

/**
 * Run `build` in a new root, and return its result with the dispose of the root. The
 * test continues outside the root, so a signal write runs its effects at once.
 */
function buildInRoot<T extends object>(build: () => T): T & { dispose: () => void } {
  let dispose!: () => void
  const built = createRoot((disposeRoot) => {
    dispose = disposeRoot
    return build()
  })
  return { ...built, dispose }
}

describe('useChatVirtualizer geometry', () => {
  it('rebuilds the offset map once for several height commits in one batch', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(5))
      vi.mocked(warnSlowScrollPhase).mockClear()

      // The row measurer commits the reads of one flush in one batch, as here.
      batch(() => {
        virt.measure('m1', 110)
        virt.measure('m2', 120)
        virt.measure('m3', 130)
      })

      // The two other rows keep their 100px.
      expect(virt.totalHeight()).toBe(110 + 120 + 130 + 2 * 100 + 4 * 20)
      expect(geomRebuilds()).toBe(1)
      dispose()
    })
  })

  it('hydrates a cached height of 0, and gives no height when the layout key changes', () => {
    createRoot((dispose) => {
      try {
        const empty = (heightKey: string): ChatRowItem => ({ id: 'empty', heightKey, hasSpanLines: false })
        // The stored height hydrates the cache before the admission adds the row to the list.
        const { virt, setList, cache } = setup([], { measured: false })
        expect(cache.commitAll([
          {
            row: empty('old'),
            height: 0,
          },
        ])).toBe(1)
        setList([
          empty('old'),
        ])
        expect(cache.heightOf(empty('old'))).toBe(0)
        expect(virt.totalHeight()).toBe(0)
        // A read within the epsilon of the height of 0 is noise, as for any other height.
        expect(virt.measure('empty', 0.2)).toBe(false)
        expect(virt.heightOfIndex(0)).toBe(0)
        expect(virt.measure('empty', 0.7)).toBe(true)
        expect(virt.heightOfIndex(0)).toBe(0.7)
        expect(virt.measure('empty', 0)).toBe(true)
        expect(virt.heightOfIndex(0)).toBe(0)
        // The row is not mounted, so it has no height under a new key, and a height of the
        // old key is refused. The admission keeps such a row out of the list.
        expect(cache.heightOf(empty('new'))).toBeUndefined()
        expect(cache.commit(empty('new'), 0, 'old', 'premeasure')).toBe('none')
        expect(virt.measure('missing', 0)).toBe(false)
        expect(cache.heightOf(empty('new'))).toBeUndefined()
      }
      finally {
        dispose()
      }
    })
  })

  it('keeps band offsets nondecreasing for zero and fractional heights', () => {
    createRoot((dispose) => {
      const { virt, cache } = setup(makeItems([
        { seq: 1, kind: 'assistant_text' },
        { seq: 2, kind: 'assistant_thinking' },
        { seq: 3, kind: 'assistant_text' },
      ]))
      virt.measure('m1', 0)
      virt.measure('m2', 0.2)
      virt.measure('m3', 0.4)
      // A height of 0 is a height: the row counts as measured.
      expect(cache.heightOf({ id: 'm1' })).toBe(0)
      expect(virt.offsetOfIndex(1)).toBe(0)
      expect(virt.offsetOfIndex(2)).toBe(0)
      expect(virt.totalHeight()).toBe(0.4)
      dispose()
    })
  })

  it('keeps an anchor at the first empty band and preserves an offset inside the later body', () => {
    createRoot((dispose) => {
      try {
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_text' },
          { seq: 2, kind: 'assistant_thinking' },
          { seq: 3, kind: 'assistant_text' },
        ]))
        virt.measure('m1', 0)
        virt.measure('m2', 0)
        virt.measure('m3', 40)
        const atStart = virt.anchorAt(0)!
        const insideBody = virt.anchorAt(10)!
        expect(atStart).toEqual({
          id: 'm1',
          offsetWithinRow: 0,
          basisHeight: 0,
          gapFraction: 0,
        })
        expect(insideBody.id).toBe('m3')
        expect(virt.scrollTopForAnchor(insideBody)).toBe(10)
        virt.measure('m1', 20)
        expect(virt.scrollTopForAnchor(atStart)).toBe(0)
        expect(virt.scrollTopForAnchor(insideBody)).toBe(30)
        expect(virt.gapAboveOf('m2') === 0).toBe(true)
        virt.measure('m2', 10)
        expect(virt.gapAboveOf('m2')).toBe(-BAND_BORDER_PX)
        expect(virt.gapAboveOf('m3')).toBe(-BAND_BORDER_PX)
      }
      finally {
        dispose()
      }
    })
  })

  it('computes the offsets and the total height of the rows', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(5))
      // 5 rows of 100px with 20px gaps between them.
      expect(virt.offsetOfIndex(0)).toBe(0)
      expect(virt.offsetOfIndex(1)).toBe(120)
      expect(virt.offsetOfIndex(2)).toBe(240)
      expect(virt.offsetOfIndex(4)).toBe(480)
      expect(virt.totalHeight()).toBe(580) // 5*100 + 4*20
      dispose()
    })
  })

  it('returns the measured height of a row, and 0 for an index outside the list', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(2))
      virt.measure('m1', 140)
      virt.measure('m2', 60)
      expect(virt.heightOfIndex(0)).toBe(140)
      expect(virt.heightOfIndex(1)).toBe(60)
      // A row outside the list has no height.
      expect(virt.heightOfIndex(-1)).toBe(0)
      expect(virt.heightOfIndex(2)).toBe(0)
      expect(virt.heightOfId('m1')).toBe(140)
      expect(virt.heightOfId('missing')).toBe(0)
      dispose()
    })
  })

  it('reports a row of the list with no height once, and lays it out as 0', () => {
    createRoot((dispose) => {
      // The list holds measured rows only in the app. A row with no height breaks that
      // invariant: a development build reports it, and the row takes no space.
      const reports = spyOnUnmeasuredRowReports()
      const { virt, setList } = setup(plainItems(3), { measured: false })
      expect(virt.heightOfId('m1')).toBe(0)
      expect(virt.heightOfId('m2')).toBe(0)
      expect(virt.heightOfId('m3')).toBe(0)
      // The two gaps of 20px.
      expect(virt.totalHeight()).toBe(40)
      expect(reports.reportedIds()).toEqual(['m1', 'm2', 'm3'])

      // A new list of the same rows rebuilds the offset map, which reports no row again.
      setList(plainItems(3))
      expect(virt.totalHeight()).toBe(40)
      expect(reports.reportedIds()).toEqual(['m1', 'm2', 'm3'])
      dispose()
    })
  })

  it('reports no row with no height in a production build, and still lays it out as 0', () => {
    vi.stubEnv('DEV', false)
    onTestFinished(() => {
      vi.unstubAllEnvs()
    })
    createRoot((dispose) => {
      const reports = spyOnUnmeasuredRowReports()
      const { virt } = setup(plainItems(2), { measured: false })
      expect(virt.totalHeight()).toBe(20)
      expect(reports.reportedIds()).toEqual([])
      dispose()
    })
  })

  it('uses the small gap when the lower row has span lines', () => {
    createRoot((dispose) => {
      // rows: plain, span, plain -> gap(0)=small (row1 has span), gap(1)=large (row2 plain)
      const { virt } = setup(makeItems([
        { seq: 1 },
        { seq: 2, span: true },
        { seq: 3 },
      ]))
      expect(virt.offsetOfIndex(1)).toBe(110) // 100 + small gap (lower row has span)
      expect(virt.offsetOfIndex(2)).toBe(230) // +100 + large gap (lower row plain)
      expect(virt.totalHeight()).toBe(330) // +100
      dispose()
    })
  })

  // The pending row of a tool call continues its request row: the list leaves no gap
  // above it, and the row's own content holds its top space. An empty one takes no space.
  it('leaves no gap above a row that continues the row above it', () => {
    createRoot((dispose) => {
      const items = makeItems([
        { seq: 1 },
        { seq: 2, span: true },
        { seq: 3 },
      ])
      items[1] = { ...items[1]!, continuesPrevious: true }
      const { virt } = setup(items)
      expect(virt.offsetOfIndex(1)).toBe(100) // 100 + no gap
      expect(virt.gapAboveOf('m2')).toBe(0)
      expect(virt.offsetOfIndex(2)).toBe(220) // +100 + large gap (lower row plain)
      dispose()
    })
  })

  describe('adjacent band rows', () => {
    // A band paints a full-bleed strip with a line on its top and bottom edge. Two
    // adjacent bands overlap by one border width so the pair shows ONE line.
    it('overlaps two adjacent bands by the band border width', () => {
      createRoot((dispose) => {
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_text' },
          { seq: 2, kind: 'assistant_thinking' },
        ]))
        expect(virt.offsetOfIndex(1)).toBe(100 - BAND_BORDER_PX)
        expect(virt.totalHeight()).toBe(200 - BAND_BORDER_PX)
        dispose()
      })
    })

    it('overlaps once per pair across a run of bands', () => {
      createRoot((dispose) => {
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_thinking' },
          { seq: 2, kind: 'assistant_text' },
          { seq: 3, kind: 'assistant_thinking' },
          { seq: 4, kind: 'assistant_text' },
        ]))
        expect(virt.offsetOfIndex(1)).toBe(100 - BAND_BORDER_PX)
        expect(virt.offsetOfIndex(2)).toBe(200 - 2 * BAND_BORDER_PX)
        expect(virt.offsetOfIndex(3)).toBe(300 - 3 * BAND_BORDER_PX)
        expect(virt.totalHeight()).toBe(400 - 3 * BAND_BORDER_PX)
        dispose()
      })
    })

    it('keeps the normal gap when only one side of the pair is a band', () => {
      createRoot((dispose) => {
        // band -> tool, then tool -> band. Neither pair merges, so both keep the
        // large gap: a band next to a non-band still draws both of its own lines.
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_text' },
          { seq: 2, kind: 'tool_use' },
          { seq: 3, kind: 'assistant_thinking' },
        ]))
        expect(virt.offsetOfIndex(1)).toBe(120)
        expect(virt.offsetOfIndex(2)).toBe(240)
        expect(virt.totalHeight()).toBe(340)
        dispose()
      })
    })

    it('overlaps a band pair even when the lower band has span lines', () => {
      createRoot((dispose) => {
        // The band rule wins over the small-gap rule. The rails then meet at the
        // band's own shared border rather than across a gap, and the bridge across
        // that seam collapses -- it sizes itself from gapAboveOf, asserted below.
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_text', span: true },
          { seq: 2, kind: 'assistant_thinking', span: true },
        ]))
        expect(virt.offsetOfIndex(1)).toBe(100 - BAND_BORDER_PX)
        dispose()
      })
    })

    it('still uses the small gap when a span row below a band is not a band', () => {
      createRoot((dispose) => {
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_text' },
          { seq: 2, kind: 'tool_use', span: true },
        ]))
        expect(virt.offsetOfIndex(1)).toBe(110)
        dispose()
      })
    })

    it('adds no gap after the last row, band or not', () => {
      createRoot((dispose) => {
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_text' },
        ]))
        expect(virt.totalHeight()).toBe(100)
        dispose()
      })
    })

    it('reports the gap above each row, which is what sizes the span-line bridge', () => {
      // The bridge overlay reads this instead of restating the gap as a token of its
      // own, so a merged band seam -- where there IS no gap -- collapses its segment
      // rather than drawing one for a gap that does not exist.
      createRoot((dispose) => {
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_text' },
          { seq: 2, kind: 'assistant_thinking' },
          { seq: 3, kind: 'tool_use', span: true },
          { seq: 4, kind: 'tool_result' },
        ]))
        expect(virt.gapAboveOf('m1')).toBe(0) // nothing above the first row
        expect(virt.gapAboveOf('m2')).toBe(-BAND_BORDER_PX) // merged band pair
        expect(virt.gapAboveOf('m3')).toBe(10) // small gap: the lower row has rails
        expect(virt.gapAboveOf('m4')).toBe(20) // large gap
        expect(virt.gapAboveOf('nope')).toBe(0) // an absent row bridges nothing
        dispose()
      })
    })

    it('treats a row with no kind as a non-band', () => {
      createRoot((dispose) => {
        const { virt } = setup(makeItems([
          { seq: 1 },
          { seq: 2 },
        ]))
        expect(virt.offsetOfIndex(1)).toBe(120)
        dispose()
      })
    })
  })

  it('shifts the later offsets by a measured height', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      expect(virt.measure('m1', 200)).toBe(true)
      expect(virt.offsetOfIndex(1)).toBe(220) // 200 + 20
      // Re-measuring within epsilon is a no-op.
      expect(virt.measure('m1', 200.2)).toBe(false)
      expect(virt.heightOfIndex(0)).toBe(200)
      dispose()
    })
  })

  it('keeps the cached height below MEASURE_EPSILON_PX and commits a re-measure of MEASURE_EPSILON_PX', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      virt.measure('m1', 200)

      expect(virt.measure('m1', 200 + MEASURE_EPSILON_PX - 0.01)).toBe(false)
      expect(virt.measure('m1', 200 - MEASURE_EPSILON_PX + 0.01)).toBe(false)
      expect(virt.heightOfIndex(0)).toBe(200)

      expect(virt.measure('m1', 200 - MEASURE_EPSILON_PX)).toBe(true)
      expect(virt.heightOfIndex(0)).toBe(200 - MEASURE_EPSILON_PX)
      dispose()
    })
  })

  it('rejects a negative height and keeps the prior measurement', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      virt.measure('m1', 200)
      expect(virt.measure('m1', -1)).toBe(false)
      expect(virt.heightOfIndex(0)).toBe(200)
      dispose()
    })
  })

  it('records the last commit for drift attribution -- first measure vs re-measure', () => {
    createRoot((dispose) => {
      const a: ChatRowItem = { id: 'a', hasSpanLines: false, kind: 'user_text' }
      const b: ChatRowItem = { id: 'b', hasSpanLines: false, kind: 'user_text' }
      const { virt, setList, cache } = setup([], { measured: false })
      expect(cache.lastMeasurement()).toBeUndefined()

      // First measure: a hidden copy measures the row before the admission adds it to the
      // list. The row had no height, so the commit states no previous height.
      expect(cache.commit(a, 40, undefined, 'premeasure')).toBe('moved')
      expect(cache.lastMeasurement()).toMatchObject({
        id: 'a',
        kind: 'user_text',
        source: 'premeasure',
        previousHeight: undefined,
        newHeight: 40,
      })
      const seqAfterFirst = cache.lastMeasurement()?.commitSeq ?? 0
      cache.commit(b, 40, undefined, 'premeasure')
      setList([
        a,
        b,
      ])

      // Re-measure: the offset map held the cached 40 -- a re-measure drift points at a
      // premeasured-vs-visible mismatch or a chrome/content change.
      virt.measure('a', 55)
      const reMeasure = cache.lastMeasurement()
      expect(reMeasure).toMatchObject({
        id: 'a',
        kind: 'user_text',
        source: 'visible',
        previousHeight: 40,
        newHeight: 55,
      })
      // commitSeq advances, so two consecutive drift WARNs reveal commits between them.
      expect(reMeasure?.commitSeq ?? 0).toBeGreaterThan(seqAfterFirst)
      dispose()
    })
  })

  it('keeps the measured-height cache bound above the chat window ceiling', () => {
    // The eviction of the height cache never drops a loaded row, so the cap limits the
    // heights of the rows that left the window. It must exceed the largest in-memory
    // window, or it would leave no room for such a row beside a full window.
    expect(HEIGHT_CACHE_MAX).toBeGreaterThan(MAX_LOADED_CHAT_MESSAGES_CEILING)
  })

  it('records a previous height on a re-measure only, not on the first commit', () => {
    createRoot((dispose) => {
      const items: ChatRowItem[] = [
        { id: 'a', hasSpanLines: false },
      ]
      // The kit commits the first height of the row in bulk, as the stored heights do.
      const { virt, cache } = setup(items, { measured: 200 })
      // First height for the row: the commit info states no previous height (the drift WARN
      // reads this).
      expect(cache.lastMeasurement()).toMatchObject({
        id: 'a',
        source: 'premeasure',
        previousHeight: undefined,
        newHeight: 200,
      })
      virt.measure('a', 260) // re-measure (async growth) — the prior height is the baseline
      expect(cache.lastMeasurement()).toMatchObject({
        id: 'a',
        previousHeight: 200,
        newHeight: 260,
      })
      dispose()
    })
  })

  it('takes a premeasured height and records it as a premeasure commit', () => {
    createRoot((dispose) => {
      const item: ChatRowItem = { id: 'a', hasSpanLines: false, heightKey: 'k1' }
      const { virt, setList, cache } = setup([], { measured: false })
      expect(cache.commit(item, 275, 'k1', 'premeasure')).toBe('moved')
      // The admission adds the measured row to the list.
      setList([
        item,
      ])
      expect(virt.heightOfIndex(0)).toBe(275)
      expect(cache.heightOf(item)).toBe(275)
      // Hidden premeasurement is cache warm-up, not a visible mount — the commit is
      // attributed to 'premeasure' so drift diagnostics can tell the sources apart.
      expect(cache.lastMeasurement()).toMatchObject({ id: 'a', source: 'premeasure', previousHeight: undefined })
      dispose()
    })
  })

  it('does not let hidden premeasure overwrite an already measured mounted row', () => {
    createRoot((dispose) => {
      try {
        const item: ChatRowItem = { id: 'a', hasSpanLines: false, heightKey: 'k1' }
        // A hidden copy measured the row at 100px. The mounted row reads 320px.
        const { virt, cache } = setup([item])
        virt.attachRow('a', fakeRow(320))

        expect(virt.heightOfIndex(0)).toBe(320)
        expect(cache.commit(item, 120, 'k1', 'premeasure')).toBe('none')
        expect(virt.heightOfIndex(0)).toBe(320)
      }
      finally {
        dispose()
      }
    })
  })

  it('ignores a pre-measured height when the row heightKey changed before commit', () => {
    createRoot((dispose) => {
      // The list holds a row with no height on purpose, to show that it takes no space.
      const reports = spyOnUnmeasuredRowReports()
      const item = (heightKey: string): ChatRowItem => ({ id: 'a', hasSpanLines: false, heightKey })
      const { virt, setList, cache } = setup([
        item('old'),
      ], { measured: false })
      setList([
        item('new'),
      ])
      expect(cache.commit(item('new'), 275, 'old', 'premeasure')).toBe('none')
      // The row has no height, so it takes no space.
      expect(virt.heightOfIndex(0)).toBe(0)
      expect(cache.heightOf(item('new'))).toBeUndefined()
      expect(reports.reportedIds()).toEqual(['a'])
      dispose()
    })
  })

  it('does not count a height of another key as measured when heightKey changes', () => {
    createRoot((dispose) => {
      const reports = spyOnUnmeasuredRowReports()
      const item = (heightKey: string): ChatRowItem => ({ id: 'a', hasSpanLines: false, heightKey })
      const { virt, setList, cache } = setup([], { measured: false })
      expect(cache.commit(item('old'), 275, 'old', 'premeasure')).toBe('moved')
      expect(cache.heightOf(item('old'))).toBe(275)
      setList([
        item('old'),
      ])
      expect(virt.heightOfIndex(0)).toBe(275)
      // The list then holds the row under a key with no height, on purpose, to show that the
      // row takes no space.
      setList([
        item('new'),
      ])
      expect(cache.heightOf(item('new'))).toBeUndefined()
      expect(virt.heightOfIndex(0)).toBe(0)
      expect(reports.reportedIds()).toEqual(['a'])
      // The cache keeps the height under the key of its measure, and it counts again there.
      setList([
        item('old'),
      ])
      expect(cache.heightOf(item('old'))).toBe(275)
      expect(virt.heightOfIndex(0)).toBe(275)
      dispose()
    })
  })

  describe('a height key change of a mounted row', () => {
    /** Row `a` under the height key `key`. */
    const rowA = (key: string): ChatRowItem => ({ id: 'a', hasSpanLines: false, heightKey: key })
    const keyedItems = (key: string): ChatRowItem[] => [
      rowA(key),
      { id: 'b', hasSpanLines: false, heightKey: 'b' },
    ]

    /**
     * The entry of row `a` in a snapshot: its height, with the key under which a measure
     * produced it. A mounted row whose key changed keeps the entry of its old key until a
     * read of its element commits under the new key.
     */
    const savedHeightOfA = (cache: RowHeightCache) =>
      cache.snapshot().find(height => height.id === 'a')

    it('keeps the height that the row measured as its slot, with no commit, so the row needs no hidden copy', () => {
      createRoot((dispose) => {
        const { virt, setList, cache } = setup(keyedItems('old'))
        virt.attachRow('a', fakeRow(320))
        expect(virt.heightOfIndex(0)).toBe(320)
        expect(savedHeightOfA(cache)).toEqual({ id: 'a', heightKey: 'old', height: 320 })
        const commitSeq = cache.lastMeasurement()?.commitSeq

        setList(keyedItems('new'))

        expect(virt.heightOfIndex(0)).toBe(320)
        expect(cache.heightOf(rowA('new'))).toBe(320)
        // No measure under the new key produced the height yet: the entry keeps its key.
        expect(savedHeightOfA(cache)).toEqual({ id: 'a', heightKey: 'old', height: 320 })
        expect(cache.lastMeasurement()?.commitSeq).toBe(commitSeq)
        // The visible copy is the authority on what the row shows.
        expect(cache.commit(rowA('new'), 120, 'new', 'premeasure')).toBe('none')
        expect(virt.heightOfIndex(0)).toBe(320)
        dispose()
      })
    })

    it('replaces the kept height with the next measure of the row, as a measure again of the same row', () => {
      createRoot((dispose) => {
        const { virt, setList, cache } = setup(keyedItems('old'))
        virt.attachRow('a', fakeRow(320))
        setList(keyedItems('new'))

        expect(virt.measure('a', 200)).toBe(true)

        expect(virt.heightOfIndex(0)).toBe(200)
        expect(savedHeightOfA(cache)).toEqual({ id: 'a', heightKey: 'new', height: 200 })
        expect(cache.lastMeasurement()).toMatchObject({
          id: 'a',
          source: 'visible',
          previousHeight: 320,
          newHeight: 200,
        })
        dispose()
      })
    })

    it('stops using the kept height when the row unmounts before it measures under its new key', async () => {
      // The admission takes such a row out of the list. This list keeps it, to show that the
      // offset map follows the cache.
      const reports = spyOnUnmeasuredRowReports()
      const { virt, setList, cache, row, dispose } = buildInRoot(() => {
        const kit = setup(keyedItems('old'))
        const row = fakeRow(320)
        kit.virt.attachRow('a', row)
        return { ...kit, row }
      })
      try {
        setList(keyedItems('new'))
        expect(cache.heightOf(rowA('new'))).toBe(320)
        const version = virt.geometryVersion()

        virt.detachRow(row)
        // The notice of the unmount waits for a microtask (see the next case).
        await Promise.resolve()

        // The provisional height described only the element that showed it. The row has no
        // height now, so it takes no space.
        expect(cache.heightOf(rowA('new'))).toBeUndefined()
        expect(virt.heightOfId('a')).toBe(0)
        expect(reports.reportedIds()).toEqual(['a'])
        // The offset map reads the heights through the cache, so the unmount changes it.
        expect(virt.geometryVersion()).toBeGreaterThan(version)
        // The cache deleted nothing: the height counts again under the key of its measure.
        setList(keyedItems('old'))
        expect(cache.heightOf(rowA('old'))).toBe(320)
        expect(virt.heightOfId('a')).toBe(320)
      }
      finally {
        dispose()
      }
    })

    it('changes the version when a row with a kept height leaves the list in the update that unmounts it', async () => {
      // The row leaves the list of the virtualizer, but it stays loaded: the admission and
      // the staging bands still ask the cache for it, under its current key.
      const answers: Array<number | undefined> = []
      const { virt, setList, cache, row, dispose } = buildInRoot(() => {
        const kit = setup(keyedItems('old'))
        const row = fakeRow(320)
        kit.virt.attachRow('a', row)
        // A reader of the heights that is not the offset map, as the staging bands are.
        createRenderEffect(() => answers.push(kit.cache.heightOf(rowA('new'))))
        return { ...kit, row }
      })
      try {
        setList(keyedItems('new'))
        expect(answers.at(-1)).toBe(320)
        const version = virt.geometryVersion()

        batch(() => {
          setList([
            { id: 'b', hasSpanLines: false, heightKey: 'b' },
          ])
          virt.detachRow(row)
        })
        await Promise.resolve()

        expect(cache.heightOf(rowA('new'))).toBeUndefined()
        expect(virt.geometryVersion()).toBeGreaterThan(version)
        expect(answers.at(-1)).toBeUndefined()
      }
      finally {
        dispose()
      }
    })

    it('keeps the height of a row whose element is replaced in one update: the old one detaches, then the new one attaches', async () => {
      // A row whose entry changes is built again by the row list: the old element detaches
      // before the new element attaches. The row must count as measured throughout, or the
      // admission would take it out of the list until a hidden copy measured it again.
      const [key, setKey] = createSignal('old')
      const measuredAt: boolean[] = []
      const { virt, setList, cache, first, second, dispose } = buildInRoot(() => {
        const kit = setup(keyedItems('old'))
        const first = fakeRow(320)
        kit.virt.attachRow('a', first)
        // The new element has no layout yet, so its attach commits no height.
        const second = noLayoutRow()
        // A reader of the heights, as the admission is: it runs again at each notice.
        createRenderEffect(() => measuredAt.push(kit.cache.heightOf(rowA(key())) !== undefined))
        return { ...kit, first, second }
      })
      try {
        batch(() => {
          setKey('new')
          setList(keyedItems('new'))
        })
        virt.detachRow(first)
        virt.attachRow('a', second)
        await Promise.resolve()

        expect(measuredAt.length).toBeGreaterThan(1)
        expect(measuredAt.every(measured => measured)).toBe(true)
        expect(cache.heightOf(rowA('new'))).toBe(320)
        expect(virt.heightOfIndex(0)).toBe(320)
      }
      finally {
        dispose()
      }
    })

    it('keeps the slot of a row whose element is replaced for a reader of the geometry between the detach and the attach', async () => {
      // The row list replaces the element of a row inside one update, and the offset map is
      // computed again when its first reader asks. That reader can run after the detach of
      // the old element and before the attach of the new one. It must get the height that
      // the row shows, or the map keeps a height of 0 for the row: nothing changes the
      // version of the cache afterwards, because the row is mounted again.
      const { virt, setList, first, second, dispose } = buildInRoot(() => {
        const kit = setup(keyedItems('old'))
        const first = fakeRow(320)
        kit.virt.attachRow('a', first)
        // The new element has no layout yet, as while its content waits for a worker.
        const second = noLayoutRow()
        return { ...kit, first, second }
      })
      try {
        let between: number | undefined
        batch(() => {
          setList(keyedItems('new'))
          virt.detachRow(first)
          between = virt.heightOfIndex(0)
          virt.attachRow('a', second)
        })
        await Promise.resolve()

        expect(between).toBe(320)
        expect(virt.heightOfIndex(0)).toBe(320)
        // Row `b` keeps its place below row `a`.
        expect(virt.offsetOfIndex(1)).toBe(340)
      }
      finally {
        dispose()
      }
    })

    it('keeps the slot of a row whose element is replaced when the attach itself reads the geometry', async () => {
      // With a known viewport, `attachRow` asks whether the row lies above the view, which
      // reads the offset map before the measurer counts the row as mounted again.
      const { virt, setList, first, second, dispose } = buildInRoot(() => {
        const kit = setup(keyedItems('old'))
        const first = fakeRow(320)
        kit.virt.attachRow('a', first)
        kit.virt.updateViewport(0, 500)
        const second = noLayoutRow()
        return { ...kit, first, second }
      })
      try {
        batch(() => {
          setList(keyedItems('new'))
          virt.detachRow(first)
          virt.attachRow('a', second)
        })
        await Promise.resolve()

        expect(virt.heightOfIndex(0)).toBe(320)
        expect(virt.offsetOfIndex(1)).toBe(340)
      }
      finally {
        dispose()
      }
    })

    it('keeps the height after the row unmounts once a measure of the same height confirms it under the new key', () => {
      createRoot((dispose) => {
        const { virt, setList, cache } = setup(keyedItems('old'))
        const row = fakeRow(320)
        virt.attachRow('a', row)
        setList(keyedItems('new'))

        expect(virt.measure('a', 320)).toBe(false)
        expect(savedHeightOfA(cache)).toEqual({ id: 'a', heightKey: 'new', height: 320 })
        virt.detachRow(row)

        expect(cache.heightOf(rowA('new'))).toBe(320)
        expect(virt.heightOfIndex(0)).toBe(320)
        dispose()
      })
    })

    it('keeps the height while a newer element of the row holds it, and stops using it when the last element unmounts', async () => {
      // The list keeps the row after its unmount, to show that the offset map follows the
      // cache. The admission takes such a row out of the list.
      const reports = spyOnUnmeasuredRowReports()
      const { virt, cache, first, second, dispose } = buildInRoot(() => {
        const kit = setup(keyedItems('old'))
        const first = fakeRow(320)
        kit.virt.attachRow('a', first)
        kit.setList(keyedItems('new'))
        // A copy with no layout yet reads nothing, so its attach confirms no height.
        const second = noLayoutRow()
        kit.virt.attachRow('a', second)
        return { ...kit, first, second }
      })
      try {
        virt.detachRow(first)
        await Promise.resolve()
        expect(cache.heightOf(rowA('new'))).toBe(320)
        expect(virt.heightOfIndex(0)).toBe(320)

        expect(reports.reportedIds()).toEqual([])

        virt.detachRow(second)
        await Promise.resolve()
        expect(cache.heightOf(rowA('new'))).toBeUndefined()
        expect(virt.heightOfIndex(0)).toBe(0)
        expect(reports.reportedIds()).toEqual(['a'])
      }
      finally {
        dispose()
      }
    })

    it('saves a kept height under the key of its measure until a measure confirms it under the new key', () => {
      createRoot((dispose) => {
        const { virt, setList, cache } = setup(keyedItems('old'))
        virt.attachRow('a', fakeRow(320))
        setList(keyedItems('new'))

        // The persistence stores the digest of this key, so a reload adopts the height only
        // for a row under the old key.
        expect(savedHeightOfA(cache)).toEqual({ id: 'a', heightKey: 'old', height: 320 })

        virt.measure('a', 320)
        expect(savedHeightOfA(cache)).toEqual({ id: 'a', heightKey: 'new', height: 320 })
        dispose()
      })
    })

    it('confirms the height again when the key changes back to the key that measured it', () => {
      createRoot((dispose) => {
        const { virt, setList, cache } = setup(keyedItems('old'))
        const row = fakeRow(320)
        virt.attachRow('a', row)
        setList(keyedItems('new'))

        setList(keyedItems('old'))

        expect(savedHeightOfA(cache)).toEqual({ id: 'a', heightKey: 'old', height: 320 })
        virt.detachRow(row)
        expect(cache.heightOf(rowA('old'))).toBe(320)
        expect(virt.heightOfIndex(0)).toBe(320)
        dispose()
      })
    })

    it('reads the row again at the next flush, and confirms a height that holds under the new key, whether or not the row resizes', async () => {
      const { virt, setList, cache, dispose } = buildInRoot(() => {
        const kit = setup(keyedItems('old'))
        kit.virt.attachRow('a', fakeRow(320))
        return kit
      })
      try {
        setList(keyedItems('new'))
        const geometryVersion = virt.geometryVersion()
        const cacheVersion = cache.version()
        expect(savedHeightOfA(cache)).toEqual({ id: 'a', heightKey: 'old', height: 320 })

        await Promise.resolve()

        expect(savedHeightOfA(cache)).toEqual({ id: 'a', heightKey: 'new', height: 320 })
        expect(virt.heightOfIndex(0)).toBe(320)
        // The confirmation moves no row, so the geometry version holds. The version of the
        // cache changes: the persistence saves the snapshot on that change, and the snapshot
        // now holds the height.
        expect(virt.geometryVersion()).toBe(geometryVersion)
        expect(cache.version()).toBeGreaterThan(cacheVersion)
      }
      finally {
        dispose()
      }
    })

    it('commits the height that the row reads at the next flush after its key changes', async () => {
      const { virt, setList, cache, row, dispose } = buildInRoot(() => {
        const kit = setup(keyedItems('old'))
        const row = fakeRow(320)
        kit.virt.attachRow('a', row)
        return { ...kit, row }
      })
      try {
        // The new content renders within the update that changes the key.
        row.getBoundingClientRect = () => new DOMRect(0, 0, 400, 200)
        setList(keyedItems('new'))

        await Promise.resolve()

        expect(virt.heightOfIndex(0)).toBe(200)
        expect(savedHeightOfA(cache)).toEqual({ id: 'a', heightKey: 'new', height: 200 })
      }
      finally {
        dispose()
      }
    })

    it('reads nothing from a row copy that replaces its content behind what it shows, and reads it once the copy shows the new content', async () => {
      const [replacing, setReplacing] = createSignal(false)
      const { virt, setList, cache, row, dispose } = buildInRoot(() => {
        const kit = setup(keyedItems('old'))
        const readiness = createRenderReadinessScope()
        readiness.trackReplacing(replacing)
        const row = fakeRow(320)
        kit.virt.attachRow('a', row, readiness)
        return { ...kit, row }
      })
      try {
        expect(savedHeightOfA(cache)).toEqual({ id: 'a', heightKey: 'old', height: 320 })
        // A diff view switches its layout: the old one stays in view while the new one mounts.
        setReplacing(true)
        setList(keyedItems('new'))
        await Promise.resolve()
        // No read under the new key ran: the entry keeps the key of its measure.
        expect(savedHeightOfA(cache)).toEqual({ id: 'a', heightKey: 'old', height: 320 })
        expect(virt.heightOfIndex(0)).toBe(320)

        row.getBoundingClientRect = () => new DOMRect(0, 0, 400, 200)
        setReplacing(false)
        await Promise.resolve()

        expect(virt.heightOfIndex(0)).toBe(200)
        expect(savedHeightOfA(cache)).toEqual({ id: 'a', heightKey: 'new', height: 200 })
      }
      finally {
        dispose()
      }
    })

    it('gives no measured height for a row that is not mounted when its key changes', async () => {
      // The admission keeps such a row out of the list. This list holds it, to show that the
      // row takes no space.
      const reports = spyOnUnmeasuredRowReports()
      const { virt, setList, cache, dispose } = buildInRoot(() => {
        const kit = setup(keyedItems('old'))
        const row = fakeRow(320)
        kit.virt.attachRow('a', row)
        kit.virt.detachRow(row)
        return kit
      })
      try {
        // The unmount is settled one microtask after the detach: until then a new element
        // could still take the row over (see the cases above).
        await Promise.resolve()

        setList(keyedItems('new'))

        expect(cache.heightOf(rowA('new'))).toBeUndefined()
        expect(virt.heightOfIndex(0)).toBe(0)
        expect(reports.reportedIds()).toEqual(['a'])
      }
      finally {
        dispose()
      }
    })

    it('counts a row whose last element just detached as mounted for one microtask, also across a key change', async () => {
      // The list keeps the row after its unmount, to show that the offset map follows the
      // cache. The admission takes such a row out of the list.
      const reports = spyOnUnmeasuredRowReports()
      const { virt, setList, cache, dispose } = buildInRoot(() => {
        const kit = setup(keyedItems('old'))
        const row = fakeRow(320)
        kit.virt.attachRow('a', row)
        kit.virt.detachRow(row)
        return kit
      })
      try {
        setList(keyedItems('new'))
        // No new element took the row over yet, and none can be excluded yet.
        expect(cache.heightOf(rowA('new'))).toBe(320)
        const version = virt.geometryVersion()

        await Promise.resolve()

        // No element took the row over, so the row has no height under its new key.
        expect(cache.heightOf(rowA('new'))).toBeUndefined()
        expect(virt.heightOfIndex(0)).toBe(0)
        expect(virt.geometryVersion()).toBeGreaterThan(version)
        expect(reports.reportedIds()).toEqual(['a'])
      }
      finally {
        dispose()
      }
    })

    /** A row whose element counts each read of its layout. `setRect` changes what a read gives. */
    const countedRow = (height: number): { el: HTMLElement, reads: () => number, setRect: (rect: DOMRect) => void } => {
      const el = fakeRow(height)
      let reads = 0
      let rect = new DOMRect(0, 0, 400, height)
      el.getBoundingClientRect = () => {
        reads++
        return rect
      }
      return {
        el,
        reads: () => reads,
        setRect: (next) => {
          rect = next
        },
      }
    }

    it('reads a mounted row again after its key changes, and commits under the new key', async () => {
      const { setList, cache, row, dispose } = buildInRoot(() => {
        const kit = setup(keyedItems('old'))
        const row = countedRow(320)
        kit.virt.attachRow('a', row.el)
        return { ...kit, row }
      })
      try {
        await Promise.resolve()
        const readsBefore = row.reads()

        setList(keyedItems('new'))
        await Promise.resolve()

        expect(row.reads()).toBe(readsBefore + 1)
        expect(savedHeightOfA(cache)).toEqual({ id: 'a', heightKey: 'new', height: 320 })
      }
      finally {
        dispose()
      }
    })

    it('does not read a mounted row again for a list change that keeps its key', async () => {
      const { setList, row, dispose } = buildInRoot(() => {
        const kit = setup(keyedItems('old'))
        const row = countedRow(320)
        kit.virt.attachRow('a', row.el)
        return { ...kit, row }
      })
      try {
        // The element has no layout at the read of the new key, so that read commits
        // nothing, and the height stays provisional across the list change below.
        row.setRect(new DOMRect(0, 0, 0, 0))
        setList(keyedItems('new'))
        await Promise.resolve()
        const readsAfterKeyChange = row.reads()

        // A measured row joins the list. Row `a` keeps its key.
        setList([
          ...keyedItems('new'),
          { id: 'c', hasSpanLines: false, heightKey: 'c' },
        ])
        await Promise.resolve()

        expect(row.reads()).toBe(readsAfterKeyChange)
      }
      finally {
        dispose()
      }
    })
  })

  it('measures a mounted row only once its content is ready, through its readiness scope', async () => {
    // A row that waits for content has a height that is wrong by that content, so
    // the visible measurer must read nothing from it. Its content can land without
    // a resize, so the scope's turn to ready alone must measure it then.
    const item: ChatRowItem = { id: 'a', hasSpanLines: false, heightKey: 'k1' }
    const { virt, cache, setBodyPending, dispose } = buildInRoot(() => {
      // A hidden copy measured the row at 100px. The mounted row reads 320px once ready.
      const kit = setup([item])
      const [bodyPending, setPending] = createSignal(true)
      const readiness = createRenderReadinessScope()
      readiness.track(bodyPending)
      kit.virt.attachRow('a', fakeRow(320), readiness)
      return { ...kit, setBodyPending: setPending }
    })
    try {
      expect(cache.heightOf(item)).toBe(100)
      expect(virt.heightOfIndex(0)).toBe(100)
      expect(virt.showsHeight('a')).toBe(false)

      setBodyPending(false)
      // The measurer commits in its next flush, a microtask later.
      expect(cache.heightOf(item)).toBe(100)
      await Promise.resolve()
      expect(cache.heightOf(item)).toBe(320)
      expect(virt.heightOfIndex(0)).toBe(320)
    }
    finally {
      dispose()
    }
  })

  it('exposes the fast-scroll flag reactively for the fling-skeleton gate', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(1))
      expect(virt.fastScrollActive()).toBe(false)
      virt.setFastScrollActive(true)
      expect(virt.fastScrollActive()).toBe(true)
      virt.setFastScrollActive(false)
      expect(virt.fastScrollActive()).toBe(false)
      dispose()
    })
  })

  it('preserves an in-gap viewport position as a gap FRACTION, re-applied against the current gap', () => {
    createRoot((dispose) => {
      const { virt, setList } = setup([
        { id: 'a', hasSpanLines: false },
        { id: 'b', hasSpanLines: false }, // gap(a->b) = large (20)
        { id: 'c', hasSpanLines: false },
      ])
      // Row 'a' is 100px tall; the 20px gap below it spans [100,120).
      // A scrollTop within the row body has no gap fraction.
      expect(virt.anchorAt(60)).toEqual({
        id: 'a',
        offsetWithinRow: 60,
        basisHeight: 100,
        gapFraction: 0,
      })
      // ...a scrollTop of 110 lands 10px into the 20px gap = HALFWAY (fraction 0.5).
      // offsetWithinRow clamps to the row bottom; gapFraction carries the in-gap part.
      const anchor = virt.anchorAt(110)
      expect(anchor).toEqual({
        id: 'a',
        offsetWithinRow: 100,
        basisHeight: 100,
        gapFraction: 0.5,
      })
      // Resolve reproduces the exact in-gap position: 100 (row bottom) + 0.5 * 20.
      expect(virt.scrollTopForAnchor(anchor!)).toBe(110)

      // Flip the next row's span lines so gap(a->b) shrinks 20 -> 10. The FRACTION is
      // re-applied to the new gap, so the pin stays halfway into it: 100 + 0.5 * 10.
      setList([
        { id: 'a', hasSpanLines: false },
        { id: 'b', hasSpanLines: true },
        { id: 'c', hasSpanLines: false },
      ])
      expect(virt.scrollTopForAnchor(anchor!)).toBe(105)
      dispose()
    })
  })

  it('falls back to the row bottom for an anchor with no gapFraction (pre-field / persisted)', () => {
    createRoot((dispose) => {
      const { virt } = setup([
        { id: 'a', hasSpanLines: false },
        { id: 'b', hasSpanLines: false },
      ])
      // An anchor restored from old persistence carries no gapFraction -> the prior
      // gap-independent pin-to-row-bottom behavior (no NaN from a missing field).
      expect(virt.scrollTopForAnchor({ id: 'a', offsetWithinRow: 100, basisHeight: 100 })).toBe(100)
      dispose()
    })
  })

  it('resolves a row PROPORTIONALLY so the fraction into the row is preserved when it measures taller', () => {
    createRoot((dispose) => {
      // Row 'a' is 100px tall, and it will measure 600.
      const { virt } = setup([
        { id: 'a', hasSpanLines: false },
        { id: 'b', hasSpanLines: false },
      ])
      // Anchor at the MIDDLE of the 100px row (scrollTop 50 -> fraction 0.5).
      const anchor = virt.anchorAt(50)
      expect(anchor).toEqual({
        id: 'a',
        offsetWithinRow: 50,
        basisHeight: 100,
        gapFraction: 0,
      })
      // Before the new measure, resolve returns the captured 50 (basis == current height).
      expect(virt.scrollTopForAnchor(anchor!)).toBe(50)
      // Row 'a' measures 600. The 0.5 fraction maps to 0.5 * 600 = 300 -- the pin
      // tracks the row's growth instead of staying at a stale 40px.
      virt.measure('a', 600)
      expect(virt.scrollTopForAnchor(anchor!)).toBe(300)
      dispose()
    })
  })

  describe('heightDebugOfId (raw-json debug surface)', () => {
    it('returns measured undefined before the row is measured', () => {
      createRoot((dispose) => {
        // The admission adds a row to the list only after a measure, so the row is not in
        // the list yet.
        const { virt, cache } = setup([], { measured: false })
        expect(virt.heightDebugOfId('m1')).toEqual({ measured: undefined })

        cache.commit({ id: 'm1', hasSpanLines: false }, 250, undefined, 'premeasure')
        expect(virt.heightDebugOfId('m1')).toEqual({ measured: 250 })
        dispose()
      })
    })

    it('surfaces the measured height once measured', () => {
      createRoot((dispose) => {
        const { virt } = setup(plainItems(2))
        virt.measure('m1', 250)
        expect(virt.heightDebugOfId('m1')).toEqual({ measured: 250 })
        dispose()
      })
    })

    it('omits stale measured height when the height key changes', () => {
      createRoot((dispose) => {
        const { virt, setList } = setup([
          { id: 'a', hasSpanLines: false, heightKey: 'old' },
        ])
        // The mounted row reads 250px, and keeps that height provisionally across the key
        // change below.
        virt.attachRow('a', fakeRow(250))
        expect(virt.heightDebugOfId('a')).toEqual({ measured: 250 })
        setList([
          { id: 'a', hasSpanLines: false, heightKey: 'new' },
        ])
        expect(virt.heightOfIndex(0)).toBe(250)
        expect(virt.heightDebugOfId('a')).toEqual({ measured: undefined })
        dispose()
      })
    })

    it('returns measured undefined for an unknown id', () => {
      createRoot((dispose) => {
        const { virt } = setup(plainItems(2))
        expect(virt.heightDebugOfId('nope')).toEqual({ measured: undefined })
        dispose()
      })
    })
  })

  describe('geometryVersion', () => {
    it('keeps its value and the total height for a commit of a row that is not in the list', () => {
      const { virt, cache, dispose } = buildInRoot(() => setup(plainItems(3)))
      try {
        const version = virt.geometryVersion()
        const total = virt.totalHeight()
        const cacheVersion = cache.version()

        // A hidden copy measures a staged row, which the admission did not add to the list yet.
        expect(cache.commit({ id: 'staged', hasSpanLines: false }, 300, undefined, 'premeasure')).toBe('moved')

        expect(cache.version()).toBeGreaterThan(cacheVersion)
        expect(virt.geometryVersion()).toBe(version)
        expect(virt.totalHeight()).toBe(total)
      }
      finally {
        dispose()
      }
    })

    it('changes for a commit that moves a listed row, also when the total height stays the same', () => {
      const { virt, dispose } = buildInRoot(() => setup(plainItems(3)))
      try {
        const version = virt.geometryVersion()
        const total = virt.totalHeight()

        // Row 0 grows by the 50px that row 1 loses, so only the offset of row 1 moves.
        batch(() => {
          virt.measure('m1', 150)
          virt.measure('m2', 50)
        })

        expect(virt.totalHeight()).toBe(total)
        expect(virt.offsetOfIndex(1)).toBe(170)
        expect(virt.geometryVersion()).toBeGreaterThan(version)
      }
      finally {
        dispose()
      }
    })

    it('keeps its value for a confirmed commit of a mounted row under a new key, and the debug view follows the commit', () => {
      const rowA = (key: string): ChatRowItem => ({ id: 'a', hasSpanLines: false, heightKey: key })
      const { virt, setList, debug, dispose } = buildInRoot(() => {
        const kit = setup([
          rowA('old'),
        ])
        kit.virt.attachRow('a', fakeRow(320))
        // The raw-JSON view reads the debug accessor in a computation of its own.
        const debug = createMemo(() => kit.virt.heightDebugOfId('a'))
        return { ...kit, debug }
      })
      try {
        setList([
          rowA('new'),
        ])
        // The row keeps its 320px provisionally: no measure produced it under the new key.
        expect(virt.heightOfIndex(0)).toBe(320)
        expect(debug()).toEqual({ measured: undefined })
        const version = virt.geometryVersion()

        // A read of the element confirms the height under the new key, so no row moves.
        expect(virt.measure('a', 320)).toBe(false)

        expect(virt.geometryVersion()).toBe(version)
        expect(debug()).toEqual({ measured: 320 })
      }
      finally {
        dispose()
      }
    })
  })
})

describe('useChatVirtualizer range', () => {
  // `setup` measures every row at 100px with a gap of 20px, so row i starts at 120 * i,
  // and it renders no overscan.
  const rangeIds = (virt: ReturnType<typeof setup>['virt'], list: readonly ChatRowItem[]) => {
    const { start, end } = virt.range()
    return { first: list[start]?.id, last: list[end - 1]?.id }
  }

  it('moves the range with its rows when rows are prepended', () => {
    createRoot((dispose) => {
      const newest = plainItems(100, 51)
      const { virt, setList } = setup(newest)
      virt.updateViewport(5000, 500)
      const before = virt.range()
      expect(before).toEqual({ start: 41, end: 46 })
      const idsBefore = rangeIds(virt, newest)

      const prepended = [
        ...plainItems(50, 1),
        ...newest,
      ]
      setList(prepended)

      expect(virt.range()).toEqual({ start: before.start + 50, end: before.end + 50 })
      expect(rangeIds(virt, prepended)).toEqual(idsBefore)
      dispose()
    })
  })

  it('keeps the range when its first row is trimmed away', () => {
    createRoot((dispose) => {
      const items = plainItems(100)
      const { virt, setList } = setup(items)
      virt.updateViewport(1200, 2000)
      expect(virt.range()).toEqual({ start: 10, end: 27 })
      const lastId = items[26]?.id

      const trimmed = items.slice(20)
      setList(trimmed)

      // The last row is now at index 6. The length of 17 rows ends before index 0,
      // so the start clamps to the list.
      expect(virt.range()).toEqual({ start: 0, end: 7 })
      expect(trimmed[virt.range().end - 1]?.id).toBe(lastId)
      dispose()
    })
  })

  it('keeps the length when only the first row remains', () => {
    createRoot((dispose) => {
      const items = plainItems(100)
      const { virt, setList } = setup(items)
      virt.updateViewport(1200, 500)
      expect(virt.range()).toEqual({ start: 10, end: 15 })

      // Rows above the slice and its last row leave: the first row moves to index 2.
      setList([
        ...items.slice(8, 14),
        ...items.slice(15),
      ])

      expect(virt.range()).toEqual({ start: 2, end: 7 })
      dispose()
    })
  })

  it('keeps the stored indices when both edge rows left the list', () => {
    createRoot((dispose) => {
      const { virt, setList } = setup(plainItems(100))
      virt.updateViewport(1200, 500)
      expect(virt.range()).toEqual({ start: 10, end: 15 })

      setList(plainItems(100, 1000))

      expect(virt.range()).toEqual({ start: 10, end: 15 })
      dispose()
    })
  })

  it('commits the current edge ids when only the ids changed', () => {
    createRoot((dispose) => {
      const { virt, setList } = setup(plainItems(100))
      virt.updateViewport(1200, 500)
      // A replaced window keeps the stored indices, and the next commit must store the
      // ids of the new rows. A later prepend then moves the slice with them.
      const replaced = plainItems(100, 1000)
      setList(replaced)
      virt.updateViewport(1200, 500)
      expect(virt.range()).toEqual({ start: 10, end: 15 })

      setList([
        ...plainItems(5, 500),
        ...replaced,
      ])

      expect(virt.range()).toEqual({ start: 15, end: 20 })
      dispose()
    })
  })

  it('keeps one neighbor on each side of a row that holds the whole view and its overscan', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(10), { overscanPx: 200 })
      virt.measure('m5', 2000)

      // Row 4 spans 480 to 2480, and holds the view at 1000 with 200px of overscan on each side.
      expect(virt.computeRange(1000, 500)).toEqual({ start: 3, end: 6 })
      // Rows of a normal height keep the slice of the view and its overscan.
      expect(virt.computeRange(0, 300)).toEqual({ start: 0, end: 5 })
      dispose()
    })
  })

  it('returns an empty range for an empty list', () => {
    createRoot((dispose) => {
      const { virt, setList } = setup(plainItems(10))
      virt.updateViewport(0, 500)
      expect(virt.range()).toEqual({ start: 0, end: 5 })

      setList([])
      expect(virt.range()).toEqual({ start: 0, end: 0 })

      setList(plainItems(1))
      expect(virt.range()).toEqual({ start: 0, end: 1 })
      dispose()
    })
  })

  it('renders no row before the first commit', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(10))
      expect(virt.range()).toEqual({ start: 0, end: 0 })

      virt.updateViewport(0, 500)
      expect(virt.range()).toEqual({ start: 0, end: 5 })
      dispose()
    })
  })

  it('keeps an empty commit empty when rows arrive before the next commit', () => {
    createRoot((dispose) => {
      const { virt, setList } = setup([])
      virt.updateViewport(0, 500)
      expect(virt.range()).toEqual({ start: 0, end: 0 })

      setList(plainItems(3))
      expect(virt.range()).toEqual({ start: 0, end: 0 })

      virt.updateViewport(0, 500)
      expect(virt.range()).toEqual({ start: 0, end: 3 })
      dispose()
    })
  })

  it('keeps the same range object while the indices hold', () => {
    createRoot((dispose) => {
      const items = plainItems(100)
      const { virt, setList } = setup(items)
      virt.updateViewport(1200, 500)
      const first = virt.range()

      // An append below the slice moves no index of it.
      setList([
        ...items,
        ...plainItems(3, 500),
      ])

      expect(virt.range()).toBe(first)
      dispose()
    })
  })
})

describe('useChatVirtualizer layoutShift', () => {
  // `setup` measures every row at 100px with a gap of 20px, so row i starts at 120 * i.
  // Row 10 is `m11`.
  const ROW_10 = 'm11'

  /**
   * A scroll element whose geometry the test sets, and a spacer and a layer for
   * `attachSpacer` and `attachLayer`. Each records its writes in `writes`, in order. The scroll element
   * follows the inline height of the spacer once the spacer has one, and its `scrollTop`
   * stays inside the scroll range, as a browser keeps it. With `roundScrollTop`, it also
   * stores each written `scrollTop` rounded to a whole pixel, as a browser rounds it to a
   * device pixel.
   */
  function fakeLayout(init: { clientHeight?: number, scrollHeight?: number, scrollTop?: number, roundScrollTop?: boolean } = {}) {
    const state = {
      clientHeight: init.clientHeight ?? 500,
      scrollHeight: init.scrollHeight ?? 2380,
      scrollTop: init.scrollTop ?? 0,
      spacerHeight: '',
      layerTransform: '',
    }
    const writes: string[] = []
    const scrollHeight = () => (state.spacerHeight === ''
      ? state.scrollHeight
      : Number.parseFloat(state.spacerHeight))
    const maxTop = () => Math.max(0, scrollHeight() - state.clientHeight)
    const scroller = document.createElement('div')
    Object.defineProperty(scroller, 'clientHeight', {
      configurable: true,
      get: () => state.clientHeight,
    })
    Object.defineProperty(scroller, 'scrollHeight', { configurable: true, get: scrollHeight })
    Object.defineProperty(scroller, 'scrollTop', {
      configurable: true,
      get: () => Math.min(state.scrollTop, maxTop()),
      set: (top: number) => {
        writes.push('scrollTop')
        const clamped = Math.max(0, Math.min(top, maxTop()))
        state.scrollTop = init.roundScrollTop === true
          ? Math.round(clamped)
          : clamped
      },
    })
    const spacer = document.createElement('div')
    Object.defineProperty(spacer, 'style', {
      configurable: true,
      value: {
        get height() {
          return state.spacerHeight
        },
        set height(value: string) {
          writes.push('spacer')
          state.spacerHeight = value
        },
      },
    })
    const layer = document.createElement('div')
    Object.defineProperty(layer, 'style', {
      configurable: true,
      value: {
        get transform() {
          return state.layerTransform
        },
        set transform(value: string) {
          writes.push('layer')
          state.layerTransform = value
        },
      },
    })
    return {
      state,
      writes,
      scroller,
      spacer,
      layer,
    }
  }

  /**
   * Mount a virtualizer over 20 rows, each with the 100px of a hidden copy, which `setup`
   * commits through the cache. The test continues outside the root, so a signal write runs
   * its effects at once, as a browser event does. `scrollElement` gives an accessor that can
   * return no element, in place of the fixed `scroller`.
   *
   * A mounted copy then reads each row at 100px (`virt.measure`), so the reader saw the
   * row at that height. Each row of `premeasuredIds` gets no such read: its 100px came from
   * the hidden copy alone, and the reader did not see the row at that height.
   */
  function mount(opts: {
    scroller?: HTMLElement
    scrollElement?: () => HTMLElement | undefined
    pinBottom?: () => boolean
    rowCount?: number
    spacerInsetPx?: Accessor<number> | number
    listStartsAtLoadedTop?: Accessor<boolean>
    premeasuredIds?: readonly string[]
  } = {}) {
    const mounted = buildInRoot(() => {
      const scroller = opts.scroller
      const scrollElement = opts.scrollElement ?? (scroller === undefined
        ? undefined
        : () => scroller)
      return setup(plainItems(opts.rowCount ?? 20), {
        ...(scrollElement === undefined
          ? {}
          : { scrollElement }),
        ...(opts.pinBottom === undefined
          ? {}
          : { pinBottom: opts.pinBottom }),
        ...(opts.spacerInsetPx === undefined
          ? {}
          : { spacerInsetPx: opts.spacerInsetPx }),
        ...(opts.listStartsAtLoadedTop === undefined
          ? {}
          : { listStartsAtLoadedTop: opts.listStartsAtLoadedTop }),
      })
    })
    for (let i = 1; i <= (opts.rowCount ?? 20); i++) {
      if (opts.premeasuredIds?.includes(`m${i}`) !== true)
        mounted.virt.measure(`m${i}`, 100)
    }
    return mounted
  }

  /** A manual animation frame queue in place of the global one, until `afterEach` removes it. */
  function manualFrames() {
    const frames = createManualFrames()
    vi.stubGlobal('requestAnimationFrame', frames.requestFrame)
    vi.stubGlobal('cancelAnimationFrame', frames.cancelFrame)
    return frames
  }

  /**
   * Mount the row `id` as an element with one child, both with stubbed rectangles. The
   * scroll element is at client top 0, so the row starts at `rowScrollTop - scrollTop` in
   * client coordinates, where `rowScrollTop` is the scroll coordinate of the row top.
   * `rowHeight` and `childTop` (from the row top) are live, and the attach reads
   * `rowHeight`. `element` is the row element, for a detach.
   */
  function mountRow(virt: ReturnType<typeof setup>['virt'], id: string, rowScrollTop: number, scrollTop: number, rowHeight = 100) {
    const rowTop = rowScrollTop - scrollTop
    const row = document.createElement('div')
    const geometry = { rowHeight, childTop: 20, element: row }
    row.getBoundingClientRect = () => new DOMRect(0, rowTop, 400, geometry.rowHeight)
    const child = document.createElement('div')
    child.getBoundingClientRect = () => new DOMRect(0, rowTop + geometry.childTop, 400, 70)
    row.append(child)
    document.body.append(row)
    virt.attachRow(id, row)
    return geometry
  }

  /** Mount row 10, which starts at scroll coordinate 1200, as `mountRow` does. */
  function mountRow10(virt: ReturnType<typeof setup>['virt'], scrollTop: number, rowHeight = 100) {
    return mountRow(virt, ROW_10, 1200, scrollTop, rowHeight)
  }

  afterEach(() => {
    vi.unstubAllGlobals()
    document.body.replaceChildren()
  })

  it('stays 0 without a scroll element', () => {
    const { virt, dispose } = mount()
    virt.measure('m4', 160)
    expect(virt.layoutShift()).toBe(0)
    expect(virt.spacerHeight()).toBe(virt.totalHeight())
    dispose()
  })

  it('stays 0 for a hidden element', () => {
    const { scroller } = fakeLayout({ clientHeight: 0, scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller })
    virt.measure('m4', 160)
    expect(virt.layoutShift()).toBe(0)
    dispose()
  })

  /**
   * A scroll element that the test can hide (a `clientHeight` of 0) or detach (the accessor
   * returns no element), with the layout of `fakeLayout`.
   */
  function leavingLayout(init: Parameters<typeof fakeLayout>[0]) {
    const layout = fakeLayout(init)
    const element = { attached: true }
    return {
      ...layout,
      scrollElement: () => (element.attached
        ? layout.scroller
        : undefined),
      leave: (how: 'hides' | 'detaches') => {
        if (how === 'hides')
          layout.state.clientHeight = 0
        else
          element.attached = false
      },
    }
  }

  it.each(['hides', 'detaches'] as const)('drops the shift to 0 at a geometry change once the scroll element %s', (how) => {
    const layout = leavingLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scrollElement: layout.scrollElement })
    virt.measure('m4', 160)
    expect(virt.layoutShift()).toBe(-60)

    layout.leave(how)
    virt.measure('m5', 130)

    expect(virt.layoutShift()).toBe(0)
    expect(virt.spacerHeight()).toBe(virt.totalHeight())
    dispose()
  })

  it('keeps the row at the viewport top in place when a row above measures', () => {
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller })
    const spacer = virt.spacerHeight()
    expect(virt.offsetOfId(ROW_10)! + virt.layoutShift()).toBe(1200)

    virt.measure('m4', 160)

    expect(virt.offsetOfId(ROW_10)! + virt.layoutShift()).toBe(1200)
    expect(virt.layoutShift()).toBe(-60)
    expect(virt.spacerHeight()).toBe(spacer)
    dispose()
  })

  it('keeps the rows in place across a prepend', () => {
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, setList, dispose } = mount({ scroller })

    setList([
      ...plainItems(5, 100),
      ...plainItems(20),
    ])

    expect(virt.offsetOfId(ROW_10)! + virt.layoutShift()).toBe(1200)
    expect(virt.layoutShift()).toBe(-600)
    dispose()
  })

  describe('a geometry change inside an effect', () => {
    // ChatView loads the stored heights of an older page in an effect, and the scroll hook
    // refreshes the rendered rows in a later effect of the same batch. Solid runs no
    // computation for a signal write inside an effect until the effects of the batch end.
    // So the shift must follow the geometry for a reader in that batch. Otherwise the
    // refresh slices the rows of the prepended page, and every mounted row unmounts and
    // mounts again: as a skeleton, during a fling.

    /** Prepend 5 rows in one effect, then run `read` in a later effect of the same batch. */
    function prependInEffect<T>(read: (virt: ReturnType<typeof setup>['virt'], scroller: HTMLElement) => T): T[] {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, setList, dispose } = mount({ scroller })
      const [page, setPage] = createSignal(0)
      const reads: T[] = []
      const disposeEffects = createRoot((disposeRoot) => {
        createEffect(on(page, () => setList([
          ...plainItems(5, 100),
          ...plainItems(20),
        ]), { defer: true }))
        createEffect(on(page, () => {
          reads.push(read(virt, scroller))
        }, { defer: true }))
        return disposeRoot
      })

      setPage(1)

      disposeEffects()
      dispose()
      return reads
    }

    it('gives a later effect of the batch the shift of the new geometry', () => {
      expect(prependInEffect(virt => ({
        shift: virt.layoutShift(),
        rowTop: virt.offsetOfId(ROW_10)! + virt.layoutShift(),
      }))).toEqual([
        { shift: -600, rowTop: 1200 },
      ])
    })

    it('slices the rows at the viewport for a refresh in a later effect of the batch', () => {
      // The viewport spans the scroll coordinates 1250 to 1750: row 10 (`m11`) to row 14
      // (`m15`), which the 5 prepended rows move to the indexes 15 to 19.
      expect(prependInEffect((virt, scroller) => {
        virt.updateViewport(scroller.scrollTop, scroller.clientHeight)
        return virt.range()
      })).toEqual([
        { start: 15, end: 20 },
      ])
    })

    it('converts the scroll coordinates of the viewport top to the content of the new geometry', () => {
      // Row 10 starts at the content coordinate 1800 after the prepend, and the viewport top
      // lies 50px inside it.
      expect(prependInEffect((virt, scroller) => virt.contentTopOf(scroller.scrollTop))).toEqual([1850])
    })

    it('moves the shift of the new geometry into scrollTop for a rebase in a later effect of the batch', () => {
      // The scroll hook rebases in an effect. A rebase that moved the shift of the old geometry
      // would leave the 600px of the prepend above the reachable top.
      expect(prependInEffect((virt, scroller) => ({
        removed: virt.rebase(),
        scrollTop: scroller.scrollTop,
        shift: virt.layoutShift(),
        row10: row10OnScreen(virt, scroller),
      }))).toEqual([
        {
          removed: -600,
          scrollTop: 1850,
          shift: 0,
          row10: -50,
        },
      ])
    })
  })

  describe('a rebase and a geometry change in one batch', () => {
    // Row 3 (`m4`) grows by 60px above the viewport top, so the shift is -60 before the batch.
    // The 5 prepended rows then add 600px above the view.

    /** Mount the rows with the layout elements attached, and grow row 3 by 60px. */
    function mountWithShift() {
      const layout = fakeLayout({ scrollTop: 1250 })
      const mounted = mount({ scroller: layout.scroller })
      mounted.virt.attachSpacer(layout.spacer)
      mounted.virt.attachLayer(layout.layer)
      mounted.virt.measure('m4', 160)
      expect(mounted.virt.layoutShift()).toBe(-60)
      return { ...mounted, layout }
    }

    const prepend = (): ChatRowItem[] => [
      ...plainItems(5, 100),
      ...plainItems(20),
    ]

    it('continues from the shift that the rebase kept for a prepend after it', () => {
      const { virt, setList, layout, dispose } = mountWithShift()

      batch(() => {
        expect(virt.rebase()).toBe(-60)
        setList(prepend())
      })

      expect(layout.scroller.scrollTop).toBe(1310)
      expect(virt.layoutShift()).toBe(-600)
      expect(row10OnScreen(virt, layout.scroller)).toBe(-50)
      dispose()
    })

    it('removes the shift of a prepend before the rebase', () => {
      const { virt, setList, layout, dispose } = mountWithShift()

      batch(() => {
        setList(prepend())
        expect(virt.rebase()).toBe(-660)
      })

      expect(layout.scroller.scrollTop).toBe(1910)
      expect(virt.layoutShift()).toBe(0)
      expect(row10OnScreen(virt, layout.scroller)).toBe(-50)
      expect(layout.state.layerTransform).toBe('translateY(0px)')
      expect(layout.state.spacerHeight).toBe(`${virt.spacerHeight()}px`)
      dispose()
    })
  })

  /**
   * The shift after `measuredId` measures 160px, on a scroll element whose `scrollTop`
   * reads `scrollTop` with no clamp. A rubber-band overscroll in Safari or WebKit can
   * report a `scrollTop` outside the scroll range, above the maximum at the bottom edge.
   * The virtualizer therefore passes `clampScrollTop` of the value to the shift rule.
   * The scroll range is 2380 - 500, so the maximum `scrollTop` is 1880.
   */
  function shiftAtUnclampedScrollTop(scrollTop: number, measuredId: string): number {
    const { scroller } = fakeLayout({ scrollHeight: 2380 })
    Object.defineProperty(scroller, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
    })
    const { virt, dispose } = mount({ scroller })
    virt.measure(measuredId, 160)
    const shift = virt.layoutShift()
    dispose()
    return shift
  }

  it('clamps a scrollTop above the maximum', () => {
    const shiftAt = (scrollTop: number) => shiftAtUnclampedScrollTop(scrollTop, 'm4')
    expect(shiftAt(99_999)).toBe(shiftAt(1880))
    expect(shiftAt(1880)).toBe(-60)
  })

  it('holds the row under the clamped viewport top for a scrollTop above the maximum', () => {
    // Row 15 (`m16`) spans 1800 to 1900, so it holds the clamped viewport top at 1880. It
    // grows below that point, so the shift is 0. An unclamped 99999 lies below the last
    // row instead, which holds its bottom edge and moves the rows up by the 60px growth.
    const shiftAt = (scrollTop: number) => shiftAtUnclampedScrollTop(scrollTop, 'm16')
    expect(shiftAt(1880)).toBe(0)
    expect(shiftAt(99_999)).toBe(shiftAt(1880))
  })

  it('keeps the content bottom while pinBottom is true', () => {
    const { scroller } = fakeLayout({ scrollTop: 1880 })
    const { virt, setList, dispose } = mount({
      scroller,
      pinBottom: () => true,
    })
    const spacer = virt.spacerHeight()

    setList(plainItems(21))

    expect(virt.spacerHeight()).toBe(spacer)
    expect(virt.layoutShift()).toBe(-120)
    dispose()
  })

  it('adds the displacement of the pivot element', () => {
    const frames = manualFrames()
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller })
    const row = mountRow10(virt, 1250)
    virt.updateViewport(1250, 500)
    frames.runFrames()

    row.childTop = 50
    virt.measure(ROW_10, 130)

    expect(virt.layoutShift()).toBe(-30)
    dispose()
  })

  describe('a height that the reader did not see', () => {
    // Row 10 spans the scroll coordinates 1200 to 1300, and row 11 (`m12`) starts at 1320.
    // The viewport top is at 1250, inside row 10, as after an upward scroll into the row.
    const ROW_11 = 'm12'

    it('holds the bottom edge of a premeasured row when its mounted copy reads another height', () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, cache, dispose } = mount({
        scroller,
        premeasuredIds: [ROW_10],
      })
      const spacer = virt.spacerHeight()
      expect(cache.heightOf({ id: ROW_10 })).toBe(100)

      // The copy is 30px taller than the hidden copy measured. Its attach reads it.
      mountRow10(virt, 1250, 130)

      expect(virt.heightOfId(ROW_10)).toBe(130)
      // The rows below row 10 keep their scroll coordinates: the row grows above the
      // reader. The spacer keeps its height, so the scroll range does not move either.
      expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1320)
      expect(virt.layoutShift()).toBe(-30)
      expect(virt.spacerHeight()).toBe(spacer)
      dispose()
    })

    it('holds the bottom edge when the first read of the copy gives a smaller height', () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, dispose } = mount({
        scroller,
        premeasuredIds: [ROW_10],
      })

      mountRow10(virt, 1250, 70)

      expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1320)
      expect(virt.layoutShift()).toBe(30)
      dispose()
    })

    it('holds the distance from the top edge at a later read of the same copy', () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, dispose } = mount({
        scroller,
        premeasuredIds: [ROW_10],
      })
      mountRow10(virt, 1250, 130)
      expect(virt.layoutShift()).toBe(-30)

      // The reader saw the row at 130px, so a later growth is a change of content that the
      // reader saw. No frame recorded a pivot element, so the content at the viewport top
      // keeps its distance from the row top, and the rows below move down.
      virt.measure(ROW_10, 170)

      expect(virt.layoutShift()).toBe(-30)
      expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1360)
      dispose()
    })

    it('holds the distance from the top edge after a first read that confirmed the height', () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, dispose } = mount({
        scroller,
        premeasuredIds: [ROW_10],
      })
      // The first read gives the premeasured height, so nothing moves, and the reader sees
      // the row at 100px from then on.
      mountRow10(virt, 1250, 100)
      expect(virt.layoutShift()).toBe(0)

      virt.measure(ROW_10, 140)

      expect(virt.layoutShift()).toBe(0)
      expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1360)
      dispose()
    })

    it('holds the bottom edge of a mounted row that a hidden copy measured before its first read', () => {
      // The row mounts with the 100px of its hidden copy and waits for its content, so no
      // read measures it. Its own first read then gives 130px.
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, cache, dispose } = mount({
        scroller,
        premeasuredIds: [ROW_10],
      })
      const waiting = createRoot((disposeRow) => {
        const [pending, setPending] = createSignal(true)
        const readiness = createRenderReadinessScope()
        readiness.track(pending)
        const row = document.createElement('div')
        row.getBoundingClientRect = () => new DOMRect(0, -50, 400, 130)
        document.body.append(row)
        virt.attachRow(ROW_10, row, readiness)
        return { setPending, disposeRow }
      })
      expect(virt.showsHeight(ROW_10)).toBe(false)
      // The mounted row has a height, so the cache refuses a later height of a hidden copy.
      expect(cache.commit({ id: ROW_10, hasSpanLines: false }, 90, undefined, 'premeasure')).toBe('none')
      expect(virt.heightOfId(ROW_10)).toBe(100)
      expect(virt.showsHeight(ROW_10)).toBe(false)
      const shiftBeforeRead = virt.layoutShift()
      const belowBeforeRead = virt.offsetOfId(ROW_11)! + shiftBeforeRead

      // The content lands: the copy turns measurable, and the next flush reads it.
      waiting.setPending(false)
      return Promise.resolve().then(() => {
        expect(virt.heightOfId(ROW_10)).toBe(130)
        expect(virt.showsHeight(ROW_10)).toBe(true)
        expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(belowBeforeRead)
        expect(virt.layoutShift()).toBe(shiftBeforeRead - 30)
        waiting.disposeRow()
        dispose()
      })
    })

    it('holds the bottom edge of the row at the viewport top when a row above it takes its first read in the same batch', () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, dispose } = mount({
        scroller,
        premeasuredIds: [
          'm4',
          ROW_10,
        ],
      })

      // The row measurer commits the reads of one flush in one batch, as here.
      batch(() => {
        virt.measure('m4', 160)
        virt.measure(ROW_10, 130)
      })

      // Row 3 grew by 60px above the reader, and row 10 by 30px at its top.
      expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1320)
      expect(virt.layoutShift()).toBe(-90)
      dispose()
    })

    it('holds the distance from the top edge when a row that the reader saw reads a new height under a new key', async () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const keyed = (key: string): ChatRowItem[] => plainItems(20).map(item => ({
        ...item,
        ...(item.id === ROW_10
          ? { heightKey: key }
          : {}),
      }))
      const { virt, setList, dispose } = buildInRoot(() => setup(keyed('old'), {
        scrollElement: () => scroller,
      }))
      for (let i = 1; i <= 20; i++)
        virt.measure(`m${i}`, 100)
      const row = mountRow10(virt, 1250)

      // The content of the row changes with its key, as at an expand. The row keeps its
      // 100px until the next flush reads its element, which now gives 140px.
      row.rowHeight = 140
      setList(keyed('new'))
      expect(virt.heightOfId(ROW_10)).toBe(100)
      await Promise.resolve()

      // The reader saw the row at 100px, so the row grows at its bottom.
      expect(virt.heightOfId(ROW_10)).toBe(140)
      expect(virt.layoutShift()).toBe(0)
      expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1360)
      dispose()
    })

    it('keeps the distance from the top edge of a row whose element is replaced in one update', () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, dispose } = mount({ scroller })
      const first = mountRow10(virt, 1250)

      // The row list builds the row again: the old element detaches, and the new element
      // attaches in the same update. The row stays mounted for the reader, who saw its
      // 100px, so the 140px of the new element are a change of content that the reader saw.
      virt.detachRow(first.element)
      mountRow10(virt, 1250, 140)

      expect(virt.layoutShift()).toBe(0)
      expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1360)
      dispose()
    })

    it('holds the bottom edge again for the first read of a copy that mounts after the row unmounted', async () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, cache, dispose } = mount({ scroller })
      const first = mountRow10(virt, 1250)
      virt.detachRow(first.element)
      // The unmount of the row settles in a microtask: no element took the row over.
      await Promise.resolve()
      expect(cache.heightOf({ id: ROW_10 })).toBe(100)

      // The row mounts again in a later update, as when the reader scrolls back to it.
      // The reader did not see the new copy at the cached 100px, and its first read
      // gives 140px.
      mountRow10(virt, 1250, 140)

      expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1320)
      expect(virt.layoutShift()).toBe(-40)
      dispose()
    })

    it('keeps the reader\'s view of a row whose new element takes over before the old one detaches', () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, dispose } = mount({ scroller })
      const first = mountRow10(virt, 1250)

      // The row stays mounted across the two elements, so the reader saw its 100px. The
      // new element reads 140px: a change of content that the reader saw.
      mountRow10(virt, 1250, 140)
      virt.detachRow(first.element)

      expect(virt.layoutShift()).toBe(0)
      expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1360)
      dispose()
    })

    describe('showsHeight', () => {
      it('is false for a row with no height, and for a row that only a hidden copy measured', () => {
        const { scroller } = fakeLayout({ scrollTop: 1250 })
        // The admission adds a row to the list only after a measure, so the list is empty.
        const { virt, setList, cache, dispose } = buildInRoot(() => setup([], {
          measured: false,
          scrollElement: () => scroller,
        }))
        expect(virt.showsHeight('m1')).toBe(false)

        expect(cache.commit({ id: 'm1', hasSpanLines: false }, 100, undefined, 'premeasure')).toBe('moved')
        setList(plainItems(1))

        expect(cache.heightOf({ id: 'm1' })).toBe(100)
        expect(virt.heightOfId('m1')).toBe(100)
        expect(virt.showsHeight('m1')).toBe(false)
        expect(virt.showsHeight('not-in-the-list')).toBe(false)
        dispose()
      })

      it('turns true at the first read of the mounted row, and false again when the row unmounts', async () => {
        const { scroller } = fakeLayout({ scrollTop: 1250 })
        const { virt, cache, dispose } = mount({
          scroller,
          premeasuredIds: [ROW_10],
        })
        expect(virt.showsHeight(ROW_10)).toBe(false)

        const copy = mountRow10(virt, 1250, 130)
        expect(virt.showsHeight(ROW_10)).toBe(true)

        virt.detachRow(copy.element)
        // The unmount settles in a microtask: until then a new element can take the row over.
        await Promise.resolve()
        expect(cache.heightOf({ id: ROW_10 })).toBe(130)
        expect(virt.showsHeight(ROW_10)).toBe(false)
        dispose()
      })

      it('stays true while a new element takes the row over before the old one detaches', async () => {
        const { scroller } = fakeLayout({ scrollTop: 1250 })
        const { virt, dispose } = mount({
          scroller,
          premeasuredIds: [ROW_10],
        })
        const first = mountRow10(virt, 1250)
        const second = mountRow10(virt, 1250)

        virt.detachRow(first.element)
        await Promise.resolve()
        expect(virt.showsHeight(ROW_10)).toBe(true)

        virt.detachRow(second.element)
        await Promise.resolve()
        expect(virt.showsHeight(ROW_10)).toBe(false)
        dispose()
      })

      it('stays true while a new element takes the row over after the old one detached, in one update', async () => {
        const { scroller } = fakeLayout({ scrollTop: 1250 })
        const { virt, dispose } = mount({
          scroller,
          premeasuredIds: [ROW_10],
        })
        const first = mountRow10(virt, 1250)

        // The row list builds the row again: the detach comes before the attach.
        virt.detachRow(first.element)
        expect(virt.showsHeight(ROW_10)).toBe(true)
        mountRow10(virt, 1250)
        await Promise.resolve()

        expect(virt.showsHeight(ROW_10)).toBe(true)
        dispose()
      })

      it('stays true when the height key of the mounted row changes', () => {
        const { scroller } = fakeLayout({ scrollTop: 1250 })
        const { virt, setList, dispose } = buildInRoot(() => setup([
          { id: 'a', heightKey: 'old', hasSpanLines: false },
        ], {
          scrollElement: () => scroller,
        }))
        const row = document.createElement('div')
        row.getBoundingClientRect = () => new DOMRect(0, 0, 400, 100)
        document.body.append(row)
        virt.attachRow('a', row)
        expect(virt.showsHeight('a')).toBe(true)

        // The element still shows its 100px, which the row keeps under the new key.
        setList([
          { id: 'a', heightKey: 'new', hasSpanLines: false },
        ])

        expect(virt.showsHeight('a')).toBe(true)
        dispose()
      })
    })

    // The reader scrolls up to a row that waits for its content behind a placeholder: the
    // copy of row 10 mounts above the view and reads nothing, because it is not measurable.
    // Then the view moves up, to the viewport top 1150: 50px of the view lie above row 10,
    // and 350px below it. The first read of the copy gives 700px.
    describe('a late row inside the view', () => {
      /** The viewport top at which row 10 is high in the view. */
      const VIEW_TOP = 1150

      /** Mount a copy of row 10 that waits for its content, so its attach reads nothing. */
      function mountWaitingRow10(virt: ReturnType<typeof setup>['virt']): HTMLElement {
        const row = document.createElement('div')
        row.getBoundingClientRect = () => new DOMRect(0, 0, 400, 100)
        document.body.append(row)
        virt.attachRow(ROW_10, row, {
          measurable: () => false,
          onMeasurable: () => {},
        })
        return row
      }

      /** Move the view to the viewport top `scrollTop`, as a scroll of the reader does. */
      function scrollTo(layout: ReturnType<typeof fakeLayout>, virt: ReturnType<typeof setup>['virt'], scrollTop: number): void {
        layout.state.scrollTop = scrollTop
        virt.updateViewport(scrollTop, layout.state.clientHeight)
      }

      it('holds the bottom edge of a row that mounted above the view', () => {
        const layout = fakeLayout({ scrollTop: 1400 })
        const { virt, dispose } = mount({
          scroller: layout.scroller,
          premeasuredIds: [ROW_10],
        })
        scrollTo(layout, virt, 1400)
        mountWaitingRow10(virt)
        expect(virt.showsHeight(ROW_10)).toBe(false)
        scrollTo(layout, virt, VIEW_TOP)
        const spacer = virt.spacerHeight()

        virt.measure(ROW_10, 700)

        // The rows below row 10 keep their scroll coordinates: the row grows above itself.
        expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1320)
        expect(virt.layoutShift()).toBe(-600)
        expect(virt.spacerHeight()).toBe(spacer)
        dispose()
      })

      it('holds the viewport top for a row that mounted inside the view', () => {
        const layout = fakeLayout({ scrollTop: VIEW_TOP })
        const { virt, dispose } = mount({
          scroller: layout.scroller,
          premeasuredIds: [ROW_10],
        })
        scrollTo(layout, virt, VIEW_TOP)
        mountWaitingRow10(virt)

        virt.measure(ROW_10, 700)

        // The row grows at its bottom, and the rows below it move down.
        expect(virt.layoutShift()).toBe(0)
        expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1920)
        dispose()
      })

      it('holds the viewport top for a row that mounted before the first viewport update', () => {
        const layout = fakeLayout({ scrollTop: 1400 })
        const { virt, dispose } = mount({
          scroller: layout.scroller,
          premeasuredIds: [ROW_10],
        })
        mountWaitingRow10(virt)
        scrollTo(layout, virt, VIEW_TOP)

        virt.measure(ROW_10, 700)

        expect(virt.layoutShift()).toBe(0)
        dispose()
      })

      it('holds the viewport top for a row that mounted within the slack of the viewport top', () => {
        // The top edge of row 10 is at 1200, half a pixel above the viewport top.
        const layout = fakeLayout({ scrollTop: 1200.5 })
        const { virt, dispose } = mount({
          scroller: layout.scroller,
          premeasuredIds: [ROW_10],
        })
        scrollTo(layout, virt, 1200.5)
        mountWaitingRow10(virt)
        scrollTo(layout, virt, VIEW_TOP)

        virt.measure(ROW_10, 700)

        expect(virt.layoutShift()).toBe(0)
        dispose()
      })

      it('holds the viewport top after the row unmounted and mounted again inside the view', async () => {
        const layout = fakeLayout({ scrollTop: 1400 })
        const { virt, dispose } = mount({
          scroller: layout.scroller,
          premeasuredIds: [ROW_10],
        })
        scrollTo(layout, virt, 1400)
        virt.detachRow(mountWaitingRow10(virt))
        // The unmount settles in a microtask, and the row then drops its record.
        await Promise.resolve()
        scrollTo(layout, virt, VIEW_TOP)
        mountWaitingRow10(virt)

        virt.measure(ROW_10, 700)

        expect(virt.layoutShift()).toBe(0)
        dispose()
      })

      it('holds the bottom edge when a new element takes the row over inside the view', () => {
        const layout = fakeLayout({ scrollTop: 1400 })
        const { virt, dispose } = mount({
          scroller: layout.scroller,
          premeasuredIds: [ROW_10],
        })
        scrollTo(layout, virt, 1400)
        const first = mountWaitingRow10(virt)
        scrollTo(layout, virt, VIEW_TOP)
        // The row stays mounted across the two elements, so it keeps its record.
        mountWaitingRow10(virt)
        virt.detachRow(first)

        virt.measure(ROW_10, 700)

        expect(virt.layoutShift()).toBe(-600)
        dispose()
      })

      it('compares a row that mounts after a rebase with the viewport top after that rebase', () => {
        // Row 12 (`m13`) spans the content coordinates 1440 to 1540.
        const ROW_12 = 'm13'
        const ROW_13 = 'm14'
        const layout = fakeLayout({ scrollTop: 1400 })
        const { virt, dispose } = mount({
          scroller: layout.scroller,
          premeasuredIds: [ROW_12],
        })
        virt.attachSpacer(layout.spacer)
        virt.attachLayer(layout.layer)
        scrollTo(layout, virt, 1400)
        // A row above the view shrinks by 60px, so the shift is 60, and the rebase moves the
        // shift into the position: the viewport top is at 1340 from then on.
        virt.measure('m4', 40)
        expect(virt.layoutShift()).toBe(60)
        expect(virt.rebase()).toBe(60)
        expect(layout.scroller.scrollTop).toBe(1340)
        // Row 12 starts at 1380 now, which is inside the view, and below 1400.
        const row = document.createElement('div')
        row.getBoundingClientRect = () => new DOMRect(0, 0, 400, 100)
        document.body.append(row)
        virt.attachRow(ROW_12, row, {
          measurable: () => false,
          onMeasurable: () => {},
        })

        virt.measure(ROW_12, 700)

        // The row mounted inside the view, so the viewport top holds, and the row below it
        // moves down. With the position from before the rebase, the row would count as
        // above the view.
        expect(virt.layoutShift()).toBe(0)
        expect(virt.offsetOfId(ROW_13)!).toBe(2100)
        dispose()
      })

      describe('notePlacement', () => {
        it('holds the viewport top after a placement that puts the row inside the view', () => {
          const layout = fakeLayout({ scrollTop: 1400 })
          const { virt, dispose } = mount({
            scroller: layout.scroller,
            premeasuredIds: [ROW_10],
          })
          scrollTo(layout, virt, 1400)
          mountWaitingRow10(virt)
          // A seek or a restore places the view: the reader did not move up to the row.
          layout.state.scrollTop = VIEW_TOP
          virt.notePlacement(VIEW_TOP)

          virt.measure(ROW_10, 700)

          expect(virt.layoutShift()).toBe(0)
          expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1920)
          dispose()
        })

        it('holds the bottom edge after a placement that leaves the row above the view', () => {
          const layout = fakeLayout({ scrollTop: 1400 })
          const { virt, dispose } = mount({
            scroller: layout.scroller,
            premeasuredIds: [ROW_10],
          })
          scrollTo(layout, virt, 1400)
          mountWaitingRow10(virt)
          layout.state.scrollTop = 1350
          virt.notePlacement(1350)
          scrollTo(layout, virt, VIEW_TOP)

          virt.measure(ROW_10, 700)

          expect(virt.layoutShift()).toBe(-600)
          dispose()
        })

        it('holds the bottom edge of a row that mounted inside the view, after a placement below it', () => {
          const layout = fakeLayout({ scrollTop: VIEW_TOP })
          const { virt, dispose } = mount({
            scroller: layout.scroller,
            premeasuredIds: [ROW_10],
          })
          scrollTo(layout, virt, VIEW_TOP)
          mountWaitingRow10(virt)
          // The placement leaves the row above the view, so the reader reaches it from below.
          layout.state.scrollTop = 1350
          virt.notePlacement(1350)
          scrollTo(layout, virt, VIEW_TOP)

          virt.measure(ROW_10, 700)

          expect(virt.layoutShift()).toBe(-600)
          dispose()
        })

        it('records no row that is not mounted', () => {
          const layout = fakeLayout({ scrollTop: 1400 })
          const { virt, dispose } = mount({
            scroller: layout.scroller,
            premeasuredIds: [ROW_10],
          })
          // Row 10 lies above the view at the placement, but it mounts later, inside the view.
          virt.notePlacement(1400)
          scrollTo(layout, virt, VIEW_TOP)
          mountWaitingRow10(virt)

          virt.measure(ROW_10, 700)

          expect(virt.layoutShift()).toBe(0)
          dispose()
        })
      })
    })

    it('holds the row at the viewport top when a premeasured row above it reads another height', () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, dispose } = mount({
        scroller,
        premeasuredIds: ['m4'],
      })

      mountRow(virt, 'm4', 360, 1250, 160)

      expect(virt.offsetOfId(ROW_10)! + virt.layoutShift()).toBe(1200)
      expect(virt.layoutShift()).toBe(-60)
      dispose()
    })
  })

  it('requests no pivot frame while pinBottom is true, and requests one again once it is false', () => {
    const frames = manualFrames()
    let pinned = true
    const { scroller } = fakeLayout({ scrollTop: 1880 })
    const { virt, setList, dispose } = mount({
      scroller,
      pinBottom: () => pinned,
    })

    virt.updateViewport(1880, 500)
    setList(plainItems(21))
    expect(frames.pendingFrames()).toBe(0)

    pinned = false
    virt.updateViewport(1880, 500)
    expect(frames.pendingFrames()).toBe(1)
    dispose()
  })

  it('reads no pivot record from before a commit that pinBottom held', () => {
    const frames = manualFrames()
    let pinned = false
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({
      scroller,
      pinBottom: () => pinned,
    })
    const row = mountRow10(virt, 1250)
    virt.updateViewport(1250, 500)
    frames.runFrames()

    // A commit in follow mode holds the content bottom: row 10 grows by 30px above the
    // child. The rectangle height of the row stays at the last read of the measurer, as
    // after a ResizeObserver delivery, so a frame finds no uncommitted height.
    pinned = true
    row.childTop = 50
    virt.measure(ROW_10, 130)
    expect(virt.layoutShift()).toBe(-30)
    frames.runFrames()

    // Follow mode ends with no scroll, and the next commit holds the viewport top. Follow
    // mode keeps no record, so the content at the viewport top keeps its distance from the
    // row top: the shift stays -30. A record from before follow mode would add the 30px move
    // that the screen already showed, and give -70.
    pinned = false
    row.childTop = 60
    virt.measure(ROW_10, 140)
    expect(virt.layoutShift()).toBe(-30)
    dispose()
  })

  it('records the pivot element once a fast scroll stops, before the fast scroll ends', () => {
    const frames = manualFrames()
    const { scroller } = fakeLayout({ scrollTop: 1200 })
    const { virt, dispose } = mount({ scroller })
    const row = mountRow10(virt, 1250)
    // A fast scroll moves the view to 1250. Its frame sees a new position, so it drops the
    // record and asks for one more frame.
    virt.setFastScrollActive(true)
    scroller.scrollTop = 1250
    virt.updateViewport(1250, 500)
    frames.runFrames()

    // The scroll stopped at 1250, and the fast scroll has not ended: the next frame sees
    // the same position and records.
    frames.runFrames()
    row.childTop = 50
    virt.measure(ROW_10, 130)

    expect(virt.layoutShift()).toBe(-30)
    dispose()
  })

  it('takes the displacement at each step of a repeated resize', () => {
    const frames = manualFrames()
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller })
    const row = mountRow10(virt, 1250)
    virt.updateViewport(1250, 500)
    frames.runFrames()

    // The direct `measure` call leaves the last read of the measurer at 100, so the measurer
    // reports an uncommitted height in both frames, as it does in a browser before the
    // ResizeObserver delivery.
    row.childTop = 50
    row.rowHeight = 130
    virt.updateViewport(1250, 500)
    frames.runFrames()
    virt.measure(ROW_10, 130)
    expect(virt.layoutShift()).toBe(-30)

    row.childTop = 90
    row.rowHeight = 170
    frames.runFrames()
    virt.measure(ROW_10, 170)
    expect(virt.layoutShift()).toBe(-70)
    dispose()
  })

  it('removes the shift on rebase', () => {
    const layout = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    virt.measure('m4', 160)
    expect(virt.layoutShift()).toBe(-60)

    expect(virt.rebase()).toBe(-60)
    expect(virt.layoutShift()).toBe(0)
    expect(layout.scroller.scrollTop).toBe(1310)
    layout.writes.length = 0
    expect(virt.rebase()).toBe(0)
    expect(layout.writes).toEqual([])
    dispose()
  })

  it('writes the scroll position first for a positive shift', () => {
    const layout = fakeLayout({ clientHeight: 0, scrollTop: 0 })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    // Row 3 measures 300px while the pane is hidden, so no shift comes of it.
    virt.measure('m4', 300)
    expect(virt.layoutShift()).toBe(0)
    layout.state.clientHeight = 500
    layout.state.spacerHeight = `${virt.spacerHeight()}px`
    layout.scroller.scrollTop = 99_999
    expect(layout.scroller.scrollTop).toBe(2080)

    virt.measure('m4', 100)
    expect(virt.layoutShift()).toBe(200)
    layout.writes.length = 0

    expect(virt.rebase()).toBe(200)
    expect(layout.writes).toEqual(['scrollTop', 'spacer', 'layer'])
    expect(layout.scroller.scrollTop).toBe(1880)
    expect(layout.state.spacerHeight).toBe('2380px')
    expect(layout.state.layerTransform).toBe('translateY(0px)')
    dispose()
  })

  it('writes the layout first for a negative shift', () => {
    const layout = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    virt.measure('m1', 300)
    expect(virt.layoutShift()).toBe(-200)
    layout.state.spacerHeight = `${virt.spacerHeight()}px`
    layout.scroller.scrollTop = 0
    layout.writes.length = 0

    expect(virt.rebase()).toBe(-200)
    expect(layout.writes).toEqual(['spacer', 'layer', 'scrollTop'])
    expect(layout.scroller.scrollTop).toBe(200)
    expect(layout.state.spacerHeight).toBe('2580px')
    dispose()
  })

  it('rebases without attached layout elements', () => {
    const layout = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.measure('m1', 300)
    expect(virt.layoutShift()).toBe(-200)
    // The scroll range holds the target with no spacer: the content stands for it.
    layout.state.scrollHeight = 2580

    expect(virt.rebase()).toBe(-200)
    expect(layout.scroller.scrollTop).toBe(1450)
    expect(virt.layoutShift()).toBe(0)
    dispose()
  })

  it.each(['hides', 'detaches'] as const)('writes nothing and keeps the shift once the scroll element %s', (how) => {
    const layout = leavingLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scrollElement: layout.scrollElement })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    virt.measure('m4', 160)
    expect(virt.layoutShift()).toBe(-60)
    layout.leave(how)
    layout.writes.length = 0

    expect(virt.rebase()).toBe(0)
    expect(layout.writes).toEqual([])
    // The rows keep their place: a shift of 0 with no `scrollTop` write would move them.
    expect(virt.layoutShift()).toBe(-60)
    dispose()
  })

  it('subscribes an effect that rebases to no signal', () => {
    const layout = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    virt.measure('m4', 160)
    let runs = 0
    const disposeEffect = createRoot((d) => {
      createRenderEffect(() => {
        runs += 1
        virt.rebase()
      })
      return d
    })
    expect(runs).toBe(1)
    expect(virt.layoutShift()).toBe(0)

    // A new shift and a new total height. An effect that read either one would run again,
    // and its rebase would remove the new shift.
    virt.measure('m5', 130)

    expect(virt.layoutShift()).toBe(-30)
    expect(runs).toBe(1)
    disposeEffect()
    dispose()
  })

  /**
   * The distance (px) from the viewport top to the top of row 10: its scroll coordinate
   * minus `scrollTop`. It stays the same while the content stays in place on screen.
   */
  function row10OnScreen(virt: ReturnType<typeof setup>['virt'], scroller: HTMLElement): number {
    return virt.scrollTopOf(virt.offsetOfId(ROW_10)!) - scroller.scrollTop
  }

  it('keeps the content in place when the browser rounds the written position', () => {
    const layout = fakeLayout({ scrollTop: 1250, roundScrollTop: true })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    virt.measure('m4', 160.37)
    expect(row10OnScreen(virt, layout.scroller)).toBeCloseTo(-50, 9)

    // The browser stores the target 1310.37 as 1310, so the shift keeps the 0.37px that the
    // write could not remove.
    expect(virt.rebase()).toBeCloseTo(-60, 9)

    expect(layout.scroller.scrollTop).toBe(1310)
    expect(virt.layoutShift()).toBeCloseTo(-0.37, 9)
    expect(row10OnScreen(virt, layout.scroller)).toBeCloseTo(-50, 9)
    expect(layout.state.layerTransform).toBe(`translateY(${virt.layoutShift()}px)`)
    expect(layout.state.spacerHeight).toBe(`${virt.spacerHeight()}px`)
    dispose()
  })

  it('keeps the content in place across consecutive rebases', () => {
    const layout = fakeLayout({ scrollTop: 1250, roundScrollTop: true })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)

    // Each row above row 10 changes by more than one pixel plus a fraction, so each rebase
    // writes, and the browser rounds each target.
    for (const [id, height] of [
      ['m2', 101.37],
      ['m3', 102.74],
      ['m4', 98.39],
      ['m5', 103.29],
      ['m6', 101.83],
    ] as const) {
      virt.measure(id, height)
      expect(virt.rebase()).not.toBe(0)
      expect(Number.isInteger(layout.scroller.scrollTop)).toBe(true)
      expect(Math.abs(virt.layoutShift())).toBeLessThan(1)
      expect(row10OnScreen(virt, layout.scroller)).toBeCloseTo(-50, 9)
    }
    dispose()
  })

  it('leaves a shift below one pixel in place', () => {
    const layout = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    virt.measure('m4', 100.6)
    expect(virt.layoutShift()).toBeCloseTo(-0.6, 9)
    layout.writes.length = 0

    expect(virt.rebase()).toBe(0)

    expect(layout.writes).toEqual([])
    expect(virt.layoutShift()).toBeCloseTo(-0.6, 9)
    dispose()
  })

  it('drops a remainder of a clamp', () => {
    const layout = fakeLayout({ clientHeight: 0, scrollTop: 0 })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    // Row 3 measures 300px while the pane is hidden, so no shift comes of it. At the bottom,
    // its shrink back to 100px gives a shift of 200.
    virt.measure('m4', 300)
    layout.state.clientHeight = 500
    layout.state.spacerHeight = `${virt.spacerHeight()}px`
    layout.scroller.scrollTop = 99_999
    virt.measure('m4', 100)
    expect(virt.layoutShift()).toBe(200)
    // The reader then scrolls into the blank band above the first row. The target -100 lies
    // outside the scroll range, so the browser clamps it to 0.
    layout.scroller.scrollTop = 100

    expect(virt.rebase()).toBe(200)

    expect(layout.scroller.scrollTop).toBe(0)
    expect(virt.layoutShift()).toBe(0)
    expect(layout.state.spacerHeight).toBe('2380px')
    expect(layout.state.layerTransform).toBe('translateY(0px)')
    dispose()
  })

  it('converts scroll coordinates', () => {
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller })
    virt.measure('m4', 200)
    expect(virt.layoutShift()).toBe(-100)
    // The same geometry with no scroll element, so its shift is 0.
    const reference = mount()
    reference.virt.measure('m4', 200)

    expect(virt.contentTopOf(500)).toBe(600)
    expect(virt.scrollTopOf(600)).toBe(500)
    const anchor = virt.anchorAt(500)
    expect(anchor).toEqual(reference.virt.anchorAt(600))
    expect(virt.scrollTopForAnchor(anchor!)).toBe(reference.virt.scrollTopForAnchor(anchor!)! - 100)
    expect(virt.scrollTopNearAnchor(anchor!)).toBe(reference.virt.scrollTopNearAnchor(anchor!)! - 100)
    expect(virt.computeRange(500, 500)).toEqual(reference.virt.computeRange(600, 500))
    reference.dispose()
    dispose()
  })

  it('converts scroll coordinates across the inset of the spacer', () => {
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller, spacerInsetPx: 28 })
    virt.measure('m4', 200)
    const shift = virt.layoutShift()
    expect(shift).toBe(-100)

    expect(virt.contentTopOf(1250)).toBe(1222 - shift)
    expect(virt.scrollTopOf(1222 - shift)).toBe(1250)
    // The anchor of the viewport top lies in row 10 (`m11`), at content 1322.
    expect(virt.anchorAt(1250)).toMatchObject({ id: ROW_10, offsetWithinRow: 22 })
    // Scroll 1210 is content 1282, in the gap below row 9, which spans 1180 to 1280 after row
    // 3 grows. So the slice starts at row 9. Without the inset, content 1310 lies in row 10.
    const reference = mount()
    reference.virt.measure('m4', 200)
    expect(virt.computeRange(1210, 500)).toEqual(reference.virt.computeRange(1282, 500))
    expect(virt.computeRange(1210, 500).start).toBe(9)
    reference.dispose()
    dispose()
  })

  it('places the top of the first row at the top of the scroller, above the inset of the spacer', () => {
    const { scroller } = fakeLayout({ scrollTop: 0 })
    const { virt, dispose } = mount({ scroller, spacerInsetPx: 28 })
    expect(virt.layoutShift()).toBe(0)

    // A reader at the top of the history sees the inset, which no anchor captures: the anchor
    // floors it to the top of the first row. That anchor resolves back to the top of the
    // scroller, not to the top of the row 28px below it.
    const atTop = virt.anchorAt(0)
    expect(atTop).toMatchObject({ id: 'm1', offsetWithinRow: 0 })
    expect(virt.scrollTopForAnchor(atTop!)).toBe(0)
    expect(virt.scrollTopNearAnchor(atTop!)).toBe(0)
    // A seek lands its row 8px below the viewport top, which lies above the content top for
    // the first row; the scrub's top end is the content top itself.
    expect(virt.placementScrollTopOf(-8)).toBe(0)
    expect(virt.placementScrollTopOf(0)).toBe(0)
    // Below the content top, a placement converts exactly.
    expect(virt.placementScrollTopOf(12)).toBe(40)
    expect(virt.scrollTopForAnchor({ id: 'm1', offsetWithinRow: 12 })).toBe(40)
    expect(virt.scrollTopForAnchor({ id: 'm2', offsetWithinRow: 0 })).toBe(virt.scrollTopOf(virt.offsetOfId('m2')!))
    dispose()
  })

  it('places the top of the first row of the list at its own place while loaded rows wait above the list', () => {
    const { scroller } = fakeLayout({ scrollTop: 0 })
    const [startsAtLoadedTop, setStartsAtLoadedTop] = createSignal(false)
    const { virt, dispose } = mount({ scroller, spacerInsetPx: 28, listStartsAtLoadedTop: startsAtLoadedTop })
    expect(virt.layoutShift()).toBe(0)

    // The first row of the list is not the first loaded row: the rows above it wait for
    // their measure. The inset above the list is then not the top of the history, so a
    // placement at the content top converts exactly, as it does for every other row.
    expect(virt.placementScrollTopOf(0)).toBe(28)
    // A seek lands its row 8px below the viewport top.
    expect(virt.placementScrollTopOf(-8)).toBe(20)
    expect(virt.scrollTopForAnchor({ id: 'm1', offsetWithinRow: 0 })).toBe(28)
    expect(virt.scrollTopNearAnchor({ id: 'm1', offsetWithinRow: 0 })).toBe(28)
    expect(virt.placementScrollTopOf(12)).toBe(40)

    // The rows above join: the first row of the list is the first loaded row again.
    setStartsAtLoadedTop(true)
    expect(virt.placementScrollTopOf(0)).toBe(0)
    expect(virt.scrollTopForAnchor({ id: 'm1', offsetWithinRow: 0 })).toBe(0)
    dispose()
  })

  it('resolves anchors in content coordinates with no conversion', () => {
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller, spacerInsetPx: 28 })
    virt.measure('m4', 200.37)
    expect(virt.layoutShift()).toBeCloseTo(-100.37, 9)
    // The same geometry with no scroll element, so its scroll coordinates are content ones.
    const reference = mount()
    reference.virt.measure('m4', 200.37)

    for (const contentTop of [0, 499.99, 1234.56, 1900]) {
      const anchor = virt.anchorAtContent(contentTop)
      expect(anchor).not.toBeNull()
      expect(anchor).toEqual(reference.virt.anchorAt(contentTop))
      expect(virt.contentTopForAnchor(anchor!)).toBe(reference.virt.scrollTopForAnchor(anchor!))
      // The scroll resolvers agree through the conversions. A resolved anchor is a placement,
      // so it converts as placementScrollTopOf does: the content top resolves to the top of
      // the scroller (see "places the top of the first row at the top of the scroller").
      const viaScroll = virt.anchorAt(virt.scrollTopOf(contentTop))
      expect(viaScroll?.id).toBe(anchor!.id)
      expect(viaScroll?.offsetWithinRow).toBeCloseTo(anchor!.offsetWithinRow, 9)
      expect(virt.scrollTopForAnchor(anchor!)).toBeCloseTo(virt.placementScrollTopOf(virt.contentTopForAnchor(anchor!)!), 9)
    }
    expect(virt.contentTopForAnchor({ id: 'gone', offsetWithinRow: 0 })).toBeNull()
    reference.dispose()
    dispose()
  })

  it('holds the content at the viewport top below the inset of the spacer', () => {
    // With the inset 28, scroll 1188 is content 1160, inside the body of row 9 (1080 to
    // 1180). Without the inset, 1188 would lie in the gap below row 9, where the rule holds
    // the bottom edge of the row.
    const { scroller } = fakeLayout({ scrollTop: 1188 })
    const { virt, dispose } = mount({ scroller, spacerInsetPx: 28 })

    // Row 9 grows at its bottom, below the viewport top, so the content at the viewport top
    // keeps its place in the row, and nothing above it moves.
    virt.measure('m10', 130)

    expect(virt.layoutShift()).toBe(0)
    dispose()
  })

  it('records the pivot element in the row that the viewport top cuts below the inset of the spacer', () => {
    const frames = manualFrames()
    // With the inset 28, scroll 1188 is content 1160, inside the body of row 9 (`m10`, 1080
    // to 1180), whose top is at scroll 1108. Without the inset, 1188 would lie in the gap
    // below row 9, where no pivot element is recorded.
    const { scroller } = fakeLayout({ scrollTop: 1188 })
    const { virt, dispose } = mount({ scroller, spacerInsetPx: 28 })
    const row = mountRow(virt, 'm10', 1108, 1188)
    virt.updateViewport(1188, 500)
    frames.runFrames()

    // Row 9 grows by 30px above the child, so the content at the viewport top moves 30px
    // down inside the row, and the rows move up by 30px to keep it in place.
    row.childTop = 50
    virt.measure('m10', 130)

    expect(virt.layoutShift()).toBe(-30)
    dispose()
  })

  it('follows a change of the inset of the spacer that an accessor gives', () => {
    // ChatView measures the inset after its first layout, so the inset changes after the
    // mount. Scroll 1188 lies in the gap below row 9 with the inset 0, and inside its body
    // with the inset 28.
    const { scroller } = fakeLayout({ scrollTop: 1188 })
    const [inset, setInset] = createSignal(0)
    const { virt, dispose } = mount({ scroller, spacerInsetPx: inset })
    expect(virt.contentTopOf(1188)).toBe(1188)

    setInset(28)

    expect(virt.contentTopOf(1188)).toBe(1160)
    expect(virt.scrollTopOf(1160)).toBe(1188)
    expect(virt.placementScrollTopOf(12)).toBe(40)
    // The shift rule reads the new inset too: row 9 grows below the viewport top, inside
    // its body, so nothing moves. With the inset 0, the rule would hold the bottom edge of
    // the row and move the rows up by 30px.
    virt.measure('m10', 130)
    expect(virt.layoutShift()).toBe(0)
    dispose()
  })

  it('changes the shift in the same update as the offsets', () => {
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const records: Array<[number | undefined, number]> = []
    const { virt, dispose } = buildInRoot(() => {
      const mounted = setup(plainItems(20), {
        scrollElement: () => scroller,
      })
      for (let i = 1; i <= 20; i++)
        mounted.virt.measure(`m${i}`, 100)
      createRenderEffect(() => {
        records.push([
          mounted.virt.offsetOfId(ROW_10),
          mounted.virt.layoutShift(),
        ])
      })
      return mounted
    })
    expect(records).toEqual([
      [1200, 0],
    ])

    virt.measure('m4', 160)
    virt.measure('m5', 130)

    expect(records).toEqual([
      [1200, 0],
      [1260, -60],
      [1290, -90],
    ])
    dispose()
  })
})
