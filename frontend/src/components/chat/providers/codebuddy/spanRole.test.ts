import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyCodebuddyMessage } from './classification'
import { codebuddyRelatedMessages, codebuddySpanRole } from './spanRole'

describe('codebuddySpanRole', () => {
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
