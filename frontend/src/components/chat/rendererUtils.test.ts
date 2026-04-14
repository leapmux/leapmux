import { describe, expect, it } from 'vitest'
import { formatCompactNumber } from './rendererUtils'

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
