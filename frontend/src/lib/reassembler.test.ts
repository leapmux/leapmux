import { describe, expect, it } from 'vitest'
import { Reassembler } from './reassembler'

describe('reassembler', () => {
  const chunk = (n: number) => new Uint8Array(n)

  it('derives liveCount from the map so it cannot drift from the entries', () => {
    const r = new Reassembler(1000)
    expect(r.liveCount()).toBe(0)
    expect(r.size()).toBe(0)

    r.start(1)
    r.start(2)
    expect(r.liveCount()).toBe(2)
    expect(r.size()).toBe(2)

    // Poisoning tombstones the entry: it stays in the map (so the id's remaining
    // chunks are recognised) but no longer counts toward the live cap.
    r.poison(1)
    expect(r.liveCount()).toBe(1)
    expect(r.size()).toBe(2)
    expect(r.get(1)?.poisoned).toBe(true)

    // Dropping removes it from both.
    r.drop(1)
    expect(r.liveCount()).toBe(1)
    expect(r.size()).toBe(1)

    r.drop(2)
    expect(r.liveCount()).toBe(0)
    expect(r.size()).toBe(0)
  })

  it('poison is idempotent and creates a tombstone even for an unseen id', () => {
    const r = new Reassembler(1000)
    r.poison(7) // over-cap breach can poison before any buffer exists
    expect(r.size()).toBe(1)
    expect(r.liveCount()).toBe(0)
    r.poison(7)
    expect(r.liveCount()).toBe(0)
    expect(r.size()).toBe(1)
  })

  it('bounds tombstones so an unterminated poisoned id cannot grow the map without limit', () => {
    // liveCount (the cap accept enforces) excludes tombstones, and a tombstone's only
    // other reaper is its terminal chunk, so a peer that poisons many ids but never
    // terminates them would grow the map for the channel's life without a bound --
    // mirroring the Go worker's tombstone cap (worker/channel/chunker.go).
    const r = new Reassembler(1000, 2)
    for (let id = 1; id <= 20; id++)
      r.poison(id)

    expect(r.size()).toBe(2) // capped at maxIncomplete regardless of ids poisoned
    expect(r.liveCount()).toBe(0)
    // Eviction keeps the most recently poisoned ids (whose chunks are most likely
    // still in flight) and drops the oldest.
    expect(r.get(20)?.poisoned).toBe(true)
    expect(r.get(19)?.poisoned).toBe(true)
    expect(r.get(1)).toBeUndefined()
  })

  it('capping tombstones does not evict a live buffer', () => {
    // Only tombstones are bounded: two live sequences plus many poisoned ids must
    // leave both live buffers intact (they are what maxMessageSize protects), with
    // only the tombstones capped.
    const r = new Reassembler(1000, 2)
    r.start(100)
    r.start(200)
    for (let id = 1; id <= 10; id++)
      r.poison(id)

    expect(r.liveCount()).toBe(2)
    expect(r.get(100)?.poisoned).toBe(false)
    expect(r.get(200)?.poisoned).toBe(false)
  })

  it('append accumulates and reports a breach once the total exceeds the limit', () => {
    const r = new Reassembler(100)
    const buf = r.start(1)
    expect(r.append(buf, chunk(60))).toEqual({ total: 60, breached: false })
    expect(buf.total).toBe(60)
    // 60 + 41 = 101 > 100 -> over the limit, and the breach size rides the return
    // value (mirroring the Go sibling AppendChunk) so the caller does not re-read
    // buf.total to build the too-large outcome.
    expect(r.append(buf, chunk(41))).toEqual({ total: 101, breached: true })
    expect(buf.total).toBe(101)
  })

  // accept is the decision tree that used to live on ChannelManager.reassemble.
  // Exercising it directly (no WebSocket, no ChannelManager) is the point of the
  // move: the rule facing the hostile peer is now testable in isolation, mirroring
  // the Go reassembler tests (worker/channel/chunker_test.go, tunnel/channel_test.go).
  const MORE = true
  const TERMINAL = false
  const alwaysHandled = () => true
  const neverHandled = () => false

  it('accept delivers a single non-chunked message without taking a slot', () => {
    const r = new Reassembler(100, 4)
    const out = r.accept(1, chunk(5), TERMINAL, alwaysHandled)
    expect(out).toEqual({ kind: 'deliver', plaintext: expect.any(Uint8Array) })
    expect((out as { plaintext: Uint8Array }).plaintext.length).toBe(5)
    expect(r.size()).toBe(0)
  })

  it('accept enforces maxMessageSize on the single-chunk path too, not just mid-sequence', () => {
    // The one path that used to lean on the Hub's per-ciphertext cap: a single
    // non-MORE chunk whose plaintext exceeds maxMessageSize. Every receiver must
    // enforce the ceiling independently (worker chunker.go, tunnel channel.go), so
    // an oversize single chunk is reported as 'too-large' and never delivered.
    const r = new Reassembler(4, 4)
    const out = r.accept(1, chunk(5), TERMINAL, alwaysHandled)
    expect(out.kind).toBe('too-large')
    expect((out as { size: number }).size).toBe(5)
    // Nothing was buffered, so no slot is taken -- mirroring the Go worker, whose
    // single-chunk too-large path returns without tombstoning.
    expect(r.size()).toBe(0)
  })

  it('accept still delivers a single chunk exactly at the size limit', () => {
    const r = new Reassembler(4, 4)
    const out = r.accept(1, chunk(4), TERMINAL, alwaysHandled)
    expect(out.kind).toBe('deliver')
    expect((out as { plaintext: Uint8Array }).plaintext.length).toBe(4)
  })

  it('accept buffers MORE chunks and delivers the joined terminal chunk', () => {
    const r = new Reassembler(100, 4)
    expect(r.accept(1, new Uint8Array([1, 2]), MORE, alwaysHandled).kind).toBe('buffered')
    expect(r.accept(1, new Uint8Array([3]), MORE, alwaysHandled).kind).toBe('buffered')
    const out = r.accept(1, new Uint8Array([4, 5]), TERMINAL, alwaysHandled)
    expect(out.kind).toBe('deliver')
    expect(Array.from((out as { plaintext: Uint8Array }).plaintext)).toEqual([1, 2, 3, 4, 5])
    expect(r.size()).toBe(0)
  })

  it('accept drops a first chunk for an id with no live handler rather than pinning a slot', () => {
    const r = new Reassembler(100, 4)
    const out = r.accept(1, chunk(5), MORE, neverHandled)
    expect(out.kind).toBe('drop-unknown')
    expect(r.size()).toBe(0)
  })

  it('accept drops a terminal chunk for an id with no live handler rather than deserializing it', () => {
    const r = new Reassembler(100, 4)
    const out = r.accept(1, chunk(5), TERMINAL, neverHandled)
    expect(out.kind).toBe('drop-unknown')
    expect(r.size()).toBe(0)
  })

  it('accept refuses a new sequence past the live cap', () => {
    const r = new Reassembler(100, 2)
    expect(r.accept(1, chunk(1), MORE, alwaysHandled).kind).toBe('buffered')
    expect(r.accept(2, chunk(1), MORE, alwaysHandled).kind).toBe('buffered')
    // Live cap full: a third NEW sequence is refused as 'too-many' (the cap breach),
    // distinct from a size breach, and no buffer is taken (the caller rejects +
    // tombstones).
    const out = r.accept(3, chunk(1), MORE, alwaysHandled)
    expect(out.kind).toBe('too-many')
    expect(r.liveCount()).toBe(2)
  })

  it('accept reports a size breach mid-sequence without poisoning (the caller owns that)', () => {
    const r = new Reassembler(4, 4)
    expect(r.accept(1, chunk(3), MORE, alwaysHandled).kind).toBe('buffered')
    const out = r.accept(1, chunk(3), MORE, alwaysHandled)
    expect(out.kind).toBe('too-large')
    expect((out as { size: number }).size).toBe(6)
    // accept leaves the buffer in place; the ChannelManager dispatcher rejects the
    // request (reaping it) and then poisons. Either way the next chunk for the id
    // is recognised once tombstoned.
  })

  it('accept reaps a poisoned id on its terminal chunk and drops its MORE chunks', () => {
    const r = new Reassembler(100, 4)
    expect(r.accept(1, chunk(1), MORE, alwaysHandled).kind).toBe('buffered')
    r.poison(1)
    // Remaining MORE chunks for the poisoned id are dropped, not re-buffered.
    expect(r.accept(1, chunk(1), MORE, alwaysHandled).kind).toBe('drop-poisoned')
    // The terminal chunk reaps the tombstone.
    expect(r.accept(1, chunk(1), TERMINAL, alwaysHandled).kind).toBe('drop-poisoned')
    expect(r.size()).toBe(0)
    // The id is free to start a fresh sequence.
    expect(r.accept(1, chunk(1), MORE, alwaysHandled).kind).toBe('buffered')
  })
})
