import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KEY_CHANNEL_RELAY_SEQ, KEY_ORG_EVENTS_RELAY_SEQ, localStorageGet, localStorageSet } from './browserStorage'
import { createPersistedSeq } from './persistedSeq'

// The seeding/clock-regression behavior is pinned through both consumers
// (relayClaim.test.ts and useOrgEvents.test.ts drive it via vi.resetModules);
// what only this test pins is the mark/id algebra, key isolation, and the
// cross-process uniqueness the per-process random low bits exist for.

// A deterministic CSPRNG mock: each call to getRandomValues advances a counter
// so two allocator instances constructed in the same test get DISTINCT low
// bits (which is what the uniqueness property turns on). Restored in afterEach.
let cryptoSpy: ReturnType<typeof vi.spyOn> | null = null
let cryptoCounter = 0

function installCryptoMock(): void {
  cryptoCounter = 0
  cryptoSpy = vi.spyOn(globalThis.crypto, 'getRandomValues').mockImplementation((arr: ArrayBufferView<ArrayBuffer>) => {
    // getRandomValues is generic over ArrayBufferView; the production caller
    // passes a Uint16Array, so index its elements (not bytes) to preserve the
    // deterministic per-element counter the uniqueness assertions depend on.
    const view = arr as unknown as { length: number, [i: number]: number }
    for (let i = 0; i < view.length; i++) {
      view[i] = cryptoCounter & 0xFF
      cryptoCounter = (cryptoCounter + 1) & 0xFF
    }
    return arr
  })
}

