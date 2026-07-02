import type { PersistableRowHeight, VirtualItem } from './useChatVirtualizer'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { localStorageGet, localStorageSet, PREFIX_CHAT_ROW_HEIGHTS } from '~/lib/browserStorage'
import { fnv1a32Hex } from '~/lib/stringDigest'
import { createRowHeightPersistence, PERSISTED_ROW_HEIGHTS_MAX, ROW_HEIGHT_SAVE_DEBOUNCE_MS } from './chatRowHeightPersistence'

interface StoredShape {
  v: 1
  rows: [string, string, number][]
}

function item(id: string, heightKey: string): VirtualItem {
  return { id, hasSpanLines: false, heightKey }
}

function storedRow(id: string, heightKey: string, height: number): [string, string, number] {
  return [id, fnv1a32Hex(heightKey), height]
}

describe('chatrowheightpersistence', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    localStorage.clear()
  })

  afterEach(() => {
    vi.useRealTimers()
    localStorage.clear()
  })

  function makeHarness(opts: {
    storageId?: string
    items?: VirtualItem[]
    measured?: Set<string>
  } = {}) {
    const [items, setItems] = createSignal<VirtualItem[]>(opts.items ?? [])
    const [geomVersion, setGeomVersion] = createSignal(0)
    const [storageId, setStorageId] = createSignal(opts.storageId)
    const measured = opts.measured ?? new Set<string>()
    const primed: PersistableRowHeight[][] = []
    const snapshot: PersistableRowHeight[] = []
    let dispose!: () => void
    createRoot((d) => {
      dispose = d
      createRowHeightPersistence({
        storageId,
        virtualItems: items,
        virt: {
          primeHeights: (entries) => {
            primed.push([...entries])
            for (const e of entries)
              measured.add(e.id)
            return entries.length
          },
          snapshotHeights: () => [...snapshot],
          geometryVersion: geomVersion,
          hasMeasuredHeight: id => measured.has(id),
        },
      })
    })
    return { setItems, setGeomVersion, setStorageId, primed, snapshot, measured, dispose }
  }

  it('hydrates stored rows whose key digest matches the live heightKey', () => {
    localStorageSet(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: 1,
      rows: [storedRow('a', 'k-a', 120), storedRow('b', 'k-b-old', 80)],
    })
    const h = makeHarness({
      storageId: 'agent-1',
      items: [item('a', 'k-a'), item('b', 'k-b-new')],
    })
    // 'a' matches and hydrates; 'b' was measured under a different layout
    // epoch and must NOT be adopted.
    expect(h.primed).toEqual([[{ id: 'a', heightKey: 'k-a', height: 120 }]])
    h.dispose()
  })

  it('adopts a pending row later, when its live key changes to match (width settles)', () => {
    localStorageSet(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: 1,
      rows: [storedRow('a', 'k-a|w800', 120)],
    })
    const h = makeHarness({ storageId: 'agent-1', items: [item('a', 'k-a|w360')] })
    expect(h.primed).toEqual([])
    h.setItems([item('a', 'k-a|w800')])
    expect(h.primed).toEqual([[{ id: 'a', heightKey: 'k-a|w800', height: 120 }]])
    h.dispose()
  })

  it('retires a pending row once a real measurement supersedes it', () => {
    localStorageSet(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: 1,
      rows: [storedRow('a', 'k-a', 120)],
    })
    const measured = new Set(['a'])
    const h = makeHarness({ storageId: 'agent-1', items: [item('a', 'k-a')], measured })
    expect(h.primed).toEqual([])
    h.dispose()
  })

  it('saves a debounced digest snapshot merged with still-pending rows', () => {
    // 'old' is a stored row for history that has not paginated in — it must
    // survive the save instead of being clobbered by the fresh snapshot.
    localStorageSet(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: 1,
      rows: [storedRow('old', 'k-old', 300)],
    })
    const h = makeHarness({ storageId: 'agent-1', items: [] })
    h.snapshot.push({ id: 'a', heightKey: 'k-a', height: 120.256 })
    h.setGeomVersion(1)
    vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS)

    const stored = localStorageGet<StoredShape>(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`)
    expect(stored?.rows).toEqual([
      ['old', fnv1a32Hex('k-old'), 300],
      ['a', fnv1a32Hex('k-a'), 120.26], // rounded to 2dp
    ])
    h.dispose()
  })

  it('coalesces a measurement burst into one save', () => {
    const h = makeHarness({ storageId: 'agent-1', items: [] })
    h.snapshot.push({ id: 'a', heightKey: 'k-a', height: 100 })
    h.setGeomVersion(1)
    vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS - 1)
    h.setGeomVersion(2)
    vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS - 1)
    expect(localStorageGet(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`)).toBeUndefined()
    vi.advanceTimersByTime(1)
    expect(localStorageGet<StoredShape>(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`)?.rows).toHaveLength(1)
    h.dispose()
  })

  it('never replaces stored data with an empty snapshot', () => {
    localStorageSet(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: 1,
      rows: [storedRow('a', 'k-a|w800', 120)],
    })
    // Live key never matches (different width), so nothing adopts and the
    // snapshot is empty — the stored rows must survive the save tick.
    const h = makeHarness({ storageId: 'agent-1', items: [item('a', 'k-a|w360')] })
    h.setGeomVersion(1)
    vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS)
    expect(localStorageGet<StoredShape>(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`)?.rows)
      .toEqual([storedRow('a', 'k-a|w800', 120)])
    h.dispose()
  })

  it('flushes an owed save on cleanup', () => {
    const h = makeHarness({ storageId: 'agent-1', items: [] })
    h.snapshot.push({ id: 'a', heightKey: 'k-a', height: 100 })
    h.setGeomVersion(1)
    h.dispose() // debounce still pending — cleanup must write
    expect(localStorageGet<StoredShape>(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`)?.rows).toHaveLength(1)
  })

  it('is inert without a storage id', () => {
    const h = makeHarness({ items: [item('a', 'k-a')] })
    h.snapshot.push({ id: 'a', heightKey: 'k-a', height: 100 })
    h.setGeomVersion(1)
    vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS)
    expect(h.primed).toEqual([])
    expect(localStorage.length).toBe(0)
    h.dispose()
  })

  it('loads and hydrates once the storage id arrives late', () => {
    localStorageSet(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: 1,
      rows: [storedRow('a', 'k-a', 120)],
    })
    const h = makeHarness({ items: [item('a', 'k-a')] }) // no id yet
    expect(h.primed).toEqual([])
    h.setStorageId('agent-1')
    expect(h.primed).toEqual([[{ id: 'a', heightKey: 'k-a', height: 120 }]])
    h.dispose()
  })

  it('caps the stored snapshot at the ceiling, dropping pending entries before fresh measurements', () => {
    // A never-matching pending row (different layout epoch) plus a full
    // ceiling's worth of fresh measurements: the cap must keep every fresh
    // row and shed the pending one first.
    localStorageSet(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: 1,
      rows: [storedRow('stale', 'k-stale|w999', 50)],
    })
    const h = makeHarness({ storageId: 'agent-1', items: [item('stale', 'k-live')] })
    for (let i = 0; i < PERSISTED_ROW_HEIGHTS_MAX; i++)
      h.snapshot.push({ id: `r${i}`, heightKey: `k-${i}`, height: 10 + i })
    h.setGeomVersion(1)
    vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS)

    const stored = localStorageGet<StoredShape>(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`)
    expect(stored?.rows).toHaveLength(PERSISTED_ROW_HEIGHTS_MAX)
    expect(stored?.rows[0][0]).toBe('r0') // 'stale' (inserted first) was shed
    expect(stored?.rows.some(([id]) => id === 'stale')).toBe(false)
    h.dispose()
  })

  it('ignores malformed stored payloads', () => {
    localStorageSet(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: 1,
      rows: [
        ['a', fnv1a32Hex('k-a')], // missing height
        ['b', fnv1a32Hex('k-b'), -5], // non-positive
        ['c', fnv1a32Hex('k-c'), Number.NaN], // non-finite
        'junk',
        ['d', fnv1a32Hex('k-d'), 40],
      ],
    })
    const h = makeHarness({
      storageId: 'agent-1',
      items: [item('a', 'k-a'), item('b', 'k-b'), item('c', 'k-c'), item('d', 'k-d')],
    })
    expect(h.primed).toEqual([[{ id: 'd', heightKey: 'k-d', height: 40 }]])
    h.dispose()
  })
})
