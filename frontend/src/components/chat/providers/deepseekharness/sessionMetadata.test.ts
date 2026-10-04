import { describe, expect, it } from 'vitest'
import { deepseekHarnessContextUsage } from './sessionMetadata'

const parsed = (usage: Record<string, unknown>) => ({ rawText: '', topLevel: null, wrapper: null, parentObject: { type: 'assistant/message', data: { usage } } })

describe('deepseekHarnessContextUsage', () => {
  it('uses native total tokens without counting cache tokens twice', () => {
    expect(deepseekHarnessContextUsage(parsed({ inputTokens: 10, outputTokens: 20, cacheReadTokens: 30, cacheWriteTokens: 40, totalTokens: 100 }))).toEqual({ inputTokens: 10, outputTokens: 20, cacheReadInputTokens: 30, cacheCreationInputTokens: 40, contextTokens: 100 })
  })

  it('preserves zero counts and leaves absent output and totals absent', () => {
    expect(deepseekHarnessContextUsage(parsed({ inputTokens: 0, outputTokens: 0, totalTokens: 0 }))).toEqual({ inputTokens: 0, outputTokens: 0, cacheReadInputTokens: 0, cacheCreationInputTokens: 0, contextTokens: 0 })
    expect(deepseekHarnessContextUsage(parsed({ inputTokens: 0 }))).toEqual({ inputTokens: 0, cacheReadInputTokens: 0, cacheCreationInputTokens: 0 })
  })

  it.each([{ value: -1 }, { value: 0.1 }, { value: '1' }, { value: Number.NaN }, { value: Number.POSITIVE_INFINITY }, { value: Number.MAX_SAFE_INTEGER + 1 }])('rejects an invalid input count: $value', ({ value }) => {
    expect(deepseekHarnessContextUsage(parsed({ inputTokens: value }))).toBeNull()
  })
})
