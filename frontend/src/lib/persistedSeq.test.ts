import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KEY_CHANNEL_RELAY_SEQ, KEY_ORG_EVENTS_RELAY_SEQ, localStorageGet } from './browserStorage'
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

    // The mark increments by 1 and is what storage holds.
    expect(localStorageGet<number>(KEY_CHANNEL_RELAY_SEQ)).toBeGreaterThan(0)
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
    const sharedMark = Date.now()
    const writeShared = () => localStorage.setItem(
      `leapmux:${KEY_CHANNEL_RELAY_SEQ}`,
      JSON.stringify({ v: sharedMark, e: Date.now() + 86_400_000 }),
    )
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
    // breaks the cross-process collision. Modulo (not &) because the id
    // exceeds 32 bits.
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

  // A NaN reaching the persisted mark (a corrupted or hand-edited storage value,
  // or any future caller that computes the seed arithmetically) must NOT poison
  // the sequence: Math.max(epochClock(), NaN) === NaN, and once the mark is NaN
  // every later mark++ stays NaN for the install's life. The relay owner-fence
  // (claimId > ownerId) is always false against NaN, so every open would refuse
  // itself until the key is cleared. Number.isFinite rejects NaN so the seed
  // falls back to the (epoch-anchored) clock instead.
  it('rejects a NaN persisted mark and falls back to the clock', () => {
    installCryptoMock()
    localStorage.setItem(
      `leapmux:${KEY_CHANNEL_RELAY_SEQ}`,
      JSON.stringify({ v: Number.NaN, e: Date.now() + 86_400_000 }),
    )
    const next = createPersistedSeq(KEY_CHANNEL_RELAY_SEQ)
    const first = next()
    // The NaN was rejected: the id is a finite, positive, exact integer rather
    // than NaN. (The mark is epoch-anchored, so it is far below the raw wall
    // clock -- the assertion is finiteness/positivity, not a clock comparison.)
    expect(Number.isSafeInteger(first)).toBe(true)
    expect(first).toBeGreaterThan(0)
    // The poisoned value is overwritten with a finite, positive mark on the first call.
    const persisted = localStorageGet<number>(KEY_CHANNEL_RELAY_SEQ)
    expect(Number.isFinite(persisted)).toBe(true)
    expect(persisted!).toBeGreaterThan(0)
  })

  // The id must stay under Number.MAX_SAFE_INTEGER so it serializes through
  // Tauri IPC and the Rust u64 exactly. A mark that would overflow once shifted
  // into the high bits is re-seeded from the clock.
  it('keeps ids below Number.MAX_SAFE_INTEGER even with a large persisted mark', () => {
    installCryptoMock()
    // A persisted mark at the very top of the range -- larger than fits under
    // MAX_SAFE_INTEGER once shifted -- must be re-seeded rather than producing
    // an overflowing id.
    const oversized = Number.MAX_SAFE_INTEGER
    localStorage.setItem(
      `leapmux:${KEY_CHANNEL_RELAY_SEQ}`,
      JSON.stringify({ v: oversized, e: Date.now() + 86_400_000 }),
    )
    const next = createPersistedSeq(KEY_CHANNEL_RELAY_SEQ)
    const first = next()
    expect(first).toBeLessThanOrEqual(Number.MAX_SAFE_INTEGER)
    expect(Number.isSafeInteger(first)).toBe(true)
  })

  // The mark is seeded from an app-anchored clock (Date.now() - APP_EPOCH_MS), not
  // the raw Unix-epoch millisecond. A raw Date.now() (~1.78e12 in 2026) sits close
  // to MARK_LIMIT (~2.199e12) and crosses it around 2039, after which composed ids
  // go inexact; the epoch anchoring keeps the mark far below MARK_LIMIT into the
  // 2090s so the id stays an exact integer.
  it('anchors the mark to the app epoch, keeping it well clear of the safe-integer ceiling', () => {
    installCryptoMock()
    const next = createPersistedSeq(KEY_CHANNEL_RELAY_SEQ)
    const id = next()
    const mark = localStorageGet<number>(KEY_CHANNEL_RELAY_SEQ)
    expect(Number.isFinite(mark)).toBe(true)
    // Epoch-anchored: the mark is strictly below the raw wall clock (a raw
    // Date.now() seed would instead sit AT or above it). This is the headroom the
    // anchoring buys.
    expect(mark!).toBeGreaterThan(0)
    expect(mark!).toBeLessThan(Date.now())
    // MARK_LIMIT = floor(MAX_SAFE_INTEGER / (2^12)); the epoch-anchored mark sits
    // far below it, so the composed id is exact.
    const markLimit = Math.floor(Number.MAX_SAFE_INTEGER / 4096)
    expect(mark!).toBeLessThan(markLimit)
    expect(Number.isSafeInteger(id)).toBe(true)
  })
})
