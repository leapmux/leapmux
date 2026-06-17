import { describe, expect, it, vi } from 'vitest'
import { getOrCreate } from './getOrCreate'

describe('getorcreate', () => {
  it('creates, inserts, and returns a value for an absent key', () => {
    const map = new Map<string, number[]>()
    const factory = vi.fn(() => [] as number[])

    const a = getOrCreate(map, 'k', factory)
    a.push(1)

    expect(factory).toHaveBeenCalledTimes(1)
    expect(map.get('k')).toBe(a)
    expect(map.get('k')).toEqual([1])
  })

  it('returns the existing value without re-creating on a present key', () => {
    const map = new Map<string, number[]>()
    const factory = vi.fn(() => [] as number[])

    const first = getOrCreate(map, 'k', factory)
    first.push(1)
    const second = getOrCreate(map, 'k', factory)

    expect(second).toBe(first) // same reference, not a fresh container
    expect(factory).toHaveBeenCalledTimes(1) // factory ran only the first time
    expect(second).toEqual([1])
  })

  it('works with a WeakMap (object keys)', () => {
    const cache = new WeakMap<object, string>()
    const key = {}
    const factory = vi.fn(() => 'parsed')

    expect(getOrCreate(cache, key, factory)).toBe('parsed')
    expect(getOrCreate(cache, key, factory)).toBe('parsed')
    expect(factory).toHaveBeenCalledTimes(1)
  })

  it('treats a stored falsy value as present (does not re-create on 0 / empty string)', () => {
    const map = new Map<string, number>()
    map.set('k', 0)
    const factory = vi.fn(() => 99)

    expect(getOrCreate(map, 'k', factory)).toBe(0) // 0 is present, not absent
    expect(factory).not.toHaveBeenCalled()
  })
})
