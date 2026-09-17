import { describe, expect, it } from 'vitest'
import { dividerMetaFromMessage } from './divider'

/**
 * The turn totals the worker injects for EVERY provider. They were parsed and dropped
 * before this reader existed: `extractResultMetadata` took the cost for the session
 * store and the tool count for nothing at all, so neither reached the row a reader
 * looks at.
 */
function parsed(inner: Record<string, unknown>) {
  return { wrapper: null, topLevel: inner, parentObject: inner, rawText: '', supplementalContent: undefined, messageMetadata: undefined }
}

describe('dividerMetaFromMessage', () => {
  it('reads the three totals the worker injects', () => {
    expect(dividerMetaFromMessage(parsed({
      type: 'result',
      duration_ms: 12_000,
      total_cost_usd: 0.1234,
      num_tool_uses: 5,
    }))).toEqual({ durationMs: 12_000, costUsd: 0.1234, numToolUses: 5 })
  })

  // A zero is a MEASUREMENT, not an absence: a turn that ran no tool is a fact the row
  // may state. Dropping it here would make the reader unable to tell it from a frame
  // that carried no count at all.
  it('keeps a zero count', () => {
    expect(dividerMetaFromMessage(parsed({ type: 'result', num_tool_uses: 0 })))
      .toEqual({ durationMs: undefined, costUsd: undefined, numToolUses: 0 })
  })

  it('answers undefined when the frame carries no totals', () => {
    expect(dividerMetaFromMessage(parsed({ type: 'result', subtype: 'turn_end' }))).toBeUndefined()
  })

  // A subagent's totals are already inside the parent's, so its rule states none --
  // the same guard `extractResultMetadata` applies for the same reason.
  it('states no totals for a subagent turn', () => {
    expect(dividerMetaFromMessage(parsed({
      type: 'result',
      parent_tool_use_id: 'toolu_abc',
      total_cost_usd: 0.05,
      num_tool_uses: 3,
    }))).toBeUndefined()
  })

  it('answers undefined for a frame with no inner message', () => {
    expect(dividerMetaFromMessage({
      wrapper: null,
      topLevel: null,
      parentObject: undefined,
      rawText: '',
      supplementalContent: undefined,
      messageMetadata: undefined,
    })).toBeUndefined()
  })
})
