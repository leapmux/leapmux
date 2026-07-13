// A persisted, clock-seeded, monotonic id sequence whose ids are also unique
// across concurrent processes that share a sidecar.
//
// Both desktop relays (the channel relay via relayClaim, the org-events relay
// via useOrgEvents) order their opens and closes by an id the Go sidecar
// compares, and the sidecar OUTLIVES a webview reload: a per-load counter
// would restart below the ids the previous page already handed out, and the
// fresh page's open -- the one that must win -- would be refused by the
// sidecar's owner fence as superseded. The wall clock alone is not monotonic
// either: a backward step (NTP, a manual adjustment) between two page loads
// would seed the fresh page BELOW the stale owner id the sidecar still holds,
// and every open would refuse itself until the clock caught back up. The
// persisted high-water mark keeps ids advancing through a regression; the
// clock is the seed only when the mark is absent (first run, cleared storage)
// or already behind it.
//
// Uniqueness across concurrent processes: the high-water mark is the ONLY
// shared state, persisted to localStorage -- and localStorage is shared by
// every process on the same origin (two Tauri windows, or two desktop apps on
// one machine). Two processes that read the same mark would compute the same
// id, and the sidecar's `current.owner > relayID` strict-greater tie-break
// would admit both, so one process's close tears down the other's relay -- the
// silent-wedge failure wsRelay.owner exists to prevent. The low TAB_BITS of
// each id are a per-process random differentiator generated in memory (NOT
// persisted), so two processes reading the same mark still mint distinct ids.
// The high bits (the persisted mark) stay monotonic across reloads, so a
// reload's open still supersedes the stale one regardless of the random low
// bits regenerating. Two processes on DIFFERENT machines have separate
// sidecars and separate id spaces, so they cannot collide at the fence -- but
// the per-process random makes the ids globally distinct anyway, which keeps
// logs and any future shared state unambiguous.
//
// One allocator serves both relays so a fix to the seeding rule cannot land on
// one and silently miss the other; each caller keeps its own storage key and
// therefore its own id space.

import { localStorageGet, localStorageSet } from './browserStorage'

// TAB_BITS is the width of the per-process random low bits. The mark occupies
// the remaining high bits and must stay below Number.MAX_SAFE_INTEGER once
// shifted, so the mark's useful range is 2^(53 - TAB_BITS). 12 bits gives
// 4096 distinct process fingerprints (birthday collision at ~75 concurrent
// processes on one origin, far beyond any realistic desktop-app fan-out) while
// leaving 41 bits for the mark -- enough for an epoch-anchored clock value
// (see APP_EPOCH_MS) into the 2090s.
const TAB_BITS = 12
const TAB_MASK = (1 << TAB_BITS) - 1
// MARK_LIMIT is the largest mark that fits under MAX_SAFE_INTEGER once placed
// in the high bits. Division, not >>, because the mark exceeds 32 bits and JS
// bitwise ops truncate to Int32. A persisted mark above this (a stale value
// from a prior scheme, or a corrupted cell) is re-seeded from the clock rather
// than producing an id that overflows the exact-integer range.
const MARK_LIMIT = Math.floor(Number.MAX_SAFE_INTEGER / (TAB_MASK + 1))

// APP_EPOCH_MS anchors the mark's clock component to a fixed recent instant rather
// than the Unix epoch, so `Date.now() - APP_EPOCH_MS` stays far below MARK_LIMIT for
// decades longer. A raw Date.now() (ms since 1970) crosses MARK_LIMIT (~2.199e12)
// around 2039 -- after which the overflow re-seed below can no longer bring the mark
// back under the limit and composed ids go inexact -- whereas the epoch-relative
// clock does not cross it until ~APP_EPOCH_MS + MARK_LIMIT, i.e. the 2090s. It must
// never change once shipped (a change would shift every future clock-seeded mark);
// the persisted high-water mark bridges the transition from any older scheme, since
// a larger stale value is carried forward by the Math.max below.
//
// This is a horizon EXTENSION, not a fix: the 2090s ceiling is still finite. Replacing
// the JS-number packing with an unbounded id (bigint / u64 end to end) so there is no
// horizon and no APP_EPOCH_MS to maintain is tracked in
// https://github.com/leapmux/leapmux/issues/298.
const APP_EPOCH_MS = 1_735_689_600_000 // 2025-01-01T00:00:00Z

