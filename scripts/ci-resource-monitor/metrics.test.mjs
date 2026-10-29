import { describe, expect, it } from 'bun:test'
import { cpuPercent, formatGigabytes, formatMegabytes } from './metrics.mjs'

describe('cpuPercent', () => {
  it('reports the busy share between two readings of /proc/stat', () => {
    const first = { idle: 800, total: 1000 }
    const second = { idle: 1600, total: 2000 }
    expect(cpuPercent(first, second)).toBe('20%')
  })

  it('reports no figure for the first reading or an unchanged total', () => {
    const reading = { idle: 5, total: 10 }
    expect(cpuPercent(null, reading)).toBe('n/a')
    expect(cpuPercent(reading, reading)).toBe('n/a')
  })
})

describe('metric formatting', () => {
  it.each([
    -1,
    undefined,
    null,
    Number.NaN,
    Number.POSITIVE_INFINITY,
    Number.NEGATIVE_INFINITY,
  ])('reports an unavailable size as unknown: %j', (value) => {
    expect(formatMegabytes(value)).toBe('unknown')
    expect(formatGigabytes(value)).toBe('unknown')
  })

  it('keeps zero, rounded sizes and large sizes', () => {
    expect(formatGigabytes(0)).toBe('0.0G')
    expect(formatGigabytes(2 ** 30)).toBe('1.0G')
    expect(formatMegabytes(0)).toBe('0M')
    expect(formatMegabytes(1.5 * 2 ** 20)).toBe('2M')
    expect(formatMegabytes(2 ** 50)).toBe('1073741824M')
  })
})
