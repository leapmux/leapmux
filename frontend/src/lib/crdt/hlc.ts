import type { HLC } from '~/generated/leapmux/v1/user_crdt_pb'
import { create } from '@bufbuild/protobuf'
import { HLCSchema } from '~/generated/leapmux/v1/user_crdt_pb'

/** Compare two HLC values lex by (physical, logical, client_id). */
export function hlcCmp(a: HLC | undefined | null, b: HLC | undefined | null): number {
  if (!a && !b)
    return 0
  if (!a)
    return -1
  if (!b)
    return 1
  if (a.physical < b.physical)
    return -1
  if (a.physical > b.physical)
    return 1
  if (a.logical < b.logical)
    return -1
  if (a.logical > b.logical)
    return 1
  if (a.clientId < b.clientId)
    return -1
  if (a.clientId > b.clientId)
    return 1
  return 0
}

/** Reports whether an HLC is unset (undefined / null / all-zero). */
export function hlcIsZero(h: HLC | undefined | null): boolean {
  if (!h)
    return true
  return h.physical === 0n && h.logical === 0n && h.clientId === ''
}

/**
 * Structural HLC shape — accepts a plain object without the proto `$typeName`
 * brand. Used by callers that hold only the three wire fields (e.g. a
 * checkpoint watermark deserialized from IDB structured clone, which never
 * carries the proto brand). Equivalent to the structural shape `formatHlcWire`
 * already accepts. The proto `HLC` type satisfies this shape structurally, so
 * `HlcShape` is the canonical parameter type for HLC helpers that don't need
 * the brand (hlcClone, formatHlcWire).
 */
export interface HlcShape { physical: bigint, logical: bigint, clientId: string }

/**
 * Deep-clone an HLC. Accepts the structural `HlcShape` (which the proto `HLC`
 * satisfies), so callers holding either a branded proto HLC or a plain object
 * literal (e.g. a checkpoint watermark from IDB structured clone) share one
 * clone path.
 */
export function hlcClone(h: HlcShape | undefined | null): HLC | undefined {
  if (!h)
    return undefined
  return create(HLCSchema, { physical: h.physical, logical: h.logical, clientId: h.clientId })
}

/**
 * Format an HLC as the "<physical>.<logical>.<client_id>" wire string the
 * Go side parses (see channelwire.EncodeResumeHLC). The single canonical
 * author of the resume_after_hlc query-param / sidecar-RPC shape — every TS
 * callsite that puts an HLC on the wire goes through here so the format can't
 * drift between the URL builder, the desktop bridge, and the persisted
 * watermark.
 *
 * Accepts the minimal structural shape (not the full proto `HLC` type) so a
 * callsite that holds only the three wire fields — e.g. the desktop bridge's
 * Tauri-IPC payload, which never carries the proto `$typeName` — can format
 * without an `as never` cast.
 */
export function formatHlcWire(hlc: { physical: bigint, logical: bigint, clientId: string }): string {
  // Guard against a corrupted input whose clientId is undefined/null: the
  // template literal would otherwise stringify it as the literal "undefined"
  // / "null", which parseHlcWire accepts (non-empty, ≤128 chars) and the hub's
  // DecodeResumeHLC mirrors — silently shipping a cursor with a bogus client_id
  // that mis-filters the journal scan. proto3 normalizes an unset string to "",
  // which parseHlcWire rejects, so this only fires for non-proto paths; treat
  // it as a programmer error (throw) rather than silently degrading.
  if (typeof hlc.clientId !== 'string')
    throw new TypeError('formatHlcWire: clientId must be a string')
  return `${hlc.physical}.${hlc.logical}.${hlc.clientId}`
}

/**
 * Parse the "<physical>.<logical>.<client_id>" wire string back into an HLC.
 * Returns undefined on any malformed shape (missing delimiters, non-numeric
 * physical/logical, empty client id) — never throws — so a corrupted persisted
 * value degrades to "no watermark" (a full-snapshot reconnect) rather than
 * crashing the socket-open effect.
 *
 * Mirrors the Go `channelwire.DecodeResumeHLC` rules EXACTLY so the round-trip
 * cursor validation at useUserEvents (`parseHlcWire(formatHlcWire(sourceHlc))`)
 * drops anything the hub would reject — otherwise a corrupted persisted value
 * passes client validation, the hub returns 400, the browser surfaces a
 * non-terminal 1006 close, and the client reconnects in a tight loop (the
 * exact storm the validation exists to prevent). The mirrored rules:
 *   - physical/logical must be base-10 integers within int64 range (BigInt is
 *     unbounded; Go's strconv.ParseInt(..., 10, 64) overflows and rejects);
 *   - physical must be > 0 (matches Go's `physical <= 0` rejection);
 *   - logical must be >= 0 (matches Go's `logical < 0` rejection);
 *   - clientId must be non-empty and ≤ 128 BYTES under UTF-8 (matches Go's
 *     `len(clientID) > 128`, which counts bytes -- NOT `.length`, which counts
 *     UTF-16 code units and diverges for every non-ASCII id).
 */