// epochClock is the mark's clock component: milliseconds since APP_EPOCH_MS, floored
// at 0 so a device clock set before the epoch cannot seed a negative mark.
function epochClock(): number {
  return Math.max(0, Date.now() - APP_EPOCH_MS)
}

// randomLowBits returns a uniformly random integer in [0, 2^TAB_BITS), sourced
// from the platform CSPRNG when available. window.crypto.getRandomValues is
// universal in the desktop webview and every browser target; the Math.random
// fallback keeps the allocator usable in a jsdom test environment that does
// not wire up crypto.
function randomLowBits(): number {
  const crypto = globalThis.crypto
  if (crypto && typeof crypto.getRandomValues === 'function') {
    // Uint16Array gives 16 bits of randomness; mask down to TAB_BITS.
    return crypto.getRandomValues(new Uint16Array(1))[0] & TAB_MASK
  }
  return Math.floor(Math.random() * (TAB_MASK + 1))
}

/**
 * Returns an allocator for `key`'s persisted monotonic sequence. The seed is
 * computed lazily on the first call (`max(epochClock(), persisted mark)`), and
 * every allocated id is persisted as the new high-water mark. The id carries a
 * per-process random in its low bits so two processes sharing the origin
 * (same localStorage) cannot mint the same id. The key must be registered in
 * browserStorage's TTL tables.
 */
export function createPersistedSeq(key: string): () => number {
  let mark: number | null = null
  // Generated once per allocator (per relay type, per process) so every id a
  // single process mints shares the same fingerprint and advances monotonically
  // with the mark in the high bits.
  const processBits = randomLowBits()
  return () => {
    if (mark === null) {
      const persisted = localStorageGet<number>(key)
      // Number.isFinite rather than `typeof === 'number'`: NaN is typeof
      // 'number', and once NaN reaches the seed (Math.max(Date.now(), NaN) ===
      // NaN) every later mark++ stays NaN -- the allocator hands out NaN for
      // the install's life, and the relay owner-fence (claimId > ownerId) is
      // always false against NaN, so every open refuses itself until the key is
      // manually cleared. A corrupted or hand-edited storage value, or any
      // future caller that computes the seed arithmetically, could land NaN
      // here; isFinite rejects NaN, Infinity, and non-numbers in one check.
      // `typeof persisted === 'number'` narrows the number | undefined that
      // localStorageGet returns to a number, and the isFinite check then rejects
      // NaN (which is itself typeof 'number') per the hazard above -- so seed is
      // always a real number and Math.max cannot produce NaN.
      const seed = typeof persisted === 'number' && Number.isFinite(persisted) ? persisted : 0
      mark = Math.max(epochClock(), seed)
      // A persisted mark above MARK_LIMIT would overflow MAX_SAFE_INTEGER once
      // shifted into the high bits. Re-seed from the epoch clock so the id stays
      // within the exact-integer range the wire boundary requires. This path
      // also reclaims the id space after a backward clock step that left a
      // stale high-water mark above the current clock-derived ceiling.
      if (mark > MARK_LIMIT) {
        mark = epochClock()
      }
    }
    mark++
    localStorageSet(key, mark)
    // High bits = monotonic mark (reload-safe, shared across processes via
    // localStorage); low bits = per-process random (distinguishes concurrent
    // processes that read the same mark). Multiplication, not <<, because the
    // mark exceeds 32 bits and JS bitwise ops truncate to Int32. The result fits
    // under Number.MAX_SAFE_INTEGER while mark <= MARK_LIMIT, which the
    // epoch-anchored clock keeps true into the 2090s (see APP_EPOCH_MS); a
    // persisted mark past MARK_LIMIT is reclaimed by the re-seed above.
    return mark * (TAB_MASK + 1) + processBits
  }
}
