import { describe, expect, it } from 'vitest'
import { formatCompactNumber, formatTokenCount, joinMetaParts } from './rendererUtils'

describe('formatCompactNumber', () => {
  it('numbers below 1000 are returned as-is', () => {
    expect(formatCompactNumber(0)).toBe('0')
    expect(formatCompactNumber(1)).toBe('1')
    expect(formatCompactNumber(999)).toBe('999')
  })

  it('thousands use k suffix with one decimal', () => {
    expect(formatCompactNumber(1000)).toBe('1k')
    expect(formatCompactNumber(1500)).toBe('1.5k')
    expect(formatCompactNumber(67738)).toBe('67.7k')
    expect(formatCompactNumber(99900)).toBe('99.9k')
  })

  it('hundreds of thousands round the decimal', () => {
    expect(formatCompactNumber(100_000)).toBe('100k')
    expect(formatCompactNumber(250_000)).toBe('250k')
    expect(formatCompactNumber(999_999)).toBe('1000k')
  })

  it('millions use m suffix with one decimal', () => {
    expect(formatCompactNumber(1_000_000)).toBe('1m')
    expect(formatCompactNumber(1_500_000)).toBe('1.5m')
    expect(formatCompactNumber(12_345_678)).toBe('12.3m')
  })

  it('hundreds of millions round the decimal', () => {
    expect(formatCompactNumber(100_000_000)).toBe('100m')
    expect(formatCompactNumber(500_000_000)).toBe('500m')
  })

  it('billions use g suffix with one decimal', () => {
    expect(formatCompactNumber(1_000_000_000)).toBe('1g')
    expect(formatCompactNumber(2_500_000_000)).toBe('2.5g')
  })

  it('drops trailing .0 decimals', () => {
    expect(formatCompactNumber(2000)).toBe('2k')
    expect(formatCompactNumber(3_000_000)).toBe('3m')
    expect(formatCompactNumber(4_000_000_000)).toBe('4g')
  })
})

describe('formatTokenCount', () => {
  it('numbers below 1000 are returned as-is', () => {
    expect(formatTokenCount(0)).toBe('0')
    expect(formatTokenCount(500)).toBe('500')
    expect(formatTokenCount(999)).toBe('999')
  })

  it('thousands use a fixed one-decimal k suffix (keeping trailing .0)', () => {
    expect(formatTokenCount(1000)).toBe('1.0k')
    expect(formatTokenCount(8476)).toBe('8.5k')
    expect(formatTokenCount(105_424)).toBe('105.4k')
  })

  it('millions use a fixed one-decimal M suffix', () => {
    expect(formatTokenCount(1_000_000)).toBe('1.0M')
    expect(formatTokenCount(12_345_678)).toBe('12.3M')
  })

  it('promotes a value that would round to "1000.0k" up to "1.0M"', () => {
    // 999_999 / 1000 rounds to "1000.0k" at one decimal; show "1.0M" instead.
    expect(formatTokenCount(999_999)).toBe('1.0M')
    expect(formatTokenCount(999_950)).toBe('1.0M')
  })

  it('keeps the k suffix just below the promotion boundary', () => {
    expect(formatTokenCount(999_949)).toBe('999.9k')
  })

  it('rounds a fractional count to an integer before bucketing', () => {
    // A stray non-integer (e.g. a server-estimated token size) must not leak
    // decimals via the sub-1k String(n) branch, and must round into the right
    // bucket rather than rendering a four-digit "1000".
    expect(formatTokenCount(999.5)).toBe('1.0k')
    expect(formatTokenCount(999.4)).toBe('999')
    expect(formatTokenCount(512.7)).toBe('513')
    expect(formatTokenCount(8476.6)).toBe('8.5k')
  })
})

describe('joinMetaParts', () => {
  it('joins truthy strings with ` · `', () => {
    expect(joinMetaParts(['a', 'b', 'c'])).toBe('a · b · c')
  })

  it('drops empty strings, false, null, and undefined', () => {
    expect(joinMetaParts(['a', '', false, null, undefined, 'b'])).toBe('a · b')
  })

  it('returns an empty string when nothing is truthy', () => {
    expect(joinMetaParts([])).toBe('')
    expect(joinMetaParts([false, null, undefined, ''])).toBe('')
  })
})
