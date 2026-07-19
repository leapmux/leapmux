// Chunked-message reassembly for the E2EE channel: the per-channel Reassembler state
// machine, its ChunkBuffer entries, the ReassemblyOutcome its accept() returns, and
// the wire-protocol limit constants both ends agree on. Extracted from channel.ts so
// the rule facing the least-trusted peer -- buffer / cap / poison / reap -- has a
// focused home and is unit-testable without driving the whole WebSocket path. It
// depends on nothing from ChannelManager, mirroring the Go reassemblers
// (backend/internal/worker/channel/chunker.go and backend/tunnel/channel.go).

// These three limits are a wire-protocol agreement with the Go implementation
// (backend/channelwire/wire.go). Both ends chunk and reassemble the same
// encrypted messages, so a receiver that disagrees silently rejects or mis-splits
// a legitimate one. They are exported so reassembler.test.ts can pin them against the
// shared testdata/channelwire_limits.json fixture, which the Go side asserts too.

/** Maximum plaintext bytes per Noise transport message (65535 - 16 byte auth tag). */
export const MAX_CHUNK_SIZE = 65535 - 16

/** Default maximum reassembled message size (16 MiB). */
export const DEFAULT_MAX_MESSAGE_SIZE = 16 * 1024 * 1024

/** Maximum number of in-flight chunked sequences per channel. */
export const MAX_INCOMPLETE_CHUNKED = 4

interface ChunkBuffer {
  parts: Uint8Array[]
  total: number
  /**
   * poisoned marks an id whose message already breached a limit and was errored to
   * its own request. Its remaining chunks are dropped rather than re-buffered or
   * re-reported: see the poisoned branch in handleMessage. The Go client of this
   * protocol keeps the same tombstone (backend/tunnel/channel.go's chunkBuffer).
   */
  poisoned: boolean
}

/**
 * What Reassembler.accept tells its caller to do with one decrypted chunk.
 * Mirrors the Go reassemblyAction enums (worker/channel/chunker.go and
 * tunnel/channel.go): a typed decision rather than a bag of optional fields, so
 * the dispatch in ChannelManager.reassemble reads as a switch over outcomes.
 * The two limit breaches are DISTINCT kinds ('too-many' for the in-flight cap,
 * 'too-large' for the size cap) so the dispatcher never string-matches a message
 * to tell them apart -- the Go worker carries the same split
 * (reassemblyTooManyIncomplete vs reassemblyTooLarge).
 */
export type ReassemblyOutcome
  = | { kind: 'deliver', plaintext: Uint8Array }
    | { kind: 'buffered' }
    | { kind: 'drop-poisoned' }
    | { kind: 'drop-unknown' }
    | { kind: 'too-many' }
    | { kind: 'too-large', size: number }

/**
 * Owns one channel's chunked-message reassembly buffers -- the in-flight ChunkBuffers
 * keyed by correlation id, plus the incomplete-chunked cap accounting. Extracted from
 * ChannelManager so the "how many sequences are live" invariant has a single home and
 * is unit-testable without driving the whole WebSocket path. Request rejection stays in
 * ChannelManager (it owns pendingRequests/streamListeners); this type owns only buffers.
 *
 * Production surface (ChannelManager calls these): `accept`, `poison`, `drop`, `clear`.
 * `start`, `append`, `get`, and `size` are the state-machine building blocks `accept`
 * composes plus read-only observers; they are public ONLY so reassembler.test.ts can
 * drive and assert the machine at each transition (e.g. `append`'s size boundary)
 * rather than through `accept`'s decision tree alone. Keep them public: narrowing
 * `start`/`append` while `get`/`size` must stay public for the same tests would trade
 * one inconsistency for a worse one and lose the granular boundary tests. This is a
 * deliberate test seam, not an over-broad API.
 */
export class Reassembler {
  private readonly buffers = new Map<number, ChunkBuffer>()

  constructor(
    private readonly maxMessageSize: number,
    private readonly maxIncomplete: number = MAX_INCOMPLETE_CHUNKED,
  ) {}

  get(correlationId: number): ChunkBuffer | undefined {
    return this.buffers.get(correlationId)
  }

  /** Total entries, tombstones included. `liveCount` is what the cap bounds; this is for observability/tests. */
  size(): number {
    return this.buffers.size
  }

