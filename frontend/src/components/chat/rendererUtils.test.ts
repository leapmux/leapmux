import { describe, expect, it } from 'vitest'
import { formatCompactNumber, formatDuration, formatSecondsParts, formatTokenCount, joinMetaParts } from './rendererUtils'

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

describe('formatTokenCount with a custom decimal precision', () => {
  it('defaults to one decimal when precision is omitted', () => {
    expect(formatTokenCount(1234)).toBe('1.2k')
    expect(formatTokenCount(1_234_567)).toBe('1.2M')
  })

  it('renders k/M to the requested number of decimals', () => {
    // The thinking-token counter passes 2 so its fast increments read finely.
    expect(formatTokenCount(4950, 2)).toBe('4.95k')
    expect(formatTokenCount(1234, 2)).toBe('1.23k')
    expect(formatTokenCount(1_234_567, 2)).toBe('1.23M')
    expect(formatTokenCount(1_000_000, 2)).toBe('1.00M')
  })

  it('leaves sub-1k values undecorated regardless of precision', () => {
    expect(formatTokenCount(999, 2)).toBe('999')
    expect(formatTokenCount(0, 2)).toBe('0')
  })

  it('tightens the M-promotion boundary as precision grows', () => {
    // At two decimals "1000.00k" appears only at 999_995+, so the cutoff moves
    // up from the one-decimal 999_950: 999_994 stays k, 999_995 promotes to M.
    expect(formatTokenCount(999_994, 2)).toBe('999.99k')
    expect(formatTokenCount(999_995, 2)).toBe('1.00M')
  })

  it('falls back to "0" for non-finite input rather than emitting a broken string', () => {
    // No caller in the thinking-token pipeline passes these, but formatTokenCount
    // is shared: NaN/Infinity must not render as "NaN" / "InfinityM".
    expect(formatTokenCount(Number.NaN)).toBe('0')
    expect(formatTokenCount(Number.POSITIVE_INFINITY, 2)).toBe('0')
    expect(formatTokenCount(Number.NEGATIVE_INFINITY)).toBe('0')
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

describe('formatDuration', () => {
  it('sub-second values are whole milliseconds', () => {
    expect(formatDuration(0)).toBe('0ms')
    expect(formatDuration(865)).toBe('865ms')
    expect(formatDuration(999)).toBe('999ms')
  })

  it('under ten seconds carries one decimal', () => {
    expect(formatDuration(1000)).toBe('1.0s')
    expect(formatDuration(3200)).toBe('3.2s')
    // toFixed rounds half away from zero, so 3.25s reads "3.3s".
    expect(formatDuration(3250)).toBe('3.3s')
    // 9.999s stays on the decimal branch and rounds up, so the boundary below
    // ten seconds reads "10.0s" while a real 10000ms reads "10s".
    expect(formatDuration(9999)).toBe('10.0s')
  })

  it('ten seconds and above compose whole units', () => {
    expect(formatDuration(10_000)).toBe('10s')
    expect(formatDuration(65_000)).toBe('1m 5s')
    expect(formatDuration(3_600_000)).toBe('1h')
    expect(formatDuration(90_061_000)).toBe('1d 1h 1m 1s')
  })

  it('rounds a fractional millisecond count', () => {
    expect(formatDuration(0.4)).toBe('0ms')
    expect(formatDuration(999.6)).toBe('1000ms')
  })

  // The extraction must leave every answer of ten seconds and above unchanged;
  // only the entry point differs.
  it('agrees with formatSecondsParts at and above ten seconds', () => {
    for (const seconds of [10, 65, 3600, 90_061])
      expect(formatDuration(seconds * 1000)).toBe(formatSecondsParts(seconds))
  })
})

describe('formatSecondsParts', () => {
  /**
   * The whole-seconds entry point, for a caller whose source counts in seconds
   * rather than measuring in milliseconds. It never takes formatDuration's
   * sub-ten-second decimal branch: the running-tool badge shows the agent's own
   * integer count, and "5.0s" beside "30s" and "1m 30s" reads as another unit.
   */
  it('renders a short duration in whole seconds, with no decimal', () => {
    expect(formatSecondsParts(1)).toBe('1s')
    expect(formatSecondsParts(5)).toBe('5s')
    expect(formatSecondsParts(9)).toBe('9s')
    // The value formatDuration would render as "5.0s".
    expect(formatDuration(5000)).toBe('5.0s')
  })

  it('composes whole units, exactly as formatDuration does above ten seconds', () => {
    expect(formatSecondsParts(10)).toBe('10s')
    expect(formatSecondsParts(30)).toBe('30s')
    expect(formatSecondsParts(60)).toBe('1m')
    expect(formatSecondsParts(90)).toBe('1m 30s')
    expect(formatSecondsParts(3600)).toBe('1h')
    expect(formatSecondsParts(3690)).toBe('1h 1m 30s')
    expect(formatSecondsParts(90_061)).toBe('1d 1h 1m 1s')
  })

  it('reads zero as "0s" rather than an empty string', () => {
    // The parts list is empty for a zero, so the seconds part is unconditional.
    expect(formatSecondsParts(0)).toBe('0s')
  })

  it('rounds a fractional count and floors a negative one at zero', () => {
    expect(formatSecondsParts(29.4)).toBe('29s')
    expect(formatSecondsParts(29.6)).toBe('30s')
    // A negative duration is not a duration. The worker already refuses one, so
    // this is the second line of defence rather than the first.
    expect(formatSecondsParts(-5)).toBe('0s')
  })
})
