// A persisted, monotonic id sequence whose ids are also unique across concurrent
// processes that share a sidecar.
//
// Both desktop relays (the channel relay via relayClaim, the userevents relay
// via useUserEvents) order their opens and closes by an id the Go sidecar
// compares, and the sidecar OUTLIVES a webview reload: a per-load counter
// would restart below the ids the previous page already handed out, and the
// fresh page's open -- the one that must win -- would be refused by the
// sidecar's owner fence as superseded. The persisted high-water mark keeps ids
// advancing across reloads instead, so a fresh page's open always supersedes
// the stale owner the sidecar still holds.
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
// The mark is a plain monotonic counter, NOT the wall clock. A clock-seeded
// mark carries a finite horizon -- the composed id packs mark * 2^TAB_BITS +
// random into a JS number, which must stay <= Number.MAX_SAFE_INTEGER to
// round-trip through Tauri IPC and the Rust u64 owner fence exactly, so a
// clock value eventually crosses the ceiling and silently corrupts the fence.
// A counter has no such horizon on its natural path: a fresh install starts at 1
// and advances by one per allocation, leaving ~2^41 headroom (MARK_LIMIT) --
// centuries at any realistic rate. A corrupted or hand-edited persisted value
// CAN sit above MARK_LIMIT, so the validation below re-seeds from 0 when it
// does; this closes the corruption class without reintroducing the clock. (The
// sidecar's relay ids are uint64 on the wire; the JS-number ceiling is purely a
// frontend concern.)
//
// One allocator serves both relays so a fix to the seeding rule cannot land on
// one and silently miss the other; each caller keeps its own storage key and
// therefore its own id space.

import { localStorageGet, localStorageSet } from './browserStorage'
import { createLogger } from './logger'

const log = createLogger('persistedSeq')

// TAB_BITS is the width of the per-process random low bits. The mark occupies
// the remaining high bits. 12 bits gives 4096 distinct process fingerprints
// (birthday collision at ~75 concurrent processes on one origin, far beyond any
// realistic desktop-app fan-out).
const TAB_BITS = 12
const TAB_MASK = (1 << TAB_BITS) - 1
// MARK_LIMIT is the largest mark whose composed id `mark * (TAB_MASK + 1) +
// processBits` still round-trips through Tauri IPC / the Rust u64 owner fence as
// an exact integer (<= Number.MAX_SAFE_INTEGER). It is the composition ceiling,
// NOT a clock horizon: a counter minted from 0 never approaches it (centuries of
// headroom), but a corrupted or hand-edited persisted value can sit above it, so
// the validation below re-seeds from 0 when it does. Coupled to TAB_MASK so a
// future bump of TAB_BITS cannot silently shrink the mark headroom. Division, not
// >>, because the operands exceed 32 bits and JS bitwise ops truncate to Int32.
const MARK_LIMIT = Math.floor(Number.MAX_SAFE_INTEGER / (TAB_MASK + 1))

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
 * Returns an allocator for `key`'s persisted monotonic sequence. The mark is
 * seeded lazily from the persisted value on the first call, and every allocated
 * id is persisted as the new high-water mark. The id carries a per-process
 * random in its low bits so two processes sharing the origin (same localStorage)
 * cannot mint the same id. The key must be registered in browserStorage's TTL
 * tables.
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
      // The seed must be (a) a real integer, not NaN/Infinity/a non-number, and
      // (b) within the composition range so the id `mark * stride + processBits`
      // stays an exact integer under MAX_SAFE_INTEGER. localStorageGet unwraps
      // the `{v, e}` cell via JSON.parse, so NaN/Infinity cannot actually arrive
      // (JSON has no such literals -- they surface as null, rejected by `typeof
      // === 'number'`); but a fractional, negative, or oversized value can. Once
      // a bad value reaches the mark it poisons the sequence for the install's
      // life: NaN makes the relay owner-fence (claimId > ownerId) always false,
      // so every open refuses itself; an oversized mark composes to an inexact
      // id that corrupts the fence (see MARK_LIMIT). Number.isSafeInteger &&
      // persisted >= 0 && persisted <= MARK_LIMIT rejects all of these, and the
      // counter only ever advances forward from a sound base. (Note: `-0`
      // satisfies all three -- `-0 === 0` -- but `-0 + 1 === 1` advances
      // forward correctly, and the JSON path cannot deliver it anyway since
      // JSON.stringify(-0) yields 0.) Anything rejected re-seeds from 0.
      mark = typeof persisted === 'number'
        && Number.isSafeInteger(persisted)
        && persisted >= 0
        && persisted <= MARK_LIMIT
        ? persisted
        : 0
    }
    mark++
    localStorageSet(key, mark)
    // localStorageSet swallows write errors (e.g. quota exceeded) silently, so
    // verify the mark landed: within this process the in-memory `mark` is
    // authoritative and only advances, so the ids minted this session are
    // correct regardless. The hazard is the NEXT reload, which reads the stale
    // lower persisted value and could mint an id below the owner the still-live
    // sidecar holds (the relay then refuses every open as superseded until an
    // app restart). The old clock-seeded scheme masked this with a clock floor
    // -- but at the cost of a clock-regression hole (a backward NTP step
    // between reloads seeded below the stale owner), so re-introducing the
    // clock is not a fix. Loud-logging makes the rare failure diagnosable.
    if (localStorageGet<number>(key) !== mark) {
      log.warn('relay-seq mark did not persist; reload after this session may wedge the relay until restart', { key })
    }
    // High bits = monotonic mark (reload-safe, shared across processes via
    // localStorage); low bits = per-process random (distinguishes concurrent
    // processes that read the same mark). Multiplication, not <<, because the
    // mark exceeds 32 bits and JS bitwise ops truncate to Int32.
    return mark * (TAB_MASK + 1) + processBits
  }
}