export function parseHlcWire(raw: string): HLC | undefined {
  // `indexOf` returns an absolute index. The first dot must be non-leading
  // (a non-empty physical segment); the second dot must be STRICTLY after the
  // first (a non-empty logical segment). d2 === d1 + 1 means an empty logical
  // segment ("100..c") — reject it explicitly rather than relying on BigInt("")
  // throwing, so the guard is not dead code checking the wrong condition.
  const d1 = raw.indexOf('.')
  if (d1 <= 0)
    return undefined
  const d2 = raw.indexOf('.', d1 + 1)
  // d2 is an absolute index. No second dot → d2 === -1 → reject. Empty logical
  // segment ("100..c") → d2 === d1 + 1 → reject explicitly, mirroring Go's
  // relative `dot2 <= 0` check (Go slices raw[dot1+1:] first, so its `dot2`
  // for an empty logical segment is 0). BigInt("") returns 0n (does not throw)
  // in this engine, so without this guard the empty segment would silently
  // parse as logical=0 — a value Go rejects, reopening the reconnect storm.
  if (d2 < 0 || d2 <= d1 + 1)
    return undefined
  const physicalRaw = raw.slice(0, d1)
  const logicalRaw = raw.slice(d1 + 1, d2)
  // Mirror the STRICTNESS of the Go decoder, not just its range. BigInt is more
  // permissive than the wire format in two ways: it accepts surrounding
  // whitespace (" 123" → 123n) and a leading `+` ("+123" → 123n). Go's
  // strconv.ParseInt rejects the whitespace but ACCEPTS the `+`, so neither
  // engine's default is the contract — both sides converge on this regex
  // instead: an optional leading `-`, then bare digits. (`channelwire`
  // rejects `+` explicitly for the same reason; see plusSigned there.)
  //
  // The sign is allowed through here so the `<= 0` / `< 0` range checks below
  // remain the ONLY sign rejection, keeping one reason-for-rejection per rule.
  // Leading zeros ("0012") are accepted by both sides and stay accepted.
  // Pinned from both languages by testdata/hlc_wire_corpus.json.
  if (!/^-?\d+$/.test(physicalRaw) || !/^-?\d+$/.test(logicalRaw))
    return undefined
  let physical: bigint
  let logical: bigint
  try {
    physical = BigInt(physicalRaw)
    logical = BigInt(logicalRaw)
  }
  catch {
    return undefined
  }
  // Mirror Go's strconv.ParseInt(..., 10, 64): reject values outside int64
  // range, so a corrupted > int64 field cannot round-trip through TS and then
  // fail the hub's int64-bounded decoder.
  if (!inInt64Range(physical) || !inInt64Range(logical))
    return undefined
  if (physical <= 0n || logical < 0n)
    return undefined
  const clientId = raw.slice(d2 + 1)
  // BYTES, not UTF-16 code units. Go's `len(clientID) > 128` counts bytes, so
  // `clientId.length` diverged for any non-ASCII id: ~60 CJK characters is 60
  // units but 180 bytes, which passed here and 400'd at the hub -- and since
  // 1006 is not a terminal close code, the client would reconnect with the same
  // persisted cursor and 400 again, forever. That is precisely the storm this
  // round-trip validation exists to prevent, so the doc's claim to mirror the Go
  // rules EXACTLY has to hold for every input, not just ASCII ones.
  if (!clientId || utf8Length(clientId) > 128)
    return undefined
  return create(HLCSchema, { physical, logical, clientId })
}

/**
 * Byte length of `s` under UTF-8 — what Go's `len(string)` returns.
 *
 * Shared encoder instance: this runs on the socket-open path and TextEncoder
 * construction is not free.
 */
const utf8Encoder = new TextEncoder()
function utf8Length(s: string): number {
  return utf8Encoder.encode(s).length
}

// INT64_MIN/MAX as bigints, for the int64-range check that mirrors Go's
// strconv.ParseInt(..., 10, 64). BigInt itself is unbounded; without this a
// corrupted persisted value outside int64 range round-trips through TS and then
// fails the hub's decoder.
const INT64_MIN = -(2n ** 63n)
const INT64_MAX = 2n ** 63n - 1n

/** True when `n` fits in a signed int64 (Go strconv.ParseInt bitSize 64). */
export function inInt64Range(n: bigint): boolean {
  return n >= INT64_MIN && n <= INT64_MAX
}

/**
 * Return the lex-greater of two HLCs (undefined if both are). Used to advance
 * the resume watermark: it is a max over the canonical HLCs applied, so it
 * monotonically tracks how far confirmedState has caught up.
 */
export function hlcMax(a: HLC | undefined, b: HLC | undefined): HLC | undefined {
  if (!a)
    return b ? hlcClone(b) : undefined
  if (!b)
    return hlcClone(a)
  return hlcCmp(a, b) >= 0 ? hlcClone(a) : hlcClone(b)
}

/**
 * HLCClock is a hybrid logical clock. The frontend mints `client_hlc`
 * advisory hints with this clock; the canonical HLCs come from the
 * hub on echo, and the clock absorbs them via observe().
 */
export class HLCClock {
  private maxPhys = 0n
  private maxLog = 0n

  constructor(public readonly clientId: string) {}

  tick(now?: number): HLC {
    const nowMs = BigInt(now ?? Date.now())
    if (nowMs > this.maxPhys) {
      this.maxPhys = nowMs
      this.maxLog = 0n
    }
    else {
      this.maxLog++
    }
    return create(HLCSchema, {
      physical: this.maxPhys,
      logical: this.maxLog,
      clientId: this.clientId,
    })
  }

  observe(remote: HLC | undefined | null): void {
    if (!remote)
      return
    const rp = remote.physical
    const rl = remote.logical
    if (rp > this.maxPhys) {
      this.maxPhys = rp
      this.maxLog = rl
      return
    }
    if (rp === this.maxPhys && rl > this.maxLog) {
      this.maxLog = rl
    }
  }

  current(): HLC {
    return create(HLCSchema, {
      physical: this.maxPhys,
      logical: this.maxLog,
      clientId: this.clientId,
    })
  }
}
