import type { PersistableRowHeight, RowHeightCache } from './chatRowHeightCache'
import type { ChatRowItem } from './chatRowItem'
import { batch, createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as storage from '~/lib/browserStorage'
import { fnv1a32Hex } from '~/lib/stringDigest'
import { deferred } from '~/test-support/async'
import { useTestStorage } from '~/test-support/persistentStorage'
import { createRowHeightCache } from './chatRowHeightCache'
import { createRowHeightPersistence, PERSISTED_ROW_HEIGHTS_MAX, ROW_HEIGHT_SAVE_DEBOUNCE_MS, STORED_ROW_HEIGHTS_VERSION } from './chatRowHeightPersistence'

// Row heights are on the ASYNCHRONOUS storage tier -- a stored payload is tens
// of KB per chat, so the family is deliberately not mirrored in memory -- which
// means these round-trips need a database to round-trip through.
//
// The fake timers below drive fake-indexeddb's request queue as well as the
// save debounce, so `settle()` advances them rather than awaiting real time.
useTestStorage()
const { localStorageDrop, localStorageLoad, localStorageStore, PREFIX_CHAT_ROW_HEIGHTS } = storage

/**
 * Let the storage queue and the IndexedDB requests behind it run to completion.
 *
 * Under fake timers a bare `await` is not enough: fake-indexeddb schedules its
 * request callbacks on `setImmediate`, which the fake clock owns, so the clock
 * has to be advanced for a read or a write to finish at all.
 */
async function settle(): Promise<void> {
  for (let i = 0; i < 12; i++)
    await vi.advanceTimersByTimeAsync(0)
}

/**
 * Read the stored payload, advancing the fake clock WHILE the read is pending.
 *
 * A bare `await localStorageLoad(...)` deadlocks here: the request it issues
 * needs the clock to move to complete, and nothing moves it while the test is
 * parked on the await. Interleaving is the only way round.
 */
async function readStored(): Promise<StoredShape | undefined> {
  const pending = localStorageLoad<StoredShape>(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`)
  let settled = false
  void pending.finally(() => {
    settled = true
  })
  for (let i = 0; i < 400; i++) {
    await vi.advanceTimersByTimeAsync(1)
    if (settled)
      break
  }
  return pending
}

interface StoredShape {
  v: typeof STORED_ROW_HEIGHTS_VERSION
  rows: [string, string, number][]
}

function item(id: string, heightKey: string): ChatRowItem {
  return { id, hasSpanLines: false, heightKey }
}

function storedRow(id: string, heightKey: string, height: number): [string, string, number] {
  return [
    id,
    fnv1a32Hex(heightKey),
    height,
  ]
}

describe('chatRowHeightPersistence', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    localStorageDrop(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`)
  })

  afterEach(() => {
    vi.useRealTimers()
    localStorageDrop(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`)
  })

  /**
   * The persistence over a real height cache.
   *
   * - `measured`: rows that the cache holds a height for at the start.
   * - `commitAll`: replaces the bulk commit of the cache, for a cache that adopts nothing.
   * - `primed`: the entries of each bulk commit that the persistence asked for.
   * - `measure(id, heightKey, height)`: a read of the mounted row commits its height.
   * - `noteChange()`: the cache reports a change with no new height.
   */
  function makeHarness(opts: {
    storageId?: string
    items?: ChatRowItem[]
    measured?: ChatRowItem[]
    commitAll?: RowHeightCache['commitAll']
  } = {}) {
    const [items, setItems] = createSignal<ChatRowItem[]>(opts.items ?? [])
    const [extraVersion, setExtraVersion] = createSignal(0)
    const [storageId, setStorageId] = createSignal(opts.storageId)
    const cache = createRowHeightCache({
      loadedIds: () => items().map(item => item.id),
    })
    for (const row of opts.measured ?? [])
      cache.commit(row, 50, row.heightKey, 'visible')
    const primed: PersistableRowHeight[][] = []
    let dispose!: () => void
    createRoot((d) => {
      dispose = d
      createRowHeightPersistence({
        storageId,
        loadedItems: items,
        heights: {
          commitAll: (entries) => {
            primed.push(entries.map(entry => ({
              id: entry.row.id,
              heightKey: entry.row.heightKey,
              height: entry.height,
            })))
            return (opts.commitAll ?? cache.commitAll)(entries)
          },
          snapshot: cache.snapshot,
          version: () => cache.version() + extraVersion(),
          isMeasured: cache.isMeasured,
        },
        storesRow: rowId => !rowId.startsWith('transient-'),
      })
    })
    return {
      setItems,
      setStorageId,
      primed,
      measure: (id: string, heightKey: string, height: number) =>
        cache.commit(item(id, heightKey), height, heightKey, 'visible'),
      noteChange: () => setExtraVersion(v => v + 1),
      dispose,
    }
  }

  // A row that exists only for a while -- the pending row of a running call -- takes no
  // stored slot: no later mount adopts it.
  it('stores no height of a row that it does not store', async () => {
    const harness = makeHarness({ storageId: 'agent-1' })
    try {
      await settle()
      harness.measure('kept', 'k-kept', 40)
      harness.measure('transient-row', 'k-transient', 30)
      vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS)
      await settle()
      expect((await readStored())?.rows).toEqual([
        storedRow('kept', 'k-kept', 40),
      ])
    }
    finally {
      harness.dispose()
    }
  })

  it('restores a saved zero and excludes a stale zero digest', async () => {
    const first = makeHarness({ storageId: 'agent-1' })
    try {
      await settle()
      first.measure('empty', 'k-empty', 0)
      first.measure('positive', 'k-positive', 120)
      vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS)
      await settle()
      expect((await readStored())?.rows).toEqual([
        storedRow('empty', 'k-empty', 0),
        storedRow('positive', 'k-positive', 120),
      ])
    }
    finally {
      first.dispose()
    }
    const restored = makeHarness({
      storageId: 'agent-1',
      items: [
        item('empty', 'k-empty'),
        item('positive', 'k-positive'),
      ],
    })
    try {
      await settle()
      expect(restored.primed).toEqual([
        [
          { id: 'empty', heightKey: 'k-empty', height: 0 },
          { id: 'positive', heightKey: 'k-positive', height: 120 },
        ],
      ])
    }
    finally {
      restored.dispose()
    }
    const stale = makeHarness({
      storageId: 'agent-1',
      items: [
        item('empty', 'new-key'),
      ],
    })
    try {
      await settle()
      expect(stale.primed).toEqual([])
    }
    finally {
      stale.dispose()
    }
  })

  it('hydrates stored rows whose key digest matches the live heightKey', async () => {
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [
        storedRow('a', 'k-a', 120),
        storedRow('b', 'k-b-old', 80),
      ],
    })
    const h = makeHarness({
      storageId: 'agent-1',
      items: [
        item('a', 'k-a'),
        item('b', 'k-b-new'),
      ],
    })
    await settle()
    // 'a' matches and hydrates; 'b' was measured under a different layout
    // epoch and must NOT be adopted.
    expect(h.primed).toEqual([
      [
        { id: 'a', heightKey: 'k-a', height: 120 },
      ],
    ])
    h.dispose()
  })

  it('adopts a pending row later, when its live key changes to match (width settles)', async () => {
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [
        storedRow('a', 'k-a|w800', 120),
      ],
    })
    const h = makeHarness({
      storageId: 'agent-1',
      items: [
        item('a', 'k-a|w360'),
      ],
    })
    await settle()
    expect(h.primed).toEqual([])
    h.setItems([
      item('a', 'k-a|w800'),
    ])
    expect(h.primed).toEqual([
      [
        { id: 'a', heightKey: 'k-a|w800', height: 120 },
      ],
    ])
    h.dispose()
  })

  it('tries a stored height again when the cache did not adopt it', async () => {
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [
        storedRow('a', 'k-a', 88),
      ],
    })
    const h = makeHarness({
      storageId: 'agent-1',
      items: [
        item('a', 'k-a'),
      ],
      // The cache adopts nothing, and the row stays unmeasured.
      commitAll: () => 0,
    })
    await settle()
    expect(h.primed).toEqual([
      [
        { id: 'a', heightKey: 'k-a', height: 88 },
      ],
    ])

    h.setItems([
      item('a', 'k-a'),
      item('b', 'k-b'),
    ])

    expect(h.primed).toEqual([
      [
        { id: 'a', heightKey: 'k-a', height: 88 },
      ],
      [
        { id: 'a', heightKey: 'k-a', height: 88 },
      ],
    ])
    h.dispose()
  })

  it('retires a pending row once a real measurement supersedes it', async () => {
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [
        storedRow('a', 'k-a', 120),
      ],
    })
    const h = makeHarness({
      storageId: 'agent-1',
      items: [
        item('a', 'k-a'),
      ],
      measured: [
        item('a', 'k-a'),
      ],
    })
    await settle()
    expect(h.primed).toEqual([])
    h.dispose()
  })

  it('saves a debounced digest snapshot merged with still-pending rows', async () => {
    // 'old' is a stored row for history that has not paginated in — it must
    // survive the save instead of being clobbered by the fresh snapshot.
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [
        storedRow('old', 'k-old', 300),
      ],
    })
    const h = makeHarness({ storageId: 'agent-1', items: [] })
    await settle()
    h.measure('a', 'k-a', 120.256)
    vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS)
    await settle()

    const stored = await readStored()
    expect(stored?.rows).toEqual([
      [
        'old',
        fnv1a32Hex('k-old'),
        300,
      ],
      [
        'a',
        fnv1a32Hex('k-a'),
        120.26,
      ], // rounded to 2dp
    ])
    h.dispose()
  })

  it('coalesces a measurement burst into one save', async () => {
    const h = makeHarness({ storageId: 'agent-1', items: [] })
    await settle()
    h.measure('a', 'k-a', 100)
    vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS - 1)
    await settle()
    h.measure('a', 'k-a', 101)
    vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS - 1)
    await settle()
    expect(await readStored()).toBeUndefined()
    vi.advanceTimersByTime(1)
    await settle()
    expect((await readStored())?.rows).toHaveLength(1)
    h.dispose()
  })

  it('never replaces stored data with an empty snapshot', async () => {
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [
        storedRow('a', 'k-a|w800', 120),
      ],
    })
    // Live key never matches (different width), so nothing adopts and the
    // snapshot is empty — the stored rows must survive the save tick of a change
    // of the cache that added no height.
    const h = makeHarness({
      storageId: 'agent-1',
      items: [
        item('a', 'k-a|w360'),
      ],
    })
    await settle()
    h.noteChange()
    vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS)
    await settle()
    expect((await readStored())?.rows)
      .toEqual([
        storedRow('a', 'k-a|w800', 120),
      ])
    h.dispose()
  })

  it('flushes an owed save on cleanup', async () => {
    const h = makeHarness({ storageId: 'agent-1', items: [] })
    await settle()
    h.measure('a', 'k-a', 100)
    h.dispose() // debounce still pending — cleanup must write
    expect((await readStored())?.rows).toHaveLength(1)
  })

  it('keeps older stored heights when the pane closes before its first load returns', async () => {
    const older = storedRow('older', 'older-key', 40)
    const write = localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [older],
    })
    await vi.advanceTimersByTimeAsync(ROW_HEIGHT_SAVE_DEBOUNCE_MS)
    expect(await write.durable).toBe(true)

    const h = makeHarness({ storageId: 'agent-1' })
    h.measure('fresh', 'fresh-key', 95)
    h.dispose()
    await settle()

    expect((await readStored())?.rows).toEqual([
      older,
      storedRow('fresh', 'fresh-key', 95),
    ])
  })

  it('discards a saved height from before tool rows selected their span block', async () => {
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: 7,
      rows: [
        storedRow('second-tool', 'same-row-key', 450),
      ],
    })
    const h = makeHarness({
      storageId: 'agent-1',
      items: [
        item('second-tool', 'same-row-key'),
      ],
    })
    try {
      await settle()
      expect(h.primed).toEqual([])
      expect(await readStored()).toBeUndefined()
    }
    finally {
      h.dispose()
    }
  })

  it('ignores a load that returns after the pane is disposed', async () => {
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [
        storedRow('a', 'k-a', 88),
      ],
    })
    const h = makeHarness({
      storageId: 'agent-1',
      items: [
        item('a', 'k-a'),
      ],
    })
    h.dispose()
    await settle()

    expect(h.primed).toEqual([])
  })

  it('keeps the incoming account cache when an outgoing read returns an obsolete version', async () => {
    const oldRead = deferred<{ v: number }>()
    const load = vi.spyOn(storage, 'localStorageLoad').mockReturnValueOnce(oldRead.promise)
    const h = makeHarness({ storageId: 'agent-1' })
    try {
      await settle()
      expect(load).toHaveBeenCalledOnce()
      storage.setStorageAccountForTests('incoming-account')
      const incoming = storedRow('incoming', 'incoming-key', 70)
      localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
        v: STORED_ROW_HEIGHTS_VERSION,
        rows: [incoming],
      })

      oldRead.resolve({ v: STORED_ROW_HEIGHTS_VERSION - 1 })
      await settle()

      expect((await readStored())?.rows).toEqual([incoming])
      expect(h.primed).toEqual([])
    }
    finally {
      h.dispose()
      load.mockRestore()
    }
  })

  it('preserves the outgoing pane snapshot when a replacement mounts before its cleanup', async () => {
    const older = storedRow('older', 'older-key', 40)
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [older],
    })
    const outgoing = makeHarness({ storageId: 'agent-1' })
    outgoing.measure('outgoing', 'outgoing-key', 60)
    const incoming = batch(() => {
      const pane = makeHarness({ storageId: 'agent-1' })
      pane.measure('incoming', 'incoming-key', 70)
      outgoing.dispose()
      return pane
    })
    try {
      await settle()
      await vi.advanceTimersByTimeAsync(ROW_HEIGHT_SAVE_DEBOUNCE_MS)
      expect((await readStored())?.rows).toEqual([
        older,
        storedRow('outgoing', 'outgoing-key', 60),
        storedRow('incoming', 'incoming-key', 70),
      ])
    }
    finally {
      incoming.dispose()
    }
  })

  it('is inert without a storage id', async () => {
    const h = makeHarness({
      items: [
        item('a', 'k-a'),
      ],
    })
    await settle()
    h.measure('a', 'k-a', 100)
    vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS)
    await settle()
    expect(h.primed).toEqual([])
    expect(await readStored()).toBeUndefined()
    h.dispose()
  })

  it('loads and hydrates once the storage id arrives late', async () => {
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [
        storedRow('a', 'k-a', 120),
      ],
    })
    const h = makeHarness({
      items: [
        item('a', 'k-a'),
      ],
    }) // no id yet
    await settle()
    expect(h.primed).toEqual([])
    h.setStorageId('agent-1')
    await settle()
    expect(h.primed).toEqual([
      [
        { id: 'a', heightKey: 'k-a', height: 120 },
      ],
    ])
    h.dispose()
  })

  // ONE LOAD PER INSTANCE is the whole contract, and it is what makes a
  // post-await identity recheck unreachable: the latch means a second id issues
  // no read at all, so no read can land under a chat the effect moved off. The
  // latch alone would NOT be enough to support a moving id -- `pending` still
  // holds the previous chat's rows -- which is why `ChatView` gives each agent tab
  // its own instance instead.
  it('loads once per instance, ignoring a storage id that moves afterwards', async () => {
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [
        storedRow('a', 'k-a', 120),
      ],
    })
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-2`, {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [
        storedRow('a', 'k-a', 999),
      ],
    })
    const h = makeHarness({
      storageId: 'agent-1',
      items: [
        item('a', 'k-a'),
      ],
    })
    await settle()
    expect(h.primed).toEqual([
      [
        { id: 'a', heightKey: 'k-a', height: 120 },
      ],
    ])

    h.setStorageId('agent-2')
    await settle()

    // The second chat's 999 never reaches the cache, and no second prime
    // happens at all.
    expect(h.primed).toEqual([
      [
        { id: 'a', heightKey: 'k-a', height: 120 },
      ],
    ])
    h.dispose()
    localStorageDrop(`${PREFIX_CHAT_ROW_HEIGHTS}agent-2`)
  })

  it('caps the stored snapshot at the ceiling, dropping pending entries before fresh measurements', async () => {
    // A never-matching pending row (different layout epoch) plus a full
    // ceiling's worth of fresh measurements: the cap must keep every fresh
    // row and shed the pending one first.
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [
        storedRow('stale', 'k-stale|w999', 50),
      ],
    })
    const h = makeHarness({
      storageId: 'agent-1',
      items: [
        item('stale', 'k-live'),
      ],
    })
    await settle()
    batch(() => {
      for (let i = 0; i < PERSISTED_ROW_HEIGHTS_MAX; i++)
        h.measure(`r${i}`, `k-${i}`, 10 + i)
    })
    vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS)
    await settle()

    const stored = await readStored()
    expect(stored?.rows).toHaveLength(PERSISTED_ROW_HEIGHTS_MAX)
    expect(stored?.rows[0]?.[0]).toBe('r0') // 'stale' (inserted first) was shed
    expect(stored?.rows.some(([id]) => id === 'stale')).toBe(false)
    h.dispose()
  })

  it('places a still-pending row that got measured at the fresh (most-recent) end', async () => {
    // 'a' loads as pending under an OLD layout epoch (digest mismatch keeps it
    // pending), then gets measured under its live key before the item-list
    // change that would retire it. When the save fires it is in BOTH pending and
    // the snapshot -- and its fresh measurement must land at the recent end, not
    // inherit 'a's early pending slot (which the cap would shed first).
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [
        storedRow('a', 'k-a-old', 120),
      ],
    })
    const h = makeHarness({
      storageId: 'agent-1',
      items: [
        item('a', 'k-a-new'),
      ],
    })
    await settle()
    // The snapshot is LRU-ordered oldest-first: 'z' measured before 'a'.
    h.measure('z', 'k-z', 40)
    h.measure('a', 'k-a-new', 200)
    vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS)
    await settle()

    const stored = await readStored()
    // 'a' at the END (freshly-measured position), carrying its measured height +
    // live-key digest -- not stranded at the front where the over-cap slice bites.
    expect(stored?.rows).toEqual([
      [
        'z',
        fnv1a32Hex('k-z'),
        40,
      ],
      [
        'a',
        fnv1a32Hex('k-a-new'),
        200,
      ],
    ])
    h.dispose()
  })

  it('discards a payload written by an older layout version', async () => {
    // A heightKey never encodes the stylesheet, so an older payload's digests
    // still match while every height in it is wrong. The version guard is the
    // only thing that stops those heights from jumping the offset map.
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: STORED_ROW_HEIGHTS_VERSION - 1,
      rows: [
        storedRow('a', 'k-a', 120),
      ],
    })
    const h = makeHarness({
      storageId: 'agent-1',
      items: [
        item('a', 'k-a'),
      ],
    })
    await settle()
    expect(h.primed).toEqual([])
    // And it is DELETED, not merely ignored. The read that returned it also refreshed its
    // expiry, so a chat the reader opens and leaves -- one that commits no height, and so
    // never overwrites the key -- would keep tens of KB of dead rows alive indefinitely.
    expect(await readStored()).toBeUndefined()
    h.dispose()
  })

  it('ignores malformed stored payloads', async () => {
    localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [
        [
          'a',
          fnv1a32Hex('k-a'),
        ], // missing height
        [
          'b',
          fnv1a32Hex('k-b'),
          -5,
        ], // non-positive
        [
          'c',
          fnv1a32Hex('k-c'),
          Number.NaN,
        ], // non-finite
        'junk',
        [
          'd',
          fnv1a32Hex('k-d'),
          40,
        ],
      ],
    })
    const h = makeHarness({
      storageId: 'agent-1',
      items: [
        item('a', 'k-a'),
        item('b', 'k-b'),
        item('c', 'k-c'),
        item('d', 'k-d'),
      ],
    })
    await settle()
    expect(h.primed).toEqual([
      [
        { id: 'd', heightKey: 'k-d', height: 40 },
      ],
    ])
    h.dispose()
  })
})
