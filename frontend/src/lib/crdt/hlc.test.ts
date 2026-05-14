import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { HLCSchema } from '~/generated/leapmux/v1/org_crdt_pb'
import { HLCClock, hlcCmp, hlcIsZero } from './hlc'

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

describe('hLCClock', () => {
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
