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

  // The Worker states the native context window in the supplement of each assistant block,
  // because the native usage carries no window.
  describe('context window', () => {
    const withWindow = (usage: Record<string, unknown>, contextWindow: unknown) => ({ ...parsed(usage), supplementalContent: { blockIndex: 0, contextWindow } })

    it('carries the window that the supplement states, beside an explicit zero reading', () => {
      expect(deepseekHarnessContextUsage(withWindow({ inputTokens: 0, outputTokens: 0, totalTokens: 0 }, 1_000_000)))
        .toEqual({ inputTokens: 0, outputTokens: 0, cacheReadInputTokens: 0, cacheCreationInputTokens: 0, contextTokens: 0, contextWindow: 1_000_000 })
    })

    it('states no window when the supplement states none', () => {
      expect(deepseekHarnessContextUsage({ ...parsed({ inputTokens: 1 }), supplementalContent: { blockIndex: 0 } })).not.toHaveProperty('contextWindow')
    })

    it.each([0, -1, 0.5, '1000000', null, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])('ignores an invalid window: %j', (value) => {
      expect(deepseekHarnessContextUsage(withWindow({ inputTokens: 1 }, value))).toEqual({ inputTokens: 1, cacheReadInputTokens: 0, cacheCreationInputTokens: 0 })
    })

    it.each([null, 'text', 7, ['contextWindow']])('ignores a supplement that is not an object: %j', (supplement) => {
      expect(deepseekHarnessContextUsage({ ...parsed({ inputTokens: 1 }), supplementalContent: supplement })).toEqual({ inputTokens: 1, cacheReadInputTokens: 0, cacheCreationInputTokens: 0 })
    })
  })
})
