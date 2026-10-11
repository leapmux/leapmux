import { describe, expect, it } from 'vitest'
import { ACP_SUPPLEMENT_IDENTITY } from '../../../src/generated/contracts/acp-protocol'
import { acpClosedToolCall, requireAcpToolSupplement } from './acpToolFrame'

const closing = (status: unknown, toolCallId: unknown = 'call-1') => ({ sessionUpdate: 'tool_call_update', toolCallId, status })

describe('acpClosedToolCall', () => {
  it('accepts a completed or failed update of the exact call by default', () => {
    expect(acpClosedToolCall(closing('completed'), 'call-1')).toBe(true)
    expect(acpClosedToolCall(closing('failed'), 'call-1')).toBe(true)
  })

  it.each(['pending', 'in_progress', undefined, 7])('refuses an update whose status %j does not end the call', (status) => {
    expect(acpClosedToolCall(closing(status), 'call-1')).toBe(false)
  })

  it('keeps only the statuses that a caller states', () => {
    expect(acpClosedToolCall(closing('completed'), 'call-1', ['completed'])).toBe(true)
    expect(acpClosedToolCall(closing('failed'), 'call-1', ['completed'])).toBe(false)
    expect(acpClosedToolCall(closing('completed'), 'call-1', [])).toBe(false)
  })

  it('refuses a foreign call, an absent call ID, and an empty call ID', () => {
    expect(acpClosedToolCall(closing('completed'), 'call-2')).toBe(false)
    expect(acpClosedToolCall({ sessionUpdate: 'tool_call_update', status: 'completed' }, 'call-1')).toBe(false)
    expect(acpClosedToolCall(closing('completed', ''), '')).toBe(false)
  })

  it('refuses a frame that is not a tool-call update', () => {
    expect(acpClosedToolCall({ sessionUpdate: 'tool_call', toolCallId: 'call-1', status: 'completed' }, 'call-1')).toBe(false)
    expect(acpClosedToolCall({ toolCallId: 'call-1', status: 'completed' }, 'call-1')).toBe(false)
  })
})

describe('requireAcpToolSupplement', () => {
  const original = { sessionUpdate: 'tool_call_update', toolCallId: 'call-1', status: 'completed' }

  it('returns the supplement whose identity matches the frame', () => {
    const supplement = { ...original, rawOutput: { output: 'native' } }
    expect(requireAcpToolSupplement(original, supplement, 'Goose terminal')).toBe(supplement)
  })

  it.each([
    { label: 'an absent supplement', supplemental: undefined },
    { label: 'a supplement of another call', supplemental: { ...original, toolCallId: 'call-2' } },
    { label: 'a supplement of another status', supplemental: { ...original, status: 'in_progress' } },
    { label: 'a supplement that omits an identity key', supplemental: { sessionUpdate: 'tool_call_update', toolCallId: 'call-1' } },
  ])('refuses $label with the label of the read', ({ supplemental }) => {
    expect(() => requireAcpToolSupplement(original, supplemental, 'Goose terminal'))
      .toThrow('The native Goose terminal record belongs to another result.')
  })

  it('refuses a numeric identity, which the Worker refuses also', () => {
    const numeric = { ...original, [ACP_SUPPLEMENT_IDENTITY.ToolCallID]: 5 }
    expect(() => requireAcpToolSupplement(numeric, { ...numeric }, 'Reasonix output')).toThrow('belongs to another result')
  })
})
