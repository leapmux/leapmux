import { describe, expect, it } from 'vitest'
import { after, first, mid } from '~/lib/lexorank'

describe('lexorank', () => {
  describe('first', () => {
    it('should return a non-empty string', () => {
      expect(first()).toBeTruthy()
      expect(first()).toBe('n')
    })
  })

  describe('after', () => {
    it('should return a rank after the given string', () => {
      const a = first()
      const b = after(a)
      expect(b > a).toBe(true)
    })

    it('should produce strictly increasing sequence', () => {
      let prev = first()
      for (let i = 0; i < 10; i++) {
        const next = after(prev)
        expect(next > prev).toBe(true)
        prev = next
      }
    })
  })

  describe('mid', () => {
    it('should return first() when both args are empty', () => {
      expect(mid('', '')).toBe(first())
    })

    it('should return rank before b when a is empty', () => {
      const b = 'n'
      const result = mid('', b)
      expect(result < b).toBe(true)
      expect(result.length).toBeGreaterThan(0)
    })

    it('should return rank after a when b is empty', () => {
      const a = 'n'
      const result = mid(a, '')
      expect(result > a).toBe(true)
    })

    it('should return rank between a and b', () => {
      const a = 'b'
      const b = 'z'
      const result = mid(a, b)
      expect(result > a).toBe(true)
      expect(result < b).toBe(true)
    })

    it('should handle adjacent characters', () => {
      const a = 'a'
      const b = 'b'
      const result = mid(a, b)
      expect(result > a).toBe(true)
      expect(result < b).toBe(true)
    })

    it('should produce valid mid for sequential insertions', () => {
      const ranks: string[] = [first()]
      for (let i = 0; i < 20; i++) {
        const next = mid(ranks[ranks.length - 1], '')
        expect(next > ranks[ranks.length - 1]).toBe(true)
        ranks.push(next)
      }

      // All ranks should be strictly ordered.
      for (let i = 1; i < ranks.length; i++) {
        expect(ranks[i] > ranks[i - 1]).toBe(true)
      }
    })

    it('should produce valid mid for insertions between bounds', () => {
      const lo = 'a'
      let hi = 'z'
      for (let i = 0; i < 20; i++) {
        const m = mid(lo, hi)
        expect(m > lo).toBe(true)
        expect(m < hi).toBe(true)
        hi = m
      }
    })

    it('should handle equal strings', () => {
      const result = mid('nn', 'nn')
      expect(result > 'nn').toBe(true)
    })
  })
})
