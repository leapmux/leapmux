import type { McpServerReceipt } from './mcpServerReceipt'
import { describe, expect, it } from 'vitest'
import { mcpReceiptRequestId, nativeMcpRefusal, parseMcpServerReceipt } from './mcpServerReceipt'

function emptyReceipt(): McpServerReceipt {
  return { initializeCapabilities: null, toolCatalogs: [], elicitationRequests: [], elicitationReplies: [], toolResults: [] }
}

function refusalReceipt(): McpServerReceipt {
  return {
    initializeCapabilities: {},
    toolCatalogs: [{ id: 0, tools: [{ name: 'ask', inputSchema: { type: 'object', properties: {} } }] }],
    elicitationRequests: [{ id: 'probe-form', toolRequestId: 2, params: { mode: 'form' } }],
    elicitationReplies: [{ id: 'probe-form', kind: 'error', error: { code: -32601, message: 'Method not found', data: { native: true } } }],
    toolResults: [{ id: 2, tool: 'ask', text: 'FORM_ROUND_TRIP_REFUSED: -32601 Method not found', isError: true }],
  }
}

describe('parseMcpServerReceipt', () => {
  it('accepts an empty pending receipt without inventing an initialize or request', () => {
    expect(parseMcpServerReceipt(emptyReceipt())).toEqual(emptyReceipt())
  })

  it('preserves native catalogs, matching IDs, error data, zero, and false', () => {
    const value = refusalReceipt()
    value.elicitationRequests.push({ id: '', toolRequestId: 0, params: { count: 0, enabled: false } })
    value.elicitationReplies.push({ id: '', kind: 'result', result: { action: 'accept', content: { count: 0, enabled: false } } })
    expect(parseMcpServerReceipt(value)).toEqual(value)
  })

  it.each([
    { label: 'a missing root', value: null },
    { label: 'a missing capabilities field', value: { ...emptyReceipt(), initializeCapabilities: undefined } },
    { label: 'array capabilities', value: { ...emptyReceipt(), initializeCapabilities: [] } },
    { label: 'a missing catalog array', value: { ...emptyReceipt(), toolCatalogs: undefined } },
    { label: 'a null tool schema', value: { ...emptyReceipt(), toolCatalogs: [{ id: 1, tools: [{ name: 'ask', inputSchema: null }] }] } },
    { label: 'an empty tool name', value: { ...emptyReceipt(), toolCatalogs: [{ id: 1, tools: [{ name: '', inputSchema: {} }] }] } },
    { label: 'a missing originating tool ID', value: { ...emptyReceipt(), elicitationRequests: [{ id: 1, params: {} }] } },
    { label: 'an unsafe request ID', value: { ...emptyReceipt(), elicitationRequests: [{ id: Number.MAX_SAFE_INTEGER + 1, toolRequestId: 2, params: {} }] } },
    { label: 'a repeated request ID', value: { ...refusalReceipt(), elicitationRequests: [refusalReceipt().elicitationRequests[0], refusalReceipt().elicitationRequests[0]] } },
    { label: 'an unknown reply kind', value: { ...emptyReceipt(), elicitationReplies: [{ id: 1, kind: 'timeout' }] } },
    { label: 'a string error code', value: { ...emptyReceipt(), elicitationReplies: [{ id: 1, kind: 'error', error: { code: '-32601', message: 'Method not found' } }] } },
    { label: 'an absent error message', value: { ...emptyReceipt(), elicitationReplies: [{ id: 1, kind: 'error', error: { code: -32601 } }] } },
    { label: 'an absent result object', value: { ...emptyReceipt(), elicitationReplies: [{ id: 1, kind: 'result', result: null }] } },
    { label: 'a non-boolean error flag', value: { ...emptyReceipt(), toolResults: [{ id: 1, tool: 'ask', text: '', isError: 0 }] } },
  ])('rejects $label', ({ value }) => {
    expect(() => parseMcpServerReceipt(value)).toThrow('MCP receipt')
  })
})

describe('nativeMcpRefusal', () => {
  it('pairs the actual request, native unsupported error, and exact originating tool result', () => {
    const value = refusalReceipt()
    expect(nativeMcpRefusal(value)).toEqual({ request: value.elicitationRequests[0], reply: value.elicitationReplies[0], toolResult: value.toolResults[0], reason: '-32601 Method not found' })
  })

  it('uses the matching result when another tool finishes last', () => {
    const value = refusalReceipt()
    value.toolResults.push({ id: 99, tool: 'ask', text: 'FORM_ROUND_TRIP_OK', isError: false })
    expect(nativeMcpRefusal(value).toolResult.id).toBe(2)
  })

  it.each([
    { label: 'no initialize', change: (value: McpServerReceipt) => { value.initializeCapabilities = null } },
    { label: 'no actual input request', change: (value: McpServerReceipt) => { value.elicitationRequests = [] } },
    { label: 'an unmatched reply ID', change: (value: McpServerReceipt) => { value.elicitationReplies[0]!.id = 'different' } },
    { label: 'an accepted form', change: (value: McpServerReceipt) => { value.elicitationReplies = [{ id: 'probe-form', kind: 'result', result: { action: 'accept' } }] } },
    { label: 'a user decline', change: (value: McpServerReceipt) => { value.elicitationReplies = [{ id: 'probe-form', kind: 'result', result: { action: 'decline' } }] } },
    { label: 'a timeout instead of native refusal', change: (value: McpServerReceipt) => { value.elicitationReplies = [{ id: 'probe-form', kind: 'error', error: { code: -32000, message: 'The operation timed out' } }] } },
    { label: 'an unrelated tool result', change: (value: McpServerReceipt) => { value.toolResults[0]!.id = 99 } },
    { label: 'a lost native error flag', change: (value: McpServerReceipt) => { value.toolResults[0]!.isError = false } },
    { label: 'an altered native refusal reason', change: (value: McpServerReceipt) => { value.toolResults[0]!.text = 'FORM_ROUND_TRIP_REFUSED: other' } },
  ])('rejects $label as unsupported-feature proof', ({ change }) => {
    const value = refusalReceipt()
    change(value)
    expect(() => nativeMcpRefusal(value)).toThrow()
  })
})

describe('mcpReceiptRequestId', () => {
  it.each([0, -1, Number.MIN_SAFE_INTEGER, Number.MAX_SAFE_INTEGER, '', '0', 'native-call-한글'])('preserves a valid typed request ID: %j', (value) => {
    expect(mcpReceiptRequestId(value, 'request ID')).toBe(value)
  })

  it.each([undefined, null, false, {}, [], 0.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1, Number.MIN_SAFE_INTEGER - 1])('rejects an invalid request ID: %j', (value) => {
    expect(() => mcpReceiptRequestId(value, 'request ID')).toThrow('request ID')
  })
})
