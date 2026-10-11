import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { ohMyPiContextUsageFromMessage } from './sessionMetadata'

function parsed(usage: unknown): ParsedMessageContent {
  const parentObject = { type: 'message_end', message: { role: 'assistant', content: [], usage } }
  return { wrapper: null, topLevel: parentObject, parentObject, rawText: '', supplementalContent: undefined, messageMetadata: undefined }
}

describe('ohMyPiContextUsageFromMessage', () => {
  it('reads omp\'s own counts', () => {
    // omp 18.2.11's own usage (probe s1).
    expect(ohMyPiContextUsageFromMessage(parsed({ input: 1200, output: 30, cacheRead: 5, cacheWrite: 7, totalTokens: 1242, cost: { total: 0 } }))).toEqual({
      inputTokens: 1200,
      cacheCreationInputTokens: 7,
      cacheReadInputTokens: 5,
      outputTokens: 30,
      contextTokens: 1242,
    })
  })

  it('reads a request whose whole prompt came from the cache', () => {
    // The fresh input is zero, but the cache read states a real context.
    expect(ohMyPiContextUsageFromMessage(parsed({ input: 0, output: 4, cacheRead: 900, cacheWrite: 0, totalTokens: 904 }))).toEqual({
      inputTokens: 0,
      cacheCreationInputTokens: 0,
      cacheReadInputTokens: 900,
      outputTokens: 4,
      contextTokens: 904,
    })
  })

  it('leaves out each count omp does not state, and a zero total', () => {
    // Absent and zero stay different: the cache counts default to zero, while an
    // absent output and a total of zero state no count at all.
    expect(ohMyPiContextUsageFromMessage(parsed({ input: 12 }))).toEqual({ inputTokens: 12, cacheCreationInputTokens: 0, cacheReadInputTokens: 0 })
    expect(ohMyPiContextUsageFromMessage(parsed({ input: 12, output: 0, totalTokens: 0 }))).toEqual({ inputTokens: 12, cacheCreationInputTokens: 0, cacheReadInputTokens: 0, outputTokens: 0 })
  })

  it('reads a row with no message as no counts', () => {
    const parentObject = { type: 'agent_end' }
    expect(ohMyPiContextUsageFromMessage({ wrapper: null, topLevel: parentObject, parentObject, rawText: '', supplementalContent: undefined, messageMetadata: undefined })).toBeNull()
  })

  it('states nothing for a request with no counts', () => {
    expect(ohMyPiContextUsageFromMessage(parsed({ input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0 }))).toBeNull()
    expect(ohMyPiContextUsageFromMessage(parsed(undefined))).toBeNull()
    expect(ohMyPiContextUsageFromMessage(parsed({ input: 'many' }))).toBeNull()
  })
})
