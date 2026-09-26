import { describe, expect, it } from 'vitest'
import { formatTokenCount } from '../../../src/components/chat/rendererUtils'
import { usageMarkers } from './contextUsage'

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
