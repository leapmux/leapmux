import { describe, expect, it } from 'vitest'
import { ampContextUsageTokens } from './scenarios'

describe('ampContextUsageTokens', () => {
  it.each([
    [{}, 1],
    [
      { inputTokens: 100, outputTokens: 40 },
      100,
    ],
    [
      { outputTokens: 40 },
      1,
    ],
    [
      { inputTokens: 0, outputTokens: 0 },
      0,
    ],
    [
      {
        inputTokens: 10,
        cacheReadTokens: 2,
        cacheWriteTokens: 3,
        outputTokens: 4,
      },
      10,
    ],
    [
      { cacheReadTokens: 2, cacheWriteTokens: 3 },
      5,
    ],
  ])('counts native input without output for %j', (usage, expected) => {
    expect(ampContextUsageTokens(usage)).toBe(expected)
  })
})