  /**
   * The entries still accumulating bytes -- `buffers` minus its tombstones -- which,
   * not `size()`, is what MAX_INCOMPLETE_CHUNKED bounds. Derived by counting rather than
   * kept as a running total so it can NEVER drift from the map it describes. A tombstone
   * holds no bytes (poisoning releases its parts) and exists only so an errored id's
   * remaining chunks are recognised and dropped; letting one burn a cap slot would let
   * four protocol violations permanently reject every later chunked message on an
   * otherwise healthy channel -- the exact denial of service the cap prevents. The map is
   * bounded by that cap plus outstanding tombstones, so the O(n) scan is trivial. The Go
   * tunnel client derives the same count (backend/tunnel/channel.go).
   */
  liveCount(): number {
    let n = 0
    for (const buf of this.buffers.values()) {
      if (!buf.poisoned)
        n++
    }
    return n
  }

  /** Register a fresh accumulation buffer for `correlationId`. */
  start(correlationId: number): ChunkBuffer {
    const buf: ChunkBuffer = { parts: [], total: 0, poisoned: false }
    this.buffers.set(correlationId, buf)
    return buf
  }

  /**
   * Turn `correlationId`'s entry into a tombstone: release the bytes it accumulated but
   * KEEP the entry so the id's remaining chunks are recognised and silently dropped
   * rather than re-accumulated. Creates the entry when the id has none -- the over-cap
   * breach is detected before any buffer exists, yet still needs a tombstone. Idempotent.
   */
  poison(correlationId: number): void {
    let buf = this.buffers.get(correlationId)
    if (!buf) {
      buf = { parts: [], total: 0, poisoned: false }
      this.buffers.set(correlationId, buf)
    }
    buf.poisoned = true
    buf.parts = []
    buf.total = 0
    this.capTombstones(correlationId)
  }

  /**
   * Bound the number of tombstones so a peer that poisons ids but never sends their
   * terminal chunk cannot grow `buffers` without limit: `liveCount` -- the cap
   * `accept` enforces -- excludes tombstones, and a tombstone's only other reaper is
   * its terminal chunk, so nothing else collects an unterminated one. Mirrors the Go
   * worker's reassembler, which bounds tombstones for the identical reason -- it too
   * has no handler registry to reap them (backend/internal/worker/channel/chunker.go).
   *
   * The JS tombstone exists only to silence the log spam of a poisoned message's
   * remaining chunks (see ChannelManager.failReassembly); evicting the OLDEST when
   * the cap is exceeded -- Map preserves insertion order -- at worst restores that
   * spam for the message least likely to still have chunks in flight, and a later
   * stray chunk for the evicted id is dropped as unknown (its terminal chunk
   * deserialises to no live handler, so it is discarded, not misrouted).
   */
  private capTombstones(keep: number): void {
    let tombstones = 0
    for (const buf of this.buffers.values()) {
      if (buf.poisoned)
        tombstones++
    }
    while (tombstones > this.maxIncomplete) {
      let evicted = false
      for (const [id, buf] of this.buffers) {
        if (buf.poisoned && id !== keep) {
          this.buffers.delete(id)
          tombstones--
          evicted = true
          break
        }
      }
      // Only the just-poisoned `keep` remains a tombstone (cap is 0): nothing left
      // to evict, so stop rather than spin.
      if (!evicted)
        break
    }
  }

  /** Remove `correlationId`'s entry, live or tombstone. */
  drop(correlationId: number): void {
    this.buffers.delete(correlationId)
  }

  /** Drop every buffer (channel teardown). */
  clear(): void {
    this.buffers.clear()
  }

  /**
   * Append a chunk to an in-progress buffer, returning the new running total and whether it
   * now breaches maxMessageSize -- mirroring the Go sibling `ChunkBuffer.AppendChunk`, which
   * returns `(total, breached)` so the breach size is part of append's contract rather than a
   * `buf.total` value the caller reaches into after the call. Both the MORE and the final-chunk
   * path must apply the identical limit: a peer can split a message any way it likes, so a check
   * on only one path limits a framing, not the message. The caller uses `total` (not a re-read
   * of `buf.total`) to size the too-large outcome and the reassembled buffer.
   */
  append(buf: ChunkBuffer, chunk: Uint8Array): { total: number, breached: boolean } {
    buf.parts.push(chunk)
    buf.total += chunk.length
    return { total: buf.total, breached: buf.total > this.maxMessageSize }
  }

