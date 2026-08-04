import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { HLCSchema } from '~/generated/leapmux/v1/user_crdt_pb'
import { formatHlcWire, HLCClock, hlcCmp, hlcIsZero, parseHlcWire } from './hlc'

function hlc(physical: bigint, logical: bigint, clientId: string) {
  return create(HLCSchema, { physical, logical, clientId })
}

describe('hlcCmp', () => {
  it('orders by physical first', () => {
    expect(hlcCmp(hlc(10n, 0n, 'a'), hlc(11n, 0n, 'a'))).toBe(-1)
    expect(hlcCmp(hlc(11n, 0n, 'a'), hlc(10n, 0n, 'a'))).toBe(1)
  })
  it('orders by logical when physical ties', () => {
    expect(hlcCmp(hlc(10n, 0n, 'a'), hlc(10n, 1n, 'a'))).toBe(-1)
  })
  it('orders by client_id when (physical, logical) tie', () => {
    expect(hlcCmp(hlc(10n, 0n, 'alpha'), hlc(10n, 0n, 'bravo'))).toBe(-1)
  })
  it('returns 0 on equal hlc', () => {
    expect(hlcCmp(hlc(10n, 0n, 'a'), hlc(10n, 0n, 'a'))).toBe(0)
  })
  it('treats undefined as smaller than anything', () => {
    expect(hlcCmp(undefined, hlc(0n, 0n, ''))).toBe(-1)
    expect(hlcCmp(hlc(0n, 0n, ''), undefined)).toBe(1)
    expect(hlcCmp(undefined, undefined)).toBe(0)
  })
})

describe('hlcIsZero', () => {
  it('true on undefined / null', () => {
    expect(hlcIsZero(undefined)).toBe(true)
    expect(hlcIsZero(null)).toBe(true)
  })
  it('true on all-zero', () => {
    expect(hlcIsZero(hlc(0n, 0n, ''))).toBe(true)
  })
  it('false on any non-zero field', () => {
    expect(hlcIsZero(hlc(1n, 0n, ''))).toBe(false)
    expect(hlcIsZero(hlc(0n, 1n, ''))).toBe(false)
    expect(hlcIsZero(hlc(0n, 0n, 'a'))).toBe(false)
  })
})

describe('class HLCClock', () => {
  it('tick produces strictly increasing HLCs (logical bumps within same physical)', () => {
    const c = new HLCClock('client-1')
    const a = c.tick(100)
    const b = c.tick(100)
    expect(a.physical).toBe(100n)
    expect(a.logical).toBe(0n)
    expect(b.physical).toBe(100n)
    expect(b.logical).toBe(1n)
  })
  it('tick resets logical when physical advances', () => {
    const c = new HLCClock('client-1')
    c.tick(100)
    const next = c.tick(200)
    expect(next.physical).toBe(200n)
    expect(next.logical).toBe(0n)
  })
  it('observe absorbs a remote HLC and the next tick is strictly greater', () => {
    const c = new HLCClock('client-1')
    c.tick(100)
    c.observe(hlc(500n, 7n, 'other'))
    const next = c.tick(100)
    expect(next.physical).toBe(500n)
    expect(next.logical).toBe(8n)
  })
})

