import { describe, expect, it } from 'vitest'
import { computePercentage, contextSize } from './ContextUsageGrid'

describe('context usage grid token math', () => {
  it('prefers provider-reported contextTokens when present', () => {
    expect(contextSize({
      inputTokens: 100,
      cacheCreationInputTokens: 10,
      cacheReadInputTokens: 20,
      outputTokens: 5,
      contextTokens: 1000,
    })).toBe(1000)
  })

  it('falls back to input/cache/output token components', () => {
    expect(contextSize({
      inputTokens: 100,
      cacheCreationInputTokens: 10,
      cacheReadInputTokens: 20,
      outputTokens: 5,
    })).toBe(135)
  })

  it('computes percentage from provider-reported contextTokens', () => {
    expect(computePercentage({
      inputTokens: 0,
      cacheCreationInputTokens: 0,
      cacheReadInputTokens: 0,
      outputTokens: 0,
      contextTokens: 50,
      contextWindow: 200,
    })).toBe(25)
  })
})
