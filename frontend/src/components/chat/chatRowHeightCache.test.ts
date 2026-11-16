import type { ChatRowItem } from './chatRowItem'
import { createMemo, createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createRowHeightCache, HEIGHT_CACHE_EVICTION_BATCH, HEIGHT_CACHE_MAX } from './chatRowHeightCache'

/** A row with the height key `key`. */
function row(id: string, key = 'a'): ChatRowItem {
  return {
    id,
    hasSpanLines: false,
    heightKey: key,
  }
}

/** A cache with no loaded row, so its eviction protects the mounted rows only. */
function newCache() {
  return createRowHeightCache({
    loadedIds: () => [],
  })
}

/** The microtask of an unmount notice (see RowHeightCache.noteDetached). */
const unmountSettled = (): Promise<void> => Promise.resolve()

describe('createRowHeightCache', () => {
  it('commits a height under the current key of the row', () => {
    const cache = newCache()
    const before = cache.version()

    expect(cache.commit(row('m1'), 120, 'a', 'visible')).toBe('moved')

    expect(cache.heightOf(row('m1'))).toBe(120)
    expect(cache.version()).not.toBe(before)
  })

  it('refuses a height that was measured under another key', () => {
    const cache = newCache()
    const before = cache.version()

    expect(cache.commit(row('m1'), 120, 'other', 'visible')).toBe('none')

    expect(cache.heightOf(row('m1'))).toBeUndefined()
    expect(cache.version()).toBe(before)
  })

  it.each([
    Number.NaN,
    -1,
    Number.POSITIVE_INFINITY,
  ])('refuses the height %s, which cannot enter the offset map', (height) => {
    const cache = newCache()

    expect(cache.commit(row('m1'), height, 'a', 'visible')).toBe('none')

    expect(cache.heightOf(row('m1'))).toBeUndefined()
  })

  it('commits a height of 0', () => {
    const cache = newCache()

    expect(cache.commit(row('m1'), 0, 'a', 'visible')).toBe('moved')

    expect(cache.heightOf(row('m1'))).toBe(0)
  })

  it('keeps the height for a second commit within the epsilon', () => {
    const cache = newCache()
    cache.commit(row('m1'), 120, 'a', 'visible')
    const before = cache.version()

    expect(cache.commit(row('m1'), 120.3, 'a', 'visible')).toBe('none')

    expect(cache.heightOf(row('m1'))).toBe(120)
    expect(cache.version()).toBe(before)
  })

  it('gives no height for a row that is not mounted when its key changes', () => {
    const cache = newCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')

    expect(cache.heightOf(row('m1', 'b'))).toBeUndefined()
    expect(cache.isMeasured(row('m1', 'b'))).toBe(false)
    expect(cache.heightUnderCurrentKey(row('m1', 'b'))).toBeUndefined()
  })

  it('gives the height again when the row returns to the key of the measure', () => {
    const cache = newCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    // The row took another key in between. Nothing dropped the height of the key `a`.
    expect(cache.heightOf(row('m1', 'b'))).toBeUndefined()

    expect(cache.heightOf(row('m1', 'a'))).toBe(120)
    expect(cache.isMeasured(row('m1', 'a'))).toBe(true)
    expect(cache.heightUnderCurrentKey(row('m1', 'a'))).toBe(120)
  })

  it('keeps the height of a mounted row provisionally when its key changes', () => {
    const cache = newCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    cache.noteMounted('m1')

    // The element of the row still shows that height, until its next read. The row
    // counts as measured, although no measure produced a height under its current key.
    expect(cache.heightOf(row('m1', 'b'))).toBe(120)
    expect(cache.isMeasured(row('m1', 'b'))).toBe(true)
    expect(cache.heightUnderCurrentKey(row('m1', 'b'))).toBeUndefined()
  })

  it('makes a reactive reader of the measured test compute again at a commit', () => {
    const cache = newCache()
    createRoot((dispose) => {
      let runs = 0
      // A memo computes again only when a signal that it read changed.
      const measured = createMemo(() => {
        runs++
        return cache.isMeasured(row('m1'))
      })
      expect(measured()).toBe(false)

      cache.commit(row('m1'), 120, 'a', 'premeasure')

      expect(measured()).toBe(true)
      expect(runs).toBe(2)
      dispose()
    })
  })

  it('confirms a provisional height when a read gives the same height under the new key', () => {
    const cache = newCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    cache.noteMounted('m1')
    const before = cache.version()

    expect(cache.commit(row('m1', 'b'), 120, 'b', 'visible')).toBe('confirmed')

    expect(cache.heightUnderCurrentKey(row('m1', 'b'))).toBe(120)
    expect(cache.snapshot()).toEqual([
      { id: 'm1', heightKey: 'b', height: 120 },
    ])
    // The confirmation changes the snapshot, so it changes the version.
    expect(cache.version()).not.toBe(before)
  })

  it('corrects a provisional height when a read gives another height under the new key', () => {
    const cache = newCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    cache.noteMounted('m1')

    expect(cache.commit(row('m1', 'b'), 180, 'b', 'visible')).toBe('moved')

    expect(cache.heightOf(row('m1', 'b'))).toBe(180)
  })

  describe('the mounted rows', () => {
    it('lists a row from its mount notice to its detach notice', () => {
      const cache = newCache()

      cache.noteMounted('m1')
      expect(cache.mountedIds.has('m1')).toBe(true)
      expect(cache.isMounted('m1')).toBe(true)

      cache.noteDetached('m1')
      expect(cache.mountedIds.has('m1')).toBe(false)
    })

    it('counts a row whose last element detached as mounted for one microtask', async () => {
      const cache = newCache()
      cache.noteMounted('m1')

      cache.noteDetached('m1')
      expect(cache.isMounted('m1')).toBe(true)

      await unmountSettled()
      expect(cache.isMounted('m1')).toBe(false)
    })

    it('tells whether it answers a provisional height for a row, and records no answer', async () => {
      const cache = newCache()
      cache.commit(row('m1', 'a'), 120, 'a', 'premeasure')
      cache.noteMounted('m1')

      // The height is of the current key, so it is no provisional height.
      expect(cache.hasProvisionalHeight(row('m1', 'a'))).toBe(false)
      expect(cache.hasProvisionalHeight(row('m1', 'b'))).toBe(true)
      expect(cache.hasProvisionalHeight(row('absent', 'b'))).toBe(false)

      const before = cache.version()
      cache.noteDetached('m1')
      // The row still counts as mounted for one microtask.
      expect(cache.hasProvisionalHeight(row('m1', 'b'))).toBe(true)
      await unmountSettled()

      expect(cache.hasProvisionalHeight(row('m1', 'b'))).toBe(false)
      // The question recorded no answer, so the unmount changed no version.
      expect(cache.version()).toBe(before)
    })

    it('stops answering for a row with a provisional height when the row unmounts', async () => {
      const cache = newCache()
      cache.commit(row('m1', 'a'), 120, 'a', 'visible')
      cache.noteMounted('m1')
      expect(cache.heightOf(row('m1', 'b'))).toBe(120)
      const before = cache.version()

      cache.noteDetached('m1')
      await unmountSettled()

      expect(cache.version()).not.toBe(before)
      expect(cache.heightOf(row('m1', 'b'))).toBeUndefined()
      // It deleted nothing: the height counts again under its own key.
      expect(cache.heightOf(row('m1', 'a'))).toBe(120)
    })

    it('answers for a row whose last element detached until a new element takes the row over, and changes no version', async () => {
      const cache = newCache()
      cache.commit(row('m1', 'a'), 120, 'a', 'visible')
      cache.noteMounted('m1')
      const before = cache.version()
      const unmounted = vi.fn()

      // The old element of the row detaches. A new element can still take the row over.
      cache.noteDetached('m1', unmounted)

      expect(cache.heightOf(row('m1', 'b'))).toBe(120)
      // The hidden copy of a row that an element can still take over is refused, as for a
      // mounted row.
      expect(cache.commit(row('m1', 'b'), 90, 'b', 'premeasure')).toBe('none')

      // A new element took the row over.
      cache.noteMounted('m1')
      await unmountSettled()

      expect(cache.heightOf(row('m1', 'b'))).toBe(120)
      expect(cache.version()).toBe(before)
      expect(unmounted).not.toHaveBeenCalled()
    })

    it('tells the caller of the unmount of a row that no element took over, after the microtask', async () => {
      const cache = newCache()
      cache.noteMounted('m1')
      const unmounted = vi.fn()

      cache.noteDetached('m1', unmounted)
      expect(unmounted).not.toHaveBeenCalled()

      await unmountSettled()
      expect(unmounted).toHaveBeenCalledTimes(1)
    })

    it('keeps a row mounted until the microtask of its last detach, and tells of one unmount', async () => {
      const cache = newCache()
      cache.commit(row('m1', 'a'), 120, 'a', 'visible')
      const unmounted = vi.fn()
      // Two elements took the row in turn, and both detached before a microtask ran.
      cache.noteMounted('m1')
      cache.noteDetached('m1', unmounted)
      cache.noteMounted('m1')
      cache.noteDetached('m1', unmounted)
      const afterFirst = vi.fn(() => cache.heightOf(row('m1', 'b')))
      // This microtask runs between the microtasks of the two detach notices.
      const between = new Promise<void>((resolve) => {
        queueMicrotask(() => {
          afterFirst()
          resolve()
        })
      })

      expect(cache.heightOf(row('m1', 'b'))).toBe(120)
      await between
      await unmountSettled()

      expect(cache.heightOf(row('m1', 'b'))).toBeUndefined()
      expect(unmounted).toHaveBeenCalledTimes(1)
    })

    it('answers nothing for a detached row that it holds no height for', async () => {
      const cache = newCache()
      cache.noteMounted('m1')
      const before = cache.version()

      cache.noteDetached('m1')

      expect(cache.heightOf(row('m1'))).toBeUndefined()
      await unmountSettled()
      expect(cache.version()).toBe(before)
    })

    it('keeps the version when a row with a current height unmounts', async () => {
      const cache = newCache()
      cache.commit(row('m1', 'a'), 120, 'a', 'visible')
      cache.noteMounted('m1')
      expect(cache.heightOf(row('m1', 'a'))).toBe(120)
      const before = cache.version()

      cache.noteDetached('m1')
      await unmountSettled()

      expect(cache.heightOf(row('m1', 'a'))).toBe(120)
      expect(cache.version()).toBe(before)
    })

    it('keeps the version when a row whose provisional height no reader asked for unmounts', async () => {
      const cache = newCache()
      cache.commit(row('m1', 'a'), 120, 'a', 'visible')
      cache.noteMounted('m1')
      const before = cache.version()

      // The key of the row changed, but nothing read its height under the new key.
      cache.noteDetached('m1')
      await unmountSettled()

      expect(cache.version()).toBe(before)
    })

    it('keeps the version at the unmount of a row whose provisional height a commit replaced', async () => {
      const cache = newCache()
      cache.commit(row('m1', 'a'), 120, 'a', 'visible')
      cache.noteMounted('m1')
      expect(cache.heightOf(row('m1', 'b'))).toBe(120)
      // The read of the row under its new key: no answer is provisional any more.
      expect(cache.commit(row('m1', 'b'), 120, 'b', 'visible')).toBe('confirmed')
      const before = cache.version()

      cache.noteDetached('m1')
      await unmountSettled()

      expect(cache.heightOf(row('m1', 'b'))).toBe(120)
      expect(cache.version()).toBe(before)
    })

    it('keeps the version when a row that it does not hold unmounts', async () => {
      const cache = newCache()
      const before = cache.version()

      cache.noteDetached('absent')
      await unmountSettled()

      expect(cache.version()).toBe(before)
    })
  })

  it('gives each entry of the snapshot with the key of its measure', () => {
    const cache = newCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    // The list now holds the row under the key `b`. The entry keeps the key `a`.
    expect(cache.heightOf(row('m1', 'b'))).toBeUndefined()

    expect(cache.snapshot()).toEqual([
      { id: 'm1', heightKey: 'a', height: 120 },
    ])
  })

  it('gives the snapshot in LRU order, the least recently measured row first', () => {
    const cache = newCache()
    cache.commit(row('m1'), 100, 'a', 'visible')
    cache.commit(row('m2'), 110, 'a', 'visible')
    // A new measure of m1 makes it the most recently measured row.
    cache.commit(row('m1'), 120, 'a', 'visible')

    expect(cache.snapshot()).toEqual([
      { id: 'm2', heightKey: 'a', height: 110 },
      { id: 'm1', heightKey: 'a', height: 120 },
    ])
  })

  it('leaves the evicted rows out of the snapshot', () => {
    const cache = newCache()
    for (let i = 0; i <= HEIGHT_CACHE_MAX; i++)
      cache.commit(row(`m${i}`), 100, 'a', 'visible')

    const saved = cache.snapshot()

    // The commit past the cap evicted the least recently measured rows, down to one
    // batch below the cap, and the order holds.
    expect(saved).toHaveLength(HEIGHT_CACHE_MAX - HEIGHT_CACHE_EVICTION_BATCH)
    expect(saved[0]).toEqual({ id: `m${HEIGHT_CACHE_EVICTION_BATCH + 1}`, heightKey: 'a', height: 100 })
    expect(saved.at(-1)).toEqual({ id: `m${HEIGHT_CACHE_MAX}`, heightKey: 'a', height: 100 })
  })

  it('evicts once for a batch of commits past the cap', () => {
    const loadedIds = vi.fn((): string[] => [])
    const cache = createRowHeightCache({ loadedIds })
    for (let i = 0; i <= HEIGHT_CACHE_MAX; i++)
      cache.commit(row(`m${i}`), 100, 'a', 'visible')
    // The eviction asks for the loaded rows, so the calls count the evictions.
    expect(loadedIds).toHaveBeenCalledTimes(1)

    // The next commits fill the room that the eviction freed.
    for (let i = 1; i <= HEIGHT_CACHE_EVICTION_BATCH; i++)
      cache.commit(row(`n${i}`), 100, 'a', 'visible')
    expect(loadedIds).toHaveBeenCalledTimes(1)
    expect(cache.snapshot()).toHaveLength(HEIGHT_CACHE_MAX)

    cache.commit(row('past'), 100, 'a', 'visible')
    expect(loadedIds).toHaveBeenCalledTimes(2)
  })

  it('refuses the height of a hidden copy for a mounted row that has a height', () => {
    const cache = newCache()
    cache.noteMounted('m1')
    cache.commit(row('m1'), 120, 'a', 'visible')

    expect(cache.commit(row('m1'), 90, 'a', 'premeasure')).toBe('none')

    expect(cache.heightOf(row('m1'))).toBe(120)
  })

  it('commits the height of a hidden copy for a row that is not mounted', () => {
    const cache = newCache()

    expect(cache.commit(row('m1'), 90, 'a', 'premeasure')).toBe('moved')

    expect(cache.heightOf(row('m1'))).toBe(90)
  })

  it('commits the height of a hidden copy for a mounted row that has no height', () => {
    const cache = newCache()
    cache.noteMounted('m1')

    expect(cache.commit(row('m1'), 90, 'a', 'premeasure')).toBe('moved')
  })

  it('commits several heights with one version change', () => {
    const cache = newCache()
    const versions: number[] = [
      cache.version(),
    ]

    const adopted = cache.commitAll([
      {
        row: row('m1'),
        height: 100,
      },
      // A height that cannot enter the offset map.
      {
        row: row('m2'),
        height: Number.NaN,
      },
      {
        row: row('m3'),
        height: 120,
      },
    ])
    versions.push(cache.version())

    expect(adopted).toBe(2)
    expect(cache.heightOf(row('m1'))).toBe(100)
    expect(cache.heightOf(row('m2'))).toBeUndefined()
    expect(cache.heightOf(row('m3'))).toBe(120)
    expect(versions[1]).toBe(versions[0]! + 1)
  })

  it('keeps the version when a batch adopts nothing', () => {
    const cache = newCache()
    const before = cache.version()

    expect(cache.commitAll([])).toBe(0)
    expect(cache.commitAll([
      // Two heights that cannot enter the offset map.
      {
        row: row('m1'),
        height: Number.NaN,
      },
      {
        row: row('m2'),
        height: -1,
      },
    ])).toBe(0)

    expect(cache.version()).toBe(before)
    expect(cache.heightOf(row('m1'))).toBeUndefined()
    expect(cache.heightOf(row('m2'))).toBeUndefined()
  })

  it('refuses the height of a batch for a mounted row that has a height, and adopts the others', () => {
    const cache = newCache()
    cache.noteMounted('m1')
    cache.commit(row('m1'), 150, 'a', 'visible')

    const adopted = cache.commitAll([
      // The mounted row shows its 150px: its own read is the authority.
      {
        row: row('m1'),
        height: 999,
      },
      {
        row: row('m2'),
        height: 110,
      },
    ])

    expect(adopted).toBe(1)
    expect(cache.heightOf(row('m1'))).toBe(150)
    expect(cache.heightOf(row('m2'))).toBe(110)
  })

  it('evicts the oldest rows past the cap, and keeps a mounted row', () => {
    const cache = newCache()
    cache.noteMounted('m0')
    for (let i = 0; i <= HEIGHT_CACHE_MAX; i++)
      cache.commit(row(`m${i}`), 100, 'a', 'visible')

    expect(cache.heightOf(row('m0'))).toBe(100)
    // The eviction passes over the mounted row, and drops the rows after it.
    expect(cache.heightOf(row('m1'))).toBeUndefined()
    expect(cache.heightOf(row(`m${HEIGHT_CACHE_EVICTION_BATCH + 1}`))).toBeUndefined()
    expect(cache.heightOf(row(`m${HEIGHT_CACHE_EVICTION_BATCH + 2}`))).toBe(100)
    expect(cache.heightOf(row(`m${HEIGHT_CACHE_MAX}`))).toBe(100)
  })

  it('keeps the heights of the loaded rows past the cap', () => {
    const loaded = Array.from({ length: 10 }, (_, i) => `m${i}`)
    const cache = createRowHeightCache({
      loadedIds: () => loaded,
    })
    for (let i = 0; i < HEIGHT_CACHE_MAX + 10; i++)
      cache.commit(row(`m${i}`), 100, 'a', 'visible')

    // The 10 loaded rows are the oldest entries, and the eviction passes over them. It
    // drops the rows after them: one more than a batch, because the commit past the cap
    // added one.
    for (const id of loaded)
      expect(cache.heightOf(row(id))).toBe(100)
    for (let i = 10; i <= 10 + HEIGHT_CACHE_EVICTION_BATCH; i++)
      expect(cache.heightOf(row(`m${i}`))).toBeUndefined()
    expect(cache.heightOf(row(`m${11 + HEIGHT_CACHE_EVICTION_BATCH}`))).toBe(100)
  })

  it('gives the cached height of a row for the debug view, whatever its key', () => {
    const cache = newCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')

    // The list now holds the row under the key `b`.
    expect(cache.heightOf(row('m1', 'b'))).toBeUndefined()
    expect(cache.debugEntry('m1')).toEqual({ key: 'a', height: 120 })
    expect(cache.debugEntry('absent')).toBeUndefined()
  })

  it('records the last commit that changed a height', () => {
    const cache = newCache()
    expect(cache.lastMeasurement()).toBeUndefined()

    cache.commit(row('m1'), 120, 'a', 'premeasure')

    expect(cache.lastMeasurement()).toMatchObject({
      id: 'm1',
      source: 'premeasure',
      previousHeight: undefined,
      newHeight: 120,
      commitSeq: 1,
    })
  })

  it('records the height before as the previous height of a later commit', () => {
    const cache = newCache()
    cache.commit(row('m1'), 120, 'a', 'visible')

    cache.commit(row('m1'), 180, 'a', 'visible')

    expect(cache.lastMeasurement()).toMatchObject({
      id: 'm1',
      source: 'visible',
      previousHeight: 120,
      newHeight: 180,
      commitSeq: 2,
    })
  })

  it('records the provisional height of a mounted row as the previous height', () => {
    const cache = newCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    cache.noteMounted('m1')

    // The element showed 120px under the key `a`, and reads 180px under the key `b`.
    cache.commit(row('m1', 'b'), 180, 'b', 'visible')

    expect(cache.lastMeasurement()).toMatchObject({ previousHeight: 120, newHeight: 180 })
  })

  it('records no previous height for a row that is not mounted and has a height of another key', () => {
    const cache = newCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'premeasure')

    // The cache answered no height for the row under the key `b`.
    cache.commit(row('m1', 'b'), 180, 'b', 'premeasure')

    expect(cache.lastMeasurement()).toMatchObject({ previousHeight: undefined, newHeight: 180 })
  })

  it('records the commit of a row that is in no list, and no commit that only confirms a height', () => {
    const cache = newCache()
    // The cache holds no list: a hidden copy of a staged row commits as any other row.
    cache.commit(row('staged', 'a'), 100, 'a', 'premeasure')
    const recorded = cache.lastMeasurement()
    expect(recorded).toMatchObject({ id: 'staged', source: 'premeasure' })

    // The same height under a new key: the answer for the row changes, and no height does.
    expect(cache.heightOf(row('staged', 'b'))).toBeUndefined()
    expect(cache.commit(row('staged', 'b'), 100, 'b', 'premeasure')).toBe('confirmed')

    expect(cache.heightOf(row('staged', 'b'))).toBe(100)
    expect(cache.lastMeasurement()).toBe(recorded)
  })

  it('keeps the record of the last commit when a commit changes no height', () => {
    const cache = newCache()
    cache.commit(row('m1'), 120, 'a', 'visible')
    const recorded = cache.lastMeasurement()

    // Within the epsilon, of another key, and unusable: none of them changes a height.
    cache.commit(row('m1'), 120.3, 'a', 'visible')
    cache.commit(row('m1'), 300, 'other', 'visible')
    cache.commit(row('m1'), Number.NaN, 'a', 'visible')

    expect(cache.lastMeasurement()).toBe(recorded)
  })
})