describe('createPersistedSeq', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  afterEach(() => {
    if (cryptoSpy) {
      cryptoSpy.mockRestore()
      cryptoSpy = null
    }
  })

  // The persisted value is the high-water MARK; the returned id carries that
  // mark in its high bits plus a per-process random in its low bits. So the
  // mark advances by 1 per allocation and is what storage holds, while the id
  // advances by 2^TAB_BITS per allocation (the low bits stay constant within
  // one allocator).
  it('persists the monotonic mark and derives ids from it', () => {
    installCryptoMock()
    const next = createPersistedSeq(KEY_CHANNEL_RELAY_SEQ)
    const first = next()
    const second = next()

    // A fresh install seeds the mark from 0, then ++ on each call, so after two
    // allocations the persisted mark is exactly 2.
    expect(localStorageGet<number>(KEY_CHANNEL_RELAY_SEQ)).toBe(2)
    // Consecutive ids from one allocator advance by a constant stride (the low
    // bits are fixed per allocator; the high mark advances by 1).
    expect(second).toBeGreaterThan(first)
    const stride = second - first
    // Stride is exactly one power of two (the low-bits width).
    expect(stride & (stride - 1)).toBe(0)
    expect(stride).toBeGreaterThan(1)
    // Same low bits across consecutive allocations within one allocator. Modulo,
    // not &, because the id exceeds 32 bits and JS bitwise ops truncate.
    expect(first % stride).toBe(second % stride)
  })

  // The counter continues from EXACTLY the persisted mark (then +1), not "some
  // value above it" -- the sidecar's strict-greater owner fence requires the
  // reload's first id to land above the stale owner, and a value that merely
  // exceeded the mark by an unspecified amount would not pin that property to
  // the mark itself.
  it('continues from exactly the persisted mark on the first call', () => {
    installCryptoMock()
    const persistedMark = 42
    localStorageSet(KEY_CHANNEL_RELAY_SEQ, persistedMark)
    const next = createPersistedSeq(KEY_CHANNEL_RELAY_SEQ)
    next()
    // The persisted mark was honored as-is and advanced by exactly 1.
    expect(localStorageGet<number>(KEY_CHANNEL_RELAY_SEQ)).toBe(persistedMark + 1)
  })

  it('keeps sequences over different keys independent', () => {
    installCryptoMock()
    const nextA = createPersistedSeq(KEY_CHANNEL_RELAY_SEQ)
    const nextB = createPersistedSeq(KEY_ORG_EVENTS_RELAY_SEQ)

    const a1 = nextA()
    const a2 = nextA()
    const b1 = nextB()
    const a3 = nextA()

    expect(a2).toBeGreaterThan(a1)
    expect(a3).toBeGreaterThan(a2)
    expect(b1).not.toBe(a1)
    // Each key persists its own mark independently.
    expect(localStorageGet<number>(KEY_CHANNEL_RELAY_SEQ)).not.toBe(
      localStorageGet<number>(KEY_ORG_EVENTS_RELAY_SEQ),
    )
  })

  // The uniqueness property the per-process random exists for: two processes
  // sharing localStorage (two Tauri windows, or two desktop apps on one
  // machine) read the SAME persisted mark. Without the random low bits their
  // ids would collide and the sidecar's strict-greater owner fence would admit
  // both, letting one process's close tear down the other's relay. The low
  // bits differ per allocator instance, so the ids differ even for the same
  // mark.
  it('mints distinct ids for two processes sharing the same persisted mark', () => {
    // Pre-seed the mark so both allocators read the same starting value,
    // simulating two processes that share localStorage and read the same mark
    // before either has written.
    const sharedMark = 1_000_000
    const writeShared = () => localStorageSet(KEY_CHANNEL_RELAY_SEQ, sharedMark)
    writeShared()

    installCryptoMock()
    // Two allocator instances = two processes. Process A reads the shared mark,
    // then we RESTORE storage to the shared mark before process B reads --
    // simulating two processes whose reads both saw the same mark (in one JS
    // runtime localStorage would otherwise serialize A's write ahead of B's
    // read).
    const nextA = createPersistedSeq(KEY_CHANNEL_RELAY_SEQ)
    const a = nextA()
    writeShared() // restore: process B reads the same mark A did
    const nextB = createPersistedSeq(KEY_CHANNEL_RELAY_SEQ)
    const b = nextB()

    expect(a).not.toBe(b)
    // Both ids are above the shared mark (each process incremented it by 1),
    // and they differ in the low bits -- the per-process differentiator that
    // breaks the cross-process collision. The composed id = mark * stride +
    // random; with mark = sharedMark + 1 it is strictly greater than the mark
    // for any TAB_BITS >= 1.
    expect(a).toBeGreaterThan(sharedMark)
    expect(b).toBeGreaterThan(sharedMark)
    // Derive the stride from a single allocator's two consecutive ids.
    const a2 = nextA()
    const realStride = a2 - a
    expect(realStride).toBeGreaterThan(1)
    expect(a % realStride).not.toBe(b % realStride)
  })

  // Reload monotonicity: a fresh allocator (simulating a webview reload) reads
  // the persisted mark and continues above it, so its open supersedes the
  // stale owner the sidecar still holds. The per-process low bits regenerate,
  // but the high mark advanced by the prior process's allocations keeps the
  // new id strictly greater.
  it('continues above the persisted mark across a fresh allocator (reload)', () => {
    installCryptoMock()
    const first = createPersistedSeq(KEY_CHANNEL_RELAY_SEQ)
    const id1 = first()
    const id2 = first()

    // A fresh allocator on the same key simulates a reload: it reads the mark
    // the prior allocator persisted and continues from there.
    const reloaded = createPersistedSeq(KEY_CHANNEL_RELAY_SEQ)
    const id3 = reloaded()

    // A reload's first id must supersede the stale one (see the comment above).
    expect(id3).toBeGreaterThan(id2)
    expect(id3).toBeGreaterThan(id1)
  })

  // A corrupted persisted value must NOT poison the sequence: mark = NaN; mark++
  // stays NaN for the install's life, and the relay owner-fence (claimId >
  // ownerId) is always false against NaN, so every open would refuse itself until
  // the key was cleared. localStorageGet unwraps the `{v, e}` cell via JSON.parse,
  // and JSON has no NaN/Infinity literal -- a stored NaN/Infinity surfaces as
  // `null`, which the `typeof persisted === 'number'` guard rejects. The values
  // that CAN reach the validation predicate are real numbers that fail
  // Number.isSafeInteger (negative, fractional, beyond MAX_SAFE_INTEGER) or fail
  // the composition bound (above MARK_LIMIT); the table below covers each, and the
  // null/NaN-arrival case is pinned separately. Anything rejected re-seeds from 0.
  it.each([
    ['negative', -1],
    ['fractional', 1.5],
    ['a non-integer beyond MAX_SAFE_INTEGER', Number.MAX_SAFE_INTEGER + 1],
    ['above the composition ceiling (MARK_LIMIT + 1)', 2_199_023_255_552],
  ])('rejects a %s persisted mark and re-seeds from 0', (_label, corrupted) => {
    installCryptoMock()
    // localStorageSet (not raw setItem): every value here round-trips through
    // JSON faithfully (none are NaN/Infinity), so the helper is the correct
    // production path to exercise. The repo's browser-storage rule routes
    // writes through it, and the {v, e} wrapper it produces is what readDynamic
    // unwraps on the next read.
    localStorageSet(KEY_CHANNEL_RELAY_SEQ, corrupted)
    const next = createPersistedSeq(KEY_CHANNEL_RELAY_SEQ)
    const first = next()
    // The corrupted value was rejected; the mark starts at 0 and the first id
    // is exactly 1 * stride + (per-process random low bits).
    expect(Number.isSafeInteger(first)).toBe(true)
    expect(first).toBeGreaterThan(0)
    const mark = localStorageGet<number>(KEY_CHANNEL_RELAY_SEQ)
    expect(mark).toBe(1)
  })

  // A stored NaN/Infinity (or any value JSON coerces to null) reaches the
  // allocator as `null`, not as the original value -- JSON has no NaN/Infinity
  // literal, so JSON.stringify({v: NaN}) writes `{"v":null}`. The allocator must
  // reject `null` (via `typeof === 'number'`) exactly as it rejects a non-number,
  // re-seeding from 0. This pins the arrival-as-null reality the JSON path
  // produces, distinct from the real-number corruptions above.
  it('rejects a NaN that JSON coerced to null and re-seeds from 0', () => {
    installCryptoMock()
    localStorageSet(KEY_CHANNEL_RELAY_SEQ, Number.NaN)
    // Sanity-check the arrival path the test depends on: JSON serialized NaN
    // to null, so the cell the allocator reads holds null, not NaN. (KEY_*
    // already carries the `leapmux:` prefix, so read it verbatim.)
    const raw = localStorage.getItem(KEY_CHANNEL_RELAY_SEQ)
    expect(raw).toContain('"v":null')
    const next = createPersistedSeq(KEY_CHANNEL_RELAY_SEQ)
    const first = next()
    expect(Number.isSafeInteger(first)).toBe(true)
    expect(first).toBeGreaterThan(0)
    const persisted = localStorageGet<number>(KEY_CHANNEL_RELAY_SEQ)
    expect(persisted).toBe(1)
  })

  // The mark is a plain monotonic counter, NOT derived from the wall clock. This
  // is the whole point of issue #298: a clock-seeded mark carries a finite
  // horizon (it eventually crosses Number.MAX_SAFE_INTEGER once packed into the
  // high bits), whereas a counter has no horizon. Pin the invariant so a future
  // change cannot quietly reintroduce a clock dependency: with the clock mocked
  // far below a persisted mark, the allocator still honors the persisted mark.
  // (The allocator itself no longer calls Date.now, so the spy is a regression
  // guard against re-introducing a clock seed, not a direct exercise of
  // allocator code -- the `first > persistedMark` assertion is what pins the
  // behavior, identical to the exact-continuation test above.)
  it('does not seed from the wall clock', () => {
    installCryptoMock()
    const persistedMark = 1_000_000
    localStorageSet(KEY_CHANNEL_RELAY_SEQ, persistedMark)
    const dateSpy = vi.spyOn(Date, 'now').mockReturnValue(1)
    try {
      const next = createPersistedSeq(KEY_CHANNEL_RELAY_SEQ)
      const first = next()
      // The persisted mark is honored (1_000_001 * stride), not the mocked
      // Date.now()=1: the composed id is far above what a clock seed of 1
      // could produce, regardless of TAB_BITS.
      expect(first).toBeGreaterThan(persistedMark)
    }
    finally {
      dateSpy.mockRestore()
    }
  })
})
