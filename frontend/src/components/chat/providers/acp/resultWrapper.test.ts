import { describe, expect, it } from 'vitest'
import { unwrapACPResult } from './resultWrapper'

// The bytes below are the ones the worker persists. `TestHandlePromptResponse_WrappedFormat`
// and `TestACPPromptPreservesTheCompleteNativeResultWrapper` pin the same shape in Go.
describe('unwrapACPResult', () => {
  it('returns the content of a native result envelope', () => {
    const content = { _meta: {}, stopReason: 'end_turn', usage: { totalTokens: 100 } }
    expect(unwrapACPResult({
      id: 'msg-1',
      role: 'result',
      seq: 4,
      created_at: '2026-03-26T10:46:48.015Z',
      content,
    })).toEqual(content)
  })

  it('returns an unwrapped answer unchanged', () => {
    const flat = { stopReason: 'end_turn', usage: { totalTokens: 100 } }
    expect(unwrapACPResult(flat)).toBe(flat)
  })

  it('returns a non-result role unchanged', () => {
    const assistant = { role: 'assistant', content: { text: 'hello' } }
    expect(unwrapACPResult(assistant)).toBe(assistant)
  })

  it('returns the envelope when a result role carries no object content', () => {
    // A string `content` is the LeapMux user-row shape, which the classifier
    // reads off the envelope itself. Falling back keeps that row reachable.
    const envelope = { role: 'result', content: 'plain text' }
    expect(unwrapACPResult(envelope)).toBe(envelope)
  })

  it('returns undefined for a non-object', () => {
    expect(unwrapACPResult(undefined)).toBeUndefined()
    expect(unwrapACPResult(null)).toBeUndefined()
    expect(unwrapACPResult('result')).toBeUndefined()
    expect(unwrapACPResult([{ stopReason: 'end_turn' }])).toBeUndefined()
  })
})