  /**
   * Advance the reassembly state machine by one decrypted chunk for correlationId
   * and return what the caller must do with it. `more` is the wire's MORE flag: a
   * terminal (non-MORE) chunk completes -- or, for a poisoned id, reaps -- the
   * sequence. `hasHandler` reports whether the caller has a live request/stream
   * registered for the id, so a first chunk for an id with no consumer is refused
   * rather than pinning maxMessageSize until the channel dies.
   *
   * This is the decision tree that used to live in ChannelManager.reassemble,
   * moved onto Reassembler so the rule facing the hostile peer is testable
   * without a WebSocket -- mirroring the Go worker's reassembler.accept
   * (backend/internal/worker/channel/chunker.go) and the tunnel client's
   * reassembleLocked (backend/tunnel/channel.go). Acting on the decision --
   * logging with channel context, rejecting the owning request -- stays with the
   * caller (ChannelManager.reassemble).
   *
   * A size or cap breach returns 'too-large' WITHOUT poisoning: the caller rejects
   * the owning request (which reaps its buffer) and THEN poisons, so the tombstone
   * survives to swallow the message's remaining chunks. JS is single-threaded, so
   * unlike the Go tunnel client there is no window between the decision and the
   * poison that would let chunks 2..N re-enter unrecognised.
   */
  accept(
    correlationId: number,
    chunk: Uint8Array,
    more: boolean,
    hasHandler: (id: number) => boolean,
  ): ReassemblyOutcome {
    const buffered = this.buffers.get(correlationId)

    // An id already errored for breaching a limit: drop the rest of its message
    // without re-buffering a byte, and reap the tombstone on its terminal chunk.
    if (buffered?.poisoned) {
      if (!more)
        this.drop(correlationId)
      return { kind: 'drop-poisoned' }
    }

    if (more) {
      let buf = buffered
      if (!buf) {
        // Reassembly state belongs to the request that will consume it. Every
        // inbound chunked message correlates to a request THIS side registered
        // (the worker initiates none), so a first chunk for an id with no live
        // handler can never be completed: buffering it would pin up to
        // maxMessageSize until the channel died. Checked BEFORE the cap so an
        // orphan never consumes a slot in the first place.
        if (!hasHandler(correlationId))
          return { kind: 'drop-unknown' }
        if (this.liveCount() >= this.maxIncomplete)
          return { kind: 'too-many' }
        buf = this.start(correlationId)
      }
      const { total, breached } = this.append(buf, chunk)
      if (!breached)
        return { kind: 'buffered' }
      return { kind: 'too-large', size: total }
    }

    // Terminal chunk, or a single non-chunked message.
    if (!buffered) {
      // The same orphan rule the MORE path applies: reassembly state belongs to
      // the request that will consume it, so a terminal chunk for an id with no
      // live handler -- a stray, a replay, or a reply whose caller already timed
      // out and unregistered -- has nobody to deliver to. Dropping it as unknown
      // skips deserializing a peer-authored plaintext whose consumer is already
      // gone. There is no memory to pin here (a terminal chunk buffers nothing),
      // so unlike the MORE path this is an efficiency guard, not a leak guard.
      if (!hasHandler(correlationId))
        return { kind: 'drop-unknown' }
      // Enforce the reassembled-message ceiling on the single-chunk path too, the
      // way the Go siblings do (worker chunker.go, tunnel channel.go): the ceiling
      // is a fixed protocol constant EVERY receiver enforces independently, and
      // this was the one browser path that leaned on the Hub's per-ciphertext cap
      // instead. MAX_CHUNK_SIZE bounds a single decrypted chunk far below
      // maxMessageSize today, so this never fires on legitimate traffic, but
      // enforcing it here makes the "every receiver enforces independently" claim
      // literally true and keeps an unbounded plaintext off the consumer if the
      // upstream cap ever relaxes.
      if (chunk.length > this.maxMessageSize)
        return { kind: 'too-large', size: chunk.length }
      return { kind: 'deliver', plaintext: chunk }
    }
    const { total, breached } = this.append(buffered, chunk)
    if (breached)
      return { kind: 'too-large', size: total }
    const full = new Uint8Array(total)
    let offset = 0
    for (const part of buffered.parts) {
      full.set(part, offset)
      offset += part.length
    }
    this.drop(correlationId)
    return { kind: 'deliver', plaintext: full }
  }
}