describe('formatHlcWire / parseHlcWire', () => {
  // The load-bearing property is that parseHlcWire mirrors the Go
  // channelwire.DecodeResumeHLC rules EXACTLY. The round-trip cursor
  // validation at useUserEvents (`parseHlcWire(formatHlcWire(sourceHlc))`)
  // exists to drop a malformed persisted value before it reaches the hub as a
  // 400 the browser surfaces as a non-terminal 1006 close (→ infinite
  // reconnect storm). If TS accepts a value the Go decoder rejects, the guard
  // is bypassed and the storm returns. These cases pin every Go rejection rule
  // the prior TS parser was missing (logical sign, clientId length, int64
  // range) plus the empty-segment guard.
  it('round-trips a well-formed HLC', () => {
    const original = hlc(1754100000000n, 42n, 'c-abc')
    const wire = formatHlcWire(original)
    expect(wire).toBe('1754100000000.42.c-abc')
    const parsed = parseHlcWire(wire)
    expect(parsed).toBeDefined()
    expect(parsed!.physical).toBe(1754100000000n)
    expect(parsed!.logical).toBe(42n)
    expect(parsed!.clientId).toBe('c-abc')
  })

  it('formatHlcWire accepts the minimal structural shape (no proto $typeName)', () => {
    // The desktop bridge holds only {physical, logical, clientId}; the helper
    // must not require the full proto HLC type.
    const wire = formatHlcWire({ physical: 5n, logical: 1n, clientId: 'c' })
    expect(wire).toBe('5.1.c')
  })

  it('parseHlcWire rejects a negative logical (mirrors Go logical < 0)', () => {
    // Pre-fix this passed TS validation and was rejected by the hub → storm.
    expect(parseHlcWire('100.-5.c')).toBeUndefined()
  })

  it('parseHlcWire rejects a clientId longer than 128 chars (mirrors Go bound)', () => {
    expect(parseHlcWire(`100.0.${'x'.repeat(129)}`)).toBeUndefined()
    // 128 is the boundary: accepted.
    expect(parseHlcWire(`100.0.${'x'.repeat(128)}`)).toBeDefined()
  })

  it('parseHlcWire rejects physical/logical outside int64 range (mirrors Go ParseInt)', () => {
    // BigInt is unbounded; Go's strconv.ParseInt(..., 10, 64) overflows.
    const over = 2n ** 63n // int64 max + 1
    expect(parseHlcWire(`${over}.0.c`)).toBeUndefined()
    expect(parseHlcWire(`100.${over}.c`)).toBeUndefined()
  })

  it('parseHlcWire rejects an empty logical segment ("100..c")', () => {
    // Pins the fixed d2 guard: previously d2<=0 was dead code (absolute index)
    // and this was saved only by BigInt("") throwing.
    expect(parseHlcWire('100..c')).toBeUndefined()
  })

  it('parseHlcWire never throws on garbage (corrupted persisted value)', () => {
    // A corrupted persisted watermark must degrade to undefined, not throw out
    // of the socket-open effect.
    expect(() => parseHlcWire('not.an.hlc.at.all')).not.toThrow()
    expect(() => parseHlcWire('abc.def')).not.toThrow()
    expect(() => parseHlcWire('')).not.toThrow()
    expect(parseHlcWire('garbage')).toBeUndefined()
  })
})

// Cross-language conformance corpus: parseHlcWire must agree with the Go hub's
// channelwire.DecodeResumeHLC on every case in testdata/hlc_wire_corpus.json.
// Both decoders parse the SAME resume-cursor wire format, and the client
// pre-validates a persisted cursor before sending so a value the hub would 400
// never leaves the browser (otherwise: non-terminal 1006 close → tight reconnect
// loop). A rule added/tightened on one side but not the other reddens CI here
// (and in backend/channelwire/wire_test.go) instead of drifting back into that
// storm. Mirrors the cross-language pattern channel.wire-limits.test.ts uses.
describe('parseHlcWire cross-language corpus', () => {
  const data = JSON.parse(
    readFileSync(
      resolve(dirname(fileURLToPath(import.meta.url)), '../../../../testdata/hlc_wire_corpus.json'),
      'utf8',
    ),
  ) as { cases: Array<{ raw: string, ok: boolean, physical?: string, logical?: string, clientId?: string }> }

  for (const c of data.cases) {
    it(`parses ${JSON.stringify(c.raw)} -> ${c.ok ? 'ok' : 'reject'}`, () => {
      const got = parseHlcWire(c.raw)
      if (!c.ok) {
        expect(got).toBeUndefined()
        return
      }
      expect(got).toBeDefined()
      expect(got!.physical).toBe(BigInt(c.physical!))
      expect(got!.logical).toBe(BigInt(c.logical!))
      expect(got!.clientId).toBe(c.clientId)
    })
  }
})
