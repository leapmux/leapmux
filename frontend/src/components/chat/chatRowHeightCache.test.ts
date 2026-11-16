import type { VirtualItem } from './useChatVirtualizer'
import { describe, expect, it } from 'vitest'
import { createRowHeightCache, HEIGHT_CACHE_MAX } from './chatRowHeightCache'

/** A row with the height key `key`. */
function row(id: string, key = 'a'): VirtualItem {
  return {
    id,
    hasSpanLines: false,
    heightKey: key,
  }
}

describe('createRowHeightCache', () => {
  it('commits a height under the current key of the row', () => {
    const cache = createRowHeightCache()
    const before = cache.version()

    expect(cache.commit(row('m1'), 120, 'a', 'visible')).toBe('moved')

    expect(cache.heightOf(row('m1'))).toBe(120)
    expect(cache.version()).not.toBe(before)
  })

  it('refuses a height that was measured under another key', () => {
    const cache = createRowHeightCache()
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
    const cache = createRowHeightCache()

    expect(cache.commit(row('m1'), height, 'a', 'visible')).toBe('none')

    expect(cache.heightOf(row('m1'))).toBeUndefined()
  })

  it('commits a height of 0', () => {
    const cache = createRowHeightCache()

    expect(cache.commit(row('m1'), 0, 'a', 'visible')).toBe('moved')

    expect(cache.heightOf(row('m1'))).toBe(0)
  })

  it('keeps the height for a second commit within the epsilon', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1'), 120, 'a', 'visible')
    const before = cache.version()

    expect(cache.commit(row('m1'), 120.3, 'a', 'visible')).toBe('none')

    expect(cache.heightOf(row('m1'))).toBe(120)
    expect(cache.version()).toBe(before)
  })

  it('gives no height for a row that is not mounted when its key changes', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')

    expect(cache.heightOf(row('m1', 'b'))).toBeUndefined()
    expect(cache.isCurrent(row('m1', 'b'))).toBe(false)
  })

  it('gives the height again when the row returns to the key of the measure', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    // The row took another key in between. Nothing dropped the height of the key `a`.
    expect(cache.heightOf(row('m1', 'b'))).toBeUndefined()

    expect(cache.heightOf(row('m1', 'a'))).toBe(120)
    expect(cache.isCurrent(row('m1', 'a'))).toBe(true)
  })

  it('keeps the height of a mounted row provisionally when its key changes', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    cache.mountedIds.add('m1')

    // The element of the row still shows that height, until its next read.
    expect(cache.heightOf(row('m1', 'b'))).toBe(120)
    expect(cache.isCurrent(row('m1', 'b'))).toBe(false)
  })

  it('confirms a provisional height when a read gives the same height under the new key', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    cache.mountedIds.add('m1')
    const before = cache.version()

    expect(cache.commit(row('m1', 'b'), 120, 'b', 'visible')).toBe('confirmed')

    expect(cache.isCurrent(row('m1', 'b'))).toBe(true)
    expect(cache.snapshot()).toEqual([
      { id: 'm1', heightKey: 'b', height: 120 },
    ])
    // The confirmation changes the snapshot, so it changes the version.
    expect(cache.version()).not.toBe(before)
  })

  it('corrects a provisional height when a read gives another height under the new key', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    cache.mountedIds.add('m1')

    expect(cache.commit(row('m1', 'b'), 180, 'b', 'visible')).toBe('moved')

    expect(cache.heightOf(row('m1', 'b'))).toBe(180)
  })

  it('stops answering for a row with a provisional height when the row unmounts', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    cache.mountedIds.add('m1')
    expect(cache.heightOf(row('m1', 'b'))).toBe(120)
    const before = cache.version()

    cache.mountedIds.delete('m1')
    cache.noteUnmounted(row('m1', 'b'))

    expect(cache.version()).not.toBe(before)
    expect(cache.heightOf(row('m1', 'b'))).toBeUndefined()
    // It deleted nothing: the height counts again under its own key.
    expect(cache.heightOf(row('m1', 'a'))).toBe(120)
  })

  it('answers for a row whose last element detached while its hold lasts, and changes no version', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    cache.mountedIds.add('m1')
    const before = cache.version()

    // The old element of the row detaches. A new element can still take the row over.
    cache.mountedIds.delete('m1')
    const endHold = cache.holdDetached('m1')

    expect(cache.heightOf(row('m1', 'b'))).toBe(120)
    // The hidden copy of a row that an element can still take over is refused, as for a
    // mounted row.
    expect(cache.commit(row('m1', 'b'), 90, 'b', 'premeasure')).toBe('none')

    // A new element took the row over.
    cache.mountedIds.add('m1')
    endHold()

    expect(cache.heightOf(row('m1', 'b'))).toBe(120)
    expect(cache.version()).toBe(before)
  })

  it('stops answering for a row that no element took over when its hold ends and its unmount is noted', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    cache.mountedIds.add('m1')
    cache.mountedIds.delete('m1')
    const endHold = cache.holdDetached('m1')
    const before = cache.version()

    endHold()
    cache.noteUnmounted(row('m1', 'b'))

    expect(cache.heightOf(row('m1', 'b'))).toBeUndefined()
    expect(cache.version()).not.toBe(before)
  })

  it('keeps a row held until each hold on it ended, and ignores a second end of one hold', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    const endFirst = cache.holdDetached('m1')
    const endSecond = cache.holdDetached('m1')

    endFirst()
    endFirst()
    expect(cache.heightOf(row('m1', 'b'))).toBe(120)

    endSecond()
    expect(cache.heightOf(row('m1', 'b'))).toBeUndefined()
  })

  it('answers nothing for a held row that it holds no height for', () => {
    const cache = createRowHeightCache()
    const endHold = cache.holdDetached('m1')

    expect(cache.heightOf(row('m1'))).toBeUndefined()
    endHold()
  })

  it('keeps the version when a row with a current height unmounts', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    cache.mountedIds.add('m1')
    const before = cache.version()

    cache.mountedIds.delete('m1')
    cache.noteUnmounted(row('m1', 'a'))

    expect(cache.heightOf(row('m1', 'a'))).toBe(120)
    expect(cache.version()).toBe(before)
  })

  it('keeps the version when a row that it does not hold unmounts', () => {
    const cache = createRowHeightCache()
    const before = cache.version()

    cache.noteUnmounted(row('absent'))

    expect(cache.version()).toBe(before)
  })

  it('gives each entry of the snapshot with the key of its measure', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    // The list now holds the row under the key `b`. The entry keeps the key `a`.
    expect(cache.heightOf(row('m1', 'b'))).toBeUndefined()

    expect(cache.snapshot()).toEqual([
      { id: 'm1', heightKey: 'a', height: 120 },
    ])
  })

  it('gives the snapshot in LRU order, the least recently measured row first', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1'), 100, 'a', 'visible')
    cache.commit(row('m2'), 110, 'a', 'visible')
    // A new measure of m1 makes it the most recently measured row.
    cache.commit(row('m1'), 120, 'a', 'visible')

    expect(cache.snapshot()).toEqual([
      { id: 'm2', heightKey: 'a', height: 110 },
      { id: 'm1', heightKey: 'a', height: 120 },
    ])
  })

  it('leaves an evicted row out of the snapshot', () => {
    const cache = createRowHeightCache()
    for (let i = 0; i <= HEIGHT_CACHE_MAX; i++)
      cache.commit(row(`m${i}`), 100, 'a', 'visible')

    const saved = cache.snapshot()

    expect(saved).toHaveLength(HEIGHT_CACHE_MAX)
    // The eviction dropped the least recently measured row, and the order holds.
    expect(saved[0]).toEqual({ id: 'm1', heightKey: 'a', height: 100 })
    expect(saved.at(-1)).toEqual({ id: `m${HEIGHT_CACHE_MAX}`, heightKey: 'a', height: 100 })
  })

  it('refuses the height of a hidden copy for a mounted row that has a height', () => {
    const cache = createRowHeightCache()
    cache.mountedIds.add('m1')
    cache.commit(row('m1'), 120, 'a', 'visible')

    expect(cache.commit(row('m1'), 90, 'a', 'premeasure')).toBe('none')

    expect(cache.heightOf(row('m1'))).toBe(120)
  })

  it('commits the height of a hidden copy for a row that is not mounted', () => {
    const cache = createRowHeightCache()

    expect(cache.commit(row('m1'), 90, 'a', 'premeasure')).toBe('moved')

    expect(cache.heightOf(row('m1'))).toBe(90)
  })

  it('commits the height of a hidden copy for a mounted row that has no height', () => {
    const cache = createRowHeightCache()
    cache.mountedIds.add('m1')

    expect(cache.commit(row('m1'), 90, 'a', 'premeasure')).toBe('moved')
  })

  it('commits several heights with one version change', () => {
    const cache = createRowHeightCache()
    const versions: number[] = [
      cache.version(),
    ]

    const adopted = cache.commitAll([
      {
        row: row('m1'),
        height: 100,
        measuredKey: 'a',
      },
      {
        row: row('m2'),
        height: 110,
        measuredKey: 'other',
      },
      {
        row: row('m3'),
        height: 120,
        measuredKey: 'a',
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
    const cache = createRowHeightCache()
    const before = cache.version()

    expect(cache.commitAll([])).toBe(0)
    expect(cache.commitAll([
      {
        row: row('m1'),
        height: 100,
        measuredKey: 'other',
      },
      // A height that cannot enter the offset map.
      {
        row: row('m2'),
        height: -1,
        measuredKey: 'a',
      },
    ])).toBe(0)

    expect(cache.version()).toBe(before)
    expect(cache.heightOf(row('m1'))).toBeUndefined()
    expect(cache.heightOf(row('m2'))).toBeUndefined()
  })

  it('refuses the height of a batch for a mounted row that has a height, and adopts the others', () => {
    const cache = createRowHeightCache()
    cache.mountedIds.add('m1')
    cache.commit(row('m1'), 150, 'a', 'visible')

    const adopted = cache.commitAll([
      // The mounted row shows its 150px: its own read is the authority.
      {
        row: row('m1'),
        height: 999,
        measuredKey: 'a',
      },
      {
        row: row('m2'),
        height: 110,
        measuredKey: 'a',
      },
    ])

    expect(adopted).toBe(1)
    expect(cache.heightOf(row('m1'))).toBe(150)
    expect(cache.heightOf(row('m2'))).toBe(110)
  })

  it('evicts the oldest row past the cap, and keeps a mounted row', () => {
    const cache = createRowHeightCache()
    cache.mountedIds.add('m0')
    for (let i = 0; i <= HEIGHT_CACHE_MAX; i++)
      cache.commit(row(`m${i}`), 100, 'a', 'visible')

    expect(cache.heightOf(row('m0'))).toBe(100)
    expect(cache.heightOf(row('m1'))).toBeUndefined()
    expect(cache.heightOf(row(`m${HEIGHT_CACHE_MAX}`))).toBe(100)
  })

  it('keeps the heights of the loaded rows past the cap', () => {
    const loaded = Array.from({ length: 10 }, (_, i) => `m${i}`)
    const cache = createRowHeightCache({
      loadedIds: () => loaded,
    })
    for (let i = 0; i < HEIGHT_CACHE_MAX + 10; i++)
      cache.commit(row(`m${i}`), 100, 'a', 'visible')

    // The 10 loaded rows are the oldest entries, and the eviction passes over them.
    for (const id of loaded)
      expect(cache.heightOf(row(id))).toBe(100)
    for (let i = 10; i < 20; i++)
      expect(cache.heightOf(row(`m${i}`))).toBeUndefined()
    expect(cache.heightOf(row('m20'))).toBe(100)
  })

  it('gives the cached height of a row for the debug view, whatever its key', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')

    // The list now holds the row under the key `b`.
    expect(cache.heightOf(row('m1', 'b'))).toBeUndefined()
    expect(cache.debugEntry('m1')).toEqual({ key: 'a', height: 120 })
    expect(cache.debugEntry('absent')).toBeUndefined()
  })

  it('records the last commit that changed a height', () => {
    const cache = createRowHeightCache()
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
    const cache = createRowHeightCache()
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
    const cache = createRowHeightCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'visible')
    cache.mountedIds.add('m1')

    // The element showed 120px under the key `a`, and reads 180px under the key `b`.
    cache.commit(row('m1', 'b'), 180, 'b', 'visible')

    expect(cache.lastMeasurement()).toMatchObject({ previousHeight: 120, newHeight: 180 })
  })

  it('records no previous height for a row that is not mounted and has a height of another key', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1', 'a'), 120, 'a', 'premeasure')

    // The cache answered no height for the row under the key `b`.
    cache.commit(row('m1', 'b'), 180, 'b', 'premeasure')

    expect(cache.lastMeasurement()).toMatchObject({ previousHeight: undefined, newHeight: 180 })
  })

  it('keeps the record of the last commit when a commit changes no height', () => {
    const cache = createRowHeightCache()
    cache.commit(row('m1'), 120, 'a', 'visible')
    const recorded = cache.lastMeasurement()

    // Within the epsilon, of another key, and unusable: none of them changes a height.
    cache.commit(row('m1'), 120.3, 'a', 'visible')
    cache.commit(row('m1'), 300, 'other', 'visible')
    cache.commit(row('m1'), Number.NaN, 'a', 'visible')

    expect(cache.lastMeasurement()).toBe(recorded)
  })
})
