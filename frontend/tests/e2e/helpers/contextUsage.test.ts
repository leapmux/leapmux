import { describe, expect, it } from 'vitest'
import { formatTokenCount } from '../../../src/components/chat/rendererUtils'
import { parseContextRow, usageMarkers } from './contextUsage'

describe('usageMarkers', () => {
  // The marker block every context-usage spec scripts. A default of 1/1 would
  // print one repeated figure and prove nothing about which count moved.
  it('marks the combined figure the card prints', () => {
    expect(usageMarkers({ inputTokens: 12000, outputTokens: 40 })).toEqual([formatTokenCount(12040)])
  })

  it('keeps the marker a substring of the card abbreviation of the total', () => {
    for (const [input, output] of [[0, 0], [1, 1], [40, 0], [999, 1], [1000, 0], [12000, 40], [250_000, 0]] as const) {
      const [marker] = usageMarkers({ inputTokens: input, outputTokens: output })
      expect(formatTokenCount(input + output), `marker for ${input}+${output}`).toContain(marker!)
    }
  })

  it('returns no marker when the step scripted no count', () => {
    expect(usageMarkers({})).toEqual([])
    expect(usageMarkers({ contextWindow: 200000 })).toEqual([])
  })

  it('still marks when only one count is scripted', () => {
    expect(usageMarkers({ inputTokens: 12000 })).toEqual([formatTokenCount(12000)])
    expect(usageMarkers({ outputTokens: 40 })).toEqual([formatTokenCount(40)])
  })
})

describe('parseContextRow', () => {
  it('reads the counts that formatTokenCount wrote for the total and the window', () => {
    expect(parseContextRow('AgentCodewhaleSession IDthr_63b7d4cbContext12.2k / 1.0M (1%)')).toEqual({ tokens: 12_200, window: 1_000_000 })
  })

  it('reads a count below one thousand without a unit', () => {
    expect(parseContextRow('Context999 / 200.0k (0%)')).toEqual({ tokens: 999, window: 200_000 })
    expect(parseContextRow('Context0 / 1.0M')).toEqual({ tokens: 0, window: 1_000_000 })
  })

  it('reads the row when the text states a headroom after the percentage', () => {
    expect(parseContextRow('Context45.0k / 200.0k (23% with 16% headroom)')).toEqual({ tokens: 45_000, window: 200_000 })
  })

  it('reads the row of a card whose other rows state digits', () => {
    expect(parseContextRow('Session IDthr_0123456789Working dir/tmp/e-1/2/wdContext3.5k / 128.0k (3%)')).toEqual({ tokens: 3_500, window: 128_000 })
  })

  it('round-trips the output of formatTokenCount to within its rounding', () => {
    for (const total of [0, 1, 999, 1_000, 12_040, 999_949, 1_000_000, 12_500_000]) {
      const reading = parseContextRow(`Context${formatTokenCount(total)} / ${formatTokenCount(1_000_000)}`)
      const unit = total >= 999_950 ? 50_000 : total >= 1_000 ? 50 : 0
      expect(Math.abs((reading?.tokens ?? Number.NaN) - total), `total ${total}`).toBeLessThanOrEqual(unit)
    }
  })

  it('returns undefined for a card with no Context row', () => {
    expect(parseContextRow('')).toBeUndefined()
    expect(parseContextRow('AgentCodewhaleSession IDthr_63b7d4cb')).toBeUndefined()
  })

  it('returns undefined for the percentage-only form of the row', () => {
    expect(parseContextRow('Context23% of the context window')).toBeUndefined()
  })
})
