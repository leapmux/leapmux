import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { ZCODE_EVENT } from '~/generated/contracts/zcode-protocol'
import { normalizeContextUsage } from '~/lib/messageParser'
import { zcodeContextUsageFromMessage } from './sessionMetadata'

/** One stored `session.updated` model response carrying `usage`. */
function usageRow(usage: Record<string, unknown>): ParsedMessageContent {
  const parentObject = {
    type: ZCODE_EVENT.SessionUpdated,
    payload: { content: 'done', stopReason: 'stop', usage },
    sessionId: 's-1',
    seq: 1,
  }
  return { wrapper: null, topLevel: parentObject, parentObject, rawText: '', supplementalContent: undefined, messageMetadata: undefined }
}

describe('zcodeContextUsageFromMessage', () => {
  // ZCode reports a cache-INCLUSIVE input with the cache buckets inside it. The
  // replay subtracts the buckets, so one token of cache traffic counts once: in
  // its bucket, and not also inside the input.
  it('subtracts the cache buckets from the cache-inclusive input', () => {
    expect(zcodeContextUsageFromMessage(usageRow({
      inputTokens: 10,
      outputTokens: 4,
      cacheReadTokens: 2,
      cacheWriteTokens: 3,
    }))).toEqual({
      inputTokens: 5,
      cacheCreationInputTokens: 3,
      cacheReadInputTokens: 2,
      outputTokens: 4,
    })
  })

  it('keeps an input that carries no cache unchanged', () => {
    expect(zcodeContextUsageFromMessage(usageRow({ inputTokens: 10, outputTokens: 4 })))
      .toEqual({
        inputTokens: 10,
        cacheCreationInputTokens: 0,
        cacheReadInputTokens: 0,
        outputTokens: 4,
      })
  })

  it('reports zero ordinary input for a full cache hit', () => {
    expect(zcodeContextUsageFromMessage(usageRow({
      inputTokens: 5,
      outputTokens: 4,
      cacheReadTokens: 2,
      cacheWriteTokens: 3,
    }))).toEqual({
      inputTokens: 0,
      cacheCreationInputTokens: 3,
      cacheReadInputTokens: 2,
      outputTokens: 4,
    })
  })

  it('clamps over-full cache buckets to zero ordinary input', () => {
    expect(zcodeContextUsageFromMessage(usageRow({
      inputTokens: 3,
      outputTokens: 4,
      cacheReadTokens: 2,
      cacheWriteTokens: 3,
    }))).toEqual({
      inputTokens: 0,
      cacheCreationInputTokens: 3,
      cacheReadInputTokens: 2,
      outputTokens: 4,
    })
  })

  it('keeps the native total as the authoritative context size', () => {
    expect(zcodeContextUsageFromMessage(usageRow({
      inputTokens: 10,
      outputTokens: 4,
      cacheReadTokens: 2,
      cacheWriteTokens: 3,
      totalTokens: 14,
    }))).toEqual({
      inputTokens: 5,
      cacheCreationInputTokens: 3,
      cacheReadInputTokens: 2,
      outputTokens: 4,
      contextTokens: 14,
    })
  })

  // A zero total states no total, so the card sums the rows instead.
  it('treats a zero total as no total', () => {
    const info = zcodeContextUsageFromMessage(usageRow({ inputTokens: 10, outputTokens: 4, totalTokens: 0 }))
    expect(info).toEqual({
      inputTokens: 10,
      cacheCreationInputTokens: 0,
      cacheReadInputTokens: 0,
      outputTokens: 4,
    })
    expect(info?.contextTokens).toBeUndefined()
  })

  it('defaults the counts the event does not state to zero', () => {
    expect(zcodeContextUsageFromMessage(usageRow({ inputTokens: 12 })))
      .toEqual({ inputTokens: 12, cacheCreationInputTokens: 0, cacheReadInputTokens: 0, outputTokens: 0 })
    expect(zcodeContextUsageFromMessage(usageRow({ cacheReadTokens: 900, outputTokens: 4 })))
      .toEqual({ inputTokens: 0, cacheCreationInputTokens: 0, cacheReadInputTokens: 900, outputTokens: 4 })
  })

  it('returns null for an all-zero snapshot and for a row that carries no usage', () => {
    expect(zcodeContextUsageFromMessage(usageRow({
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheWriteTokens: 0,
      totalTokens: 0,
    }))).toBeNull()

    const noUsage = { type: ZCODE_EVENT.SessionUpdated, payload: { content: 'done', stopReason: 'stop' }, sessionId: 's-1', seq: 1 }
    expect(zcodeContextUsageFromMessage({ wrapper: null, topLevel: noUsage, parentObject: noUsage, rawText: '', supplementalContent: undefined, messageMetadata: undefined }))
      .toBeNull()
  })

  it('returns null for a stored row that is not a model response', () => {
    // A `session.updated` of another shape stores no reply row, and the usage of
    // another event type belongs to no reply either.
    const drop = { type: ZCODE_EVENT.SessionUpdated, payload: { messageCount: 3 }, sessionId: 's-1', seq: 1 }
    expect(zcodeContextUsageFromMessage({ wrapper: null, topLevel: drop, parentObject: drop, rawText: '', supplementalContent: undefined, messageMetadata: undefined }))
      .toBeNull()
    const turn = { type: ZCODE_EVENT.TurnCompleted, payload: { usage: { inputTokens: 10 } }, sessionId: 's-1', seq: 1 }
    expect(zcodeContextUsageFromMessage({ wrapper: null, topLevel: turn, parentObject: turn, rawText: '', supplementalContent: undefined, messageMetadata: undefined }))
      .toBeNull()
  })

  // The stored replay and the worker's context_usage supplement are two readers
  // of one native reading. They must produce one display.
  it('replays the reading that the worker supplement carries', () => {
    const replayed = zcodeContextUsageFromMessage(usageRow({
      inputTokens: 10,
      outputTokens: 4,
      cacheReadTokens: 2,
      cacheWriteTokens: 3,
      totalTokens: 14,
    }))
    expect(replayed).toEqual(normalizeContextUsage({
      input_tokens: 5,
      cache_read_input_tokens: 2,
      cache_creation_input_tokens: 3,
      output_tokens: 4,
      context_tokens: 14,
    }))
  })
})
