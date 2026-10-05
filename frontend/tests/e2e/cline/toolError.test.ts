import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { clineToolError } from './toolError'

function requestWithToolResult(callId: string, content: string): MockModelRequestRecord {
  return {
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    stepIndex: 1,
    body: {
      messages: [
        { role: 'assistant', content: null, tool_calls: [{ id: callId, type: 'function', function: { name: 'form_probe__ask', arguments: '{}' } }] },
        { role: 'tool', tool_call_id: callId, content },
      ],
    },
  }
}

describe('clineToolError', () => {
  it('returns the decoded message with its quotes', () => {
    const message = 'MCP request to "form_probe" (tools/call) timed out after 10s. Increase the "timeout" field (in seconds) for this server in cline_mcp_settings.json.'
    const request = requestWithToolResult('call-1', JSON.stringify({ error: message }))
    expect(clineToolError(request, 'call-1')).toEqual({ text: message })
  })

  it.each([
    { label: 'plain text', content: 'The tool failed.' },
    { label: 'a successful output', content: JSON.stringify({ result: 'done' }) },
    { label: 'an empty error', content: JSON.stringify({ error: '' }) },
    { label: 'a non-string error', content: JSON.stringify({ error: { message: 'nested' } }) },
    { label: 'an error beside another field', content: JSON.stringify({ error: 'failed', result: 'partial' }) },
    { label: 'a JSON array', content: JSON.stringify([{ error: 'failed' }]) },
  ])('rejects $label', ({ content }) => {
    expect(() => clineToolError(requestWithToolResult('call-1', content), 'call-1')).toThrow('call-1')
  })

  it('rejects a request that carries no result for the call', () => {
    const request = requestWithToolResult('other-call', JSON.stringify({ error: 'failed' }))
    expect(() => clineToolError(request, 'call-1')).toThrow('call-1')
  })
})
