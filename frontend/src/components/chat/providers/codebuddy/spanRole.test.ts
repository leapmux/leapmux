import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyCodebuddyMessage } from './classification'
import { codebuddyRelatedMessages, codebuddySpanRole } from './spanRole'

describe('codebuddySpanRole', () => {
  it('requests the exact live user tool-result partner for a native REPL failure', () => {
    const request = input({ type: 'assistant', session_id: 'native-session', message: { role: 'assistant', content: [{ type: 'tool_use', id: 'native-code-1', name: 'REPL', input: { code: 'throw new Error("computed-" + (70 + 7))' } }] } }, undefined, AgentProvider.CODEBUDDY)
    const result = input({ type: 'user', session_id: 'native-session', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'native-code-1', is_error: false, content: [{ type: 'text', text: '{"stdout":"","stderr":"","error":"computed-77"}' }] }] } }, undefined, AgentProvider.CODEBUDDY)
    expect(codebuddySpanRole(request)).toBe('request')
    expect(codebuddySpanRole(result)).toBe('result')
    expect(codebuddyRelatedMessages(result)).toEqual(['request'])
  })

  it.each(['', ' '])('ignores a live result with an empty native call ID: %j', (toolUseId) => {
    const result = input({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: toolUseId, content: 'output' }] } }, undefined, AgentProvider.CODEBUDDY)
    expect(codebuddySpanRole(result)).toBe('other')
    expect(codebuddyRelatedMessages(result)).toEqual([])
  })

  it('finds a live result after ordinary text blocks and rejects ordinary user text', () => {
    const result = input({ type: 'user', message: { content: [{ type: 'text', text: 'ordinary' }, { type: 'tool_result', tool_use_id: 'actual', content: 'output' }] } }, undefined, AgentProvider.CODEBUDDY)
    expect(codebuddyRelatedMessages(result)).toEqual(['request'])
    expect(codebuddyRelatedMessages(input({ type: 'user', message: { content: 'ordinary' } }, undefined, AgentProvider.CODEBUDDY))).toEqual([])
  })

  it('pairs a stored Workflow child function call with its native result', () => {
    const request = input({ type: 'function_call', id: 'request-record', callId: 'child-read-1', name: 'Read', arguments: '{}' }, undefined, AgentProvider.CODEBUDDY)
    const output = input({ type: 'function_call_result', id: 'result-record', callId: 'child-read-1', status: 'completed', output: 'CHILD_FILE_MARKER' }, undefined, AgentProvider.CODEBUDDY)
    const progress = input({ type: 'function_call_result', id: 'progress-record', callId: 'child-read-1', status: 'in_progress', output: 'partial' }, undefined, AgentProvider.CODEBUDDY)
    const resultAlias = input({ type: 'function_call_output', call_id: 'child-read-1', output: 'CHILD_FILE_MARKER' }, undefined, AgentProvider.CODEBUDDY)
    expect(codebuddySpanRole(request)).toBe('request')
    expect(codebuddySpanRole(output)).toBe('result')
    expect(codebuddySpanRole(progress)).toBe('other')
    expect(codebuddySpanRole(resultAlias)).toBe('result')
    expect(codebuddyRelatedMessages(output)).toEqual(['request'])
    expect(codebuddyRelatedMessages(progress)).toEqual([])
    const visible = [request, progress, output].filter(row => classifyCodebuddyMessage(row).kind !== 'hidden')
    expect(visible.map(codebuddySpanRole)).toEqual(['request', 'result'])
  })
})
