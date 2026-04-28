import { describe, expect, it } from 'vitest'
import { shallowEqual, shallowEqualArrays, shallowEqualExcept } from './shallowEqual'

describe('shallowequal', () => {
  it('returns true for same reference', () => {
    const obj = { a: 1 }
    expect(shallowEqual(obj, obj)).toBe(true)
  })

  it('returns true for value-equal plain objects', () => {
    expect(shallowEqual({ a: 1, b: 2 }, { a: 1, b: 2 })).toBe(true)
  })

  it('returns false when key counts differ', () => {
    expect(shallowEqual({ a: 1 }, { a: 1, b: 2 })).toBe(false)
  })

  it('returns false on differing values', () => {
    expect(shallowEqual({ a: 1 }, { a: 2 })).toBe(false)
  })

  it('rejects arrays unless same reference', () => {
    const arr = [1, 2]
    expect(shallowEqual(arr, arr)).toBe(true)
    expect(shallowEqual([1, 2], [1, 2])).toBe(false)
  })

  it('rejects null/undefined inputs', () => {
    expect(shallowEqual(null, null)).toBe(true)
    expect(shallowEqual(null, {})).toBe(false)
    expect(shallowEqual(undefined, {})).toBe(false)
  })
})

describe('shallowequalarrays', () => {
  it('returns true for same reference', () => {
    const arr = [1, 2, 3]
    expect(shallowEqualArrays(arr, arr)).toBe(true)
  })

  it('returns true for two empty arrays', () => {
    expect(shallowEqualArrays([], [])).toBe(true)
  })

  it('returns true for element-wise equal arrays', () => {
    expect(shallowEqualArrays([1, 2, 3], [1, 2, 3])).toBe(true)
    expect(shallowEqualArrays(['a', 'b'], ['a', 'b'])).toBe(true)
    expect(shallowEqualArrays([true, false, 0], [true, false, 0])).toBe(true)
  })

  it('returns false when lengths differ', () => {
    expect(shallowEqualArrays([1], [1, 2])).toBe(false)
    expect(shallowEqualArrays([], [undefined])).toBe(false)
  })

  it('returns false when any element differs', () => {
    expect(shallowEqualArrays([1, 2, 3], [1, 2, 4])).toBe(false)
    expect(shallowEqualArrays(['a', 'b'], ['a', 'c'])).toBe(false)
  })

  it('treats NaN as equal to itself (Object.is semantics)', () => {
    expect(shallowEqualArrays([Number.NaN], [Number.NaN])).toBe(true)
  })

  it('compares object elements by reference (does not recurse)', () => {
    const obj = { a: 1 }
    expect(shallowEqualArrays([obj], [obj])).toBe(true)
    expect(shallowEqualArrays([{ a: 1 }], [{ a: 1 }])).toBe(false)
  })
})

describe('shallowequalexcept', () => {
  it('ignores skip keys when comparing', () => {
    expect(shallowEqualExcept(
      { a: 1, ts: 100 },
      { a: 1, ts: 200 },
      ['ts'],
    )).toBe(true)
  })

  it('returns false when a non-skipped key differs', () => {
    expect(shallowEqualExcept(
      { a: 1, b: 2, ts: 100 },
      { a: 1, b: 3, ts: 200 },
      ['ts'],
    )).toBe(false)
  })
})
