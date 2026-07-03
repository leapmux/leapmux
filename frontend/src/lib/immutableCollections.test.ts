import { describe, expect, it } from 'vitest'
import { mapWithout, setWith, setWithout } from './immutableCollections'

describe('immutablecollections', () => {
  describe('setwith', () => {
    it('adds a missing value into a NEW set, leaving the original untouched', () => {
      const original = new Set([1, 2])
      const next = setWith(original, 3)
      expect(next).not.toBe(original)
      expect([...next]).toEqual([1, 2, 3])
      expect([...original]).toEqual([1, 2])
    })

    it('returns the SAME reference when the value is already present (no churn)', () => {
      const original = new Set(['a', 'b'])
      expect(setWith(original, 'a')).toBe(original)
    })
  })

  describe('setwithout', () => {
    it('removes a present value into a NEW set, leaving the original untouched', () => {
      const original = new Set([1, 2, 3])
      const next = setWithout(original, 2)
      expect(next).not.toBe(original)
      expect([...next]).toEqual([1, 3])
      expect([...original]).toEqual([1, 2, 3])
    })

    it('returns the SAME reference when the value is absent (no churn)', () => {
      const original = new Set(['a', 'b'])
      expect(setWithout(original, 'z')).toBe(original)
    })
  })

  describe('mapwithout', () => {
    it('removes a present key into a NEW map, leaving the original untouched', () => {
      const original = new Map([['a', 1], ['b', 2]])
      const next = mapWithout(original, 'a')
      expect(next).not.toBe(original)
      expect([...next]).toEqual([['b', 2]])
      expect([...original]).toEqual([['a', 1], ['b', 2]])
    })

    it('returns the SAME reference when the key is absent (no churn)', () => {
      const original = new Map([['a', 1]])
      expect(mapWithout(original, 'z')).toBe(original)
    })
  })
})
