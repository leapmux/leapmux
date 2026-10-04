import type { McpServerReceipt } from '../helpers/mcpServerReceipt'
import { describe, expect, it } from 'vitest'
import { cursorAutomaticMcpDecline } from './mcpScenario'

function nativeReceipt(): McpServerReceipt {
  return {
    initializeCapabilities: { elicitation: { form: {} } },
    toolCatalogs: [{ id: 0, tools: [{ name: 'ask', inputSchema: { type: 'object', properties: {}, additionalProperties: false } }] }],
    elicitationRequests: [{ id: 0, toolRequestId: 0, params: { mode: 'form', message: 'Choose the native form.', requestedSchema: { type: 'object', properties: { enabled: { type: 'boolean' } } } } }],
    elicitationReplies: [{ id: 0, kind: 'result', result: { action: 'decline' } }],
    toolResults: [{ id: 0, tool: 'ask', text: 'FORM_ROUND_TRIP_DECLINED', isError: false }],
  }
}

describe('cursorAutomaticMcpDecline', () => {
  it('keeps the exact native capabilities and correlates zero-valued request IDs', () => {
    const receipt = nativeReceipt()
    const result = cursorAutomaticMcpDecline(receipt)
    expect(result.request).toBe(receipt.elicitationRequests[0])
    expect(result.reply).toBe(receipt.elicitationReplies[0])
    expect(result.toolResult).toBe(receipt.toolResults[0])
    expect(result.reply.result).toEqual({ action: 'decline' })
    expect(receipt.initializeCapabilities).toEqual({ elicitation: { form: {} } })
  })

  it.each([
    { label: 'missing initialization', change(receipt: McpServerReceipt) { receipt.initializeCapabilities = null } },
    { label: 'missing form capability', change(receipt: McpServerReceipt) { receipt.initializeCapabilities = { elicitation: {} } } },
    { label: 'missing ask catalog', change(receipt: McpServerReceipt) { receipt.toolCatalogs = [] } },
    { label: 'missing native request', change(receipt: McpServerReceipt) { receipt.elicitationRequests = [] } },
    { label: 'missing matching reply', change(receipt: McpServerReceipt) { receipt.elicitationReplies = [{ id: 1, kind: 'result', result: { action: 'decline' } }] } },
    { label: 'native error reply', change(receipt: McpServerReceipt) { receipt.elicitationReplies = [{ id: 0, kind: 'error', error: { code: -32601, message: 'Method not found.' } }] } },
    { label: 'accepted input', change(receipt: McpServerReceipt) { receipt.elicitationReplies = [{ id: 0, kind: 'result', result: { action: 'accept', content: { enabled: false } } }] } },
    { label: 'cancelled input', change(receipt: McpServerReceipt) { receipt.elicitationReplies = [{ id: 0, kind: 'result', result: { action: 'cancel' } }] } },
    { label: 'decline with supplied input', change(receipt: McpServerReceipt) { receipt.elicitationReplies = [{ id: 0, kind: 'result', result: { action: 'decline', content: {} } }] } },
    { label: 'missing matching tool result', change(receipt: McpServerReceipt) { receipt.toolResults[0]!.id = 1 } },
    { label: 'wrong native tool', change(receipt: McpServerReceipt) { receipt.toolResults[0]!.tool = 'echo' } },
    { label: 'failed native result', change(receipt: McpServerReceipt) { receipt.toolResults[0]!.isError = true } },
    { label: 'changed native result', change(receipt: McpServerReceipt) { receipt.toolResults[0]!.text = 'FORM_ROUND_TRIP_FAILED' } },
  ])('refuses the $label boundary', ({ change }) => {
    const receipt = nativeReceipt()
    change(receipt)
    expect(() => cursorAutomaticMcpDecline(receipt)).toThrow(/Cursor MCP/)
  })
})
