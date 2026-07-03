import type { PersistableRowHeight, VirtualItem } from './useChatVirtualizer'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { localStorageGet, localStorageRemove, localStorageSet, PREFIX_CHAT_ROW_HEIGHTS } from '~/lib/browserStorage'
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
    localStorageRemove(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`)
  })

  afterEach(() => {
    vi.useRealTimers()
    localStorageRemove(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`)
  })

  function makeHarness(opts: {
    storageId?: string
    items?: VirtualItem[]
    measured?: Set<string>
    /** Ids whose deferred premeasure is queued behind the momentum gate (not yet committed). */
    pending?: Set<string>
    primeHeights?: (entries: readonly PersistableRowHeight[], measured: Set<string>) => number
  } = {}) {
    const [items, setItems] = createSignal<VirtualItem[]>(opts.items ?? [])
    const [geomVersion, setGeomVersion] = createSignal(0)
    const [storageId, setStorageId] = createSignal(opts.storageId)
    const measured = opts.measured ?? new Set<string>()
    const pending = opts.pending ?? new Set<string>()
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
            if (opts.primeHeights)
              return opts.primeHeights(entries, measured)
            for (const e of entries)
              measured.add(e.id)
            return entries.length
          },
          snapshotHeights: () => [...snapshot],
          geometryVersion: geomVersion,
          hasMeasuredHeight: id => measured.has(id),
          hasPendingPremeasuredHeight: id => pending.has(id),
        },
      })
    })
    return { setItems, setGeomVersion, setStorageId, primed, snapshot, measured, pending, dispose }
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

  it('keeps a pending row when the virtualizer defers prime-height adoption', () => {
    localStorageSet(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: 1,
      rows: [storedRow('a', 'k-a', 120)],
    })
    const pending = new Set<string>()
    const h = makeHarness({
      storageId: 'agent-1',
      items: [item('a', 'k-a')],
      pending,
      // Deferred behind the momentum gate: nothing commits, but the row is marked
      // pending (as the real virtualizer does), so it isn't re-primed on every
      // item-list change while the deferral is live.
      primeHeights: (entries) => {
        for (const e of entries)
          pending.add(e.id)
        return 0
      },
    })
    expect(h.primed).toEqual([[{ id: 'a', heightKey: 'k-a', height: 120 }]])

    h.snapshot.push({ id: 'b', heightKey: 'k-b', height: 80 })
    h.setGeomVersion(1)
    vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS)

    expect(localStorageGet<StoredShape>(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`)?.rows)
      .toEqual([storedRow('a', 'k-a', 120), storedRow('b', 'k-b', 80)])
    h.dispose()
  })

  it('re-attempts a deferred prime after the fling settles and the key still matches', () => {
    // A momentum fling is in flight when the row's warm-start is first
    // attempted: the virtualizer DEFERS the prime (queues it, commits nothing,
    // returns 0). The row's live heightKey then drifts (a transient UI/chrome
    // toggle) and reverts to the SAME digest-matching key. Because the deferred
    // commit was rejected at flush time (key had drifted), the warm-start height
    // is still not in the cache -- so the persistence layer MUST re-attempt the
    // prime when the key matches again. It must not permanently bar the row.
    localStorageSet(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: 1,
      rows: [storedRow('a', 'k-a', 120)],
    })
    const pending = new Set<string>()
    let deferring = true
    const h = makeHarness({
      storageId: 'agent-1',
      items: [item('a', 'k-a')],
      pending,
      // While the fling is active the prime is deferred: nothing commits, the row
      // stays UNMEASURED, and (as the real virtualizer does) it is marked pending
      // behind the momentum gate. Once the fling settles it commits normally.
      primeHeights: (entries, measured) => {
        if (deferring) {
          for (const e of entries)
            pending.add(e.id)
          return 0
        }
        for (const e of entries) {
          pending.delete(e.id)
          measured.add(e.id)
        }
        return entries.length
      },
    })
    // First attempt on load: deferred (commits nothing, row stays unmeasured).
    expect(h.primed).toEqual([[{ id: 'a', heightKey: 'k-a', height: 120 }]])

    // Key drifts mid-fling (a chrome/UI toggle rewrote it): digest no longer
    // matches, so nothing is attempted for the transient key.
    h.setItems([item('a', 'k-a-transient')])
    expect(h.primed).toHaveLength(1)

    // Fling has settled; the key reverts to its original digest-matching value.
    deferring = false
    h.setItems([item('a', 'k-a')])

    // The warm-start height MUST be adopted now -- the earlier deferral was
    // transient, not a permanent rejection.
    expect(h.primed).toEqual([
      [{ id: 'a', heightKey: 'k-a', height: 120 }],
      [{ id: 'a', heightKey: 'k-a', height: 120 }],
    ])
    expect(h.measured.has('a')).toBe(true)
    h.dispose()
  })

  it('re-attempts a deferred prime that was DROPPED under the same key, but not while it is still queued', () => {
    // A momentum fling defers the prime (queues it behind the gate, commits
    // nothing). The row is then trimmed out of the window before the deferral
    // flush can commit it, so the queued prime is DROPPED -- neither measured nor
    // still pending. Under the SAME digest-matching key (no key drift to clear the
    // attempt marker), the persistence layer must re-attempt when the row returns,
    // or the stale marker bars its warm-start height for the component's life.
    localStorageSet(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: 1,
      rows: [storedRow('a', 'k-a', 120)],
    })
    const pending = new Set<string>()
    let deferring = true
    const h = makeHarness({
      storageId: 'agent-1',
      items: [item('a', 'k-a')],
      pending,
      // While the fling is active the prime is DEFERRED: queued as pending,
      // committing nothing. Once it settles it commits normally.
      primeHeights: (entries, measured) => {
        if (deferring) {
          for (const e of entries)
            pending.add(e.id)
          return 0
        }
        for (const e of entries)
          measured.add(e.id)
        return entries.length
      },
    })
    // First attempt on load: deferred (queued as pending, nothing committed).
    expect(h.primed).toHaveLength(1)

    // An item-list change while the prime is STILL queued must NOT re-prime it --
    // the marker suppresses the per-item-change churn during the fling.
    h.setItems([item('a', 'k-a'), item('z', 'k-z')])
    expect(h.primed).toHaveLength(1)

    // The fling settles, but the row was trimmed out before the flush, so its
    // queued prime is dropped: no longer pending, still unmeasured.
    pending.delete('a')
    deferring = false
    // The same digest-matching key reappears -> the stale marker must clear and the
    // warm-start height must finally be adopted.
    h.setItems([item('a', 'k-a')])
    expect(h.primed).toHaveLength(2)
    expect(h.primed[1]).toEqual([{ id: 'a', heightKey: 'k-a', height: 120 }])
    expect(h.measured.has('a')).toBe(true)
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
    expect(localStorageGet(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`)).toBeUndefined()
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

  it('places a still-pending row that got measured at the fresh (most-recent) end', () => {
    // 'a' loads as pending under an OLD layout epoch (digest mismatch keeps it
    // pending), then gets measured under its live key before the item-list
    // change that would retire it. When the save fires it is in BOTH pending and
    // the snapshot -- and its fresh measurement must land at the recent end, not
    // inherit 'a's early pending slot (which the cap would shed first).
    localStorageSet(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`, {
      v: 1,
      rows: [storedRow('a', 'k-a-old', 120)],
    })
    const h = makeHarness({ storageId: 'agent-1', items: [item('a', 'k-a-new')] })
    // snapshotHeights is LRU-ordered oldest-first: 'z' measured before 'a'.
    h.snapshot.push({ id: 'z', heightKey: 'k-z', height: 40 })
    h.snapshot.push({ id: 'a', heightKey: 'k-a-new', height: 200 })
    h.setGeomVersion(1)
    vi.advanceTimersByTime(ROW_HEIGHT_SAVE_DEBOUNCE_MS)

    const stored = localStorageGet<StoredShape>(`${PREFIX_CHAT_ROW_HEIGHTS}agent-1`)
    // 'a' at the END (freshly-measured position), carrying its measured height +
    // live-key digest -- not stranded at the front where the over-cap slice bites.
    expect(stored?.rows).toEqual([
      ['z', fnv1a32Hex('k-z'), 40],
      ['a', fnv1a32Hex('k-a-new'), 200],
    ])
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
