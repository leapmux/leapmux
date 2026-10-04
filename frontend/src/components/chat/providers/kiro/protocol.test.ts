import { describe, expect, it } from 'vitest'
import { kiroMeta } from './protocol'

describe('kiroMeta', () => {
  it('reads the kiro object of a frame\'s metadata', () => {
    expect(kiroMeta({ _meta: { kiro: { kind: 'turn_end' }, other: 1 } })).toEqual({ kind: 'turn_end' })
  })

  it('answers undefined for a frame with no kiro object', () => {
    expect(kiroMeta(undefined)).toBeUndefined()
    expect(kiroMeta(null)).toBeUndefined()
    expect(kiroMeta({})).toBeUndefined()
    expect(kiroMeta({ _meta: 'x' })).toBeUndefined()
    expect(kiroMeta({ _meta: { kiro: 'x' } })).toBeUndefined()
    expect(kiroMeta({ _meta: { goose: {} } })).toBeUndefined()
  })
})
