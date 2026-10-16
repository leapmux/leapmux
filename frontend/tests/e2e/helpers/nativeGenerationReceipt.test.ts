import type { LogLine } from './timingFixture'
import { describe, expect, it } from 'vitest'
import { GENERATION_TRACE_FIELD as FIELD, GENERATION_TRACE_MARKER } from '../../../src/generated/contracts/generation-trace'
import { hasNativeGenerationReceipt } from './nativeGenerationReceipt'

const receipt: LogLine = {
  raw: '',
  rxAt: 1,
  json: {
    marker: GENERATION_TRACE_MARKER,
    [FIELD.Scope]: 'native-message',
    [FIELD.Kind]: 'text',
    [FIELD.SHA256]: 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad',
    [FIELD.Bytes]: 3,
  },
}

describe('hasNativeGenerationReceipt', () => {
  it('matches the known SHA-256 vector and its exact byte count', () => {
    expect(hasNativeGenerationReceipt([{ raw: 'banner', json: null, rxAt: 0 }, receipt], 'abc')).toBe(true)
    expect(hasNativeGenerationReceipt([receipt], 'ab')).toBe(false)
    expect(hasNativeGenerationReceipt([receipt], 'abcd')).toBe(false)
  })

  it.each([
    { marker: 'other' },
    { [FIELD.Scope]: '' },
    { [FIELD.Scope]: null },
    { [FIELD.Kind]: 'reasoning' },
    { [FIELD.SHA256]: '' },
    { [FIELD.Bytes]: 0 },
    { [FIELD.Bytes]: '3' },
  ])('rejects an incorrect native receipt field: %j', (change) => {
    expect(hasNativeGenerationReceipt([{ ...receipt, json: { ...receipt.json, ...change } }], 'abc')).toBe(false)
  })

  it('rejects empty text rather than accepting an empty-stream receipt', () => {
    expect(() => hasNativeGenerationReceipt([], '')).toThrow('nonempty text')
  })
})
