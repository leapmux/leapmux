import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyCodebuddyMessage } from './classification'

describe('classifyCodebuddyMessage', () => {
  it('classifies a stored Workflow child function call and its output as tool rows', () => {
    const request = { type: 'function_call', id: 'request-record', callId: 'child-read-1', name: 'Read', arguments: '{"file_path":"/work/marker.txt"}' }
    const result = { type: 'function_call_result', id: 'result-record', callId: 'child-read-1', status: 'completed', output: { type: 'text', text: 'CHILD_FILE_MARKER' } }
    const resultAlias = { type: 'function_call_output', call_id: 'child-read-1', output: 'CHILD_FILE_MARKER' }
    expect(classifyCodebuddyMessage(input(request, undefined, AgentProvider.CODEBUDDY))).toEqual({ kind: 'tool_use' })
    expect(classifyCodebuddyMessage(input(result, undefined, AgentProvider.CODEBUDDY))).toEqual({ kind: 'tool_result' })
    expect(classifyCodebuddyMessage(input(resultAlias, undefined, AgentProvider.CODEBUDDY))).toEqual({ kind: 'tool_result' })
  })

  it('hides a stored native progress result until the tool finishes', () => {
    const progress = { type: 'function_call_result', callId: 'child-read-1', status: 'in_progress', output: { type: 'text', text: 'partial' } }
    expect(classifyCodebuddyMessage(input(progress, undefined, AgentProvider.CODEBUDDY))).toEqual({ kind: 'hidden' })
  })

  it('classifies a stored Workflow child answer as assistant text', () => {
    const stored = { type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'ARCHIVED_CHILD_TEXT' }] }
    expect(classifyCodebuddyMessage(input(stored, undefined, AgentProvider.CODEBUDDY)))
      .toEqual({ kind: 'assistant_text' })
  })

  it('keeps a stored user record outside assistant text', () => {
    const stored = { type: 'message', role: 'user', content: [{ type: 'input_text', text: 'CHILD_PROMPT' }] }
    expect(classifyCodebuddyMessage(input(stored, undefined, AgentProvider.CODEBUDDY)))
      .toEqual({ kind: 'unknown' })
  })

  it('hides a stored assistant record without output text', () => {
    const stored = { type: 'message', role: 'assistant', content: [] }
    expect(classifyCodebuddyMessage(input(stored, undefined, AgentProvider.CODEBUDDY)))
      .toEqual({ kind: 'hidden' })
  })
})
