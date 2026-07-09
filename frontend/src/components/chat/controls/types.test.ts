import { describe, expect, it } from 'vitest'
import { toRpcId } from './types'

describe('toRpcId', () => {
  it('converts numeric string to number', () => {
    expect(toRpcId('42')).toBe(42)
  })

  it('preserves non-numeric string', () => {
    expect(toRpcId('abc')).toBe('abc')
  })

  it('converts zero', () => {
    expect(toRpcId('0')).toBe(0)
  })

  it('converts a negative integer string to a number', () => {
    expect(toRpcId('-5')).toBe(-5)
  })

  it('preserves a UUID-style id (non-numeric), the Claude/ACP request-id case', () => {
    expect(toRpcId('abc-123')).toBe('abc-123')
  })
})
