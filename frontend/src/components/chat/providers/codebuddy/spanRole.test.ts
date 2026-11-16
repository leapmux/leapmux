import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyCodebuddyMessage } from './classification'
import { codebuddyNeedsRequest, codebuddySpanRole } from './spanRole'

describe('codebuddySpanRole', () => {
  it('requests the exact live user tool-result partner for a native REPL failure', () => {
    const request = input({
      type: 'assistant',
      session_id: 'native-session',
      message: {
        role: 'assistant',
        content: [
          {
            type: 'tool_use',
            id: 'native-code-1',
            name: 'REPL',
            input: { code: 'throw new Error("computed-" + (70 + 7))' },
          },
        ],
      },
    }, undefined, AgentProvider.CODEBUDDY)
    const result = input({
      type: 'user',
      session_id: 'native-session',
      message: {
        role: 'user',
        content: [
          {
            type: 'tool_result',
            tool_use_id: 'native-code-1',
            is_error: false,
            content: [
              { type: 'text', text: '{"stdout":"","stderr":"","error":"computed-77"}' },
            ],
          },
        ],
      },
    }, undefined, AgentProvider.CODEBUDDY)
    expect(codebuddySpanRole(request)).toBe('request')
    expect(codebuddySpanRole(result)).toBe('result')
    expect(codebuddyNeedsRequest(request)).toBe(false)
    expect(codebuddyNeedsRequest(result)).toBe(true)
  })

  it.each(['', ' '])('ignores a live result with an empty native call ID: %j', (toolUseId) => {
    const result = input({
      type: 'user',
      message: {
        content: [
          { type: 'tool_result', tool_use_id: toolUseId, content: 'output' },
        ],
      },
    }, undefined, AgentProvider.CODEBUDDY)
    expect(codebuddySpanRole(result)).toBe('other')
    expect(codebuddyNeedsRequest(result)).toBe(false)
  })

  it('finds a live result after ordinary text blocks and rejects ordinary user text', () => {
    const result = input({
      type: 'user',
      message: {
        content: [
          { type: 'text', text: 'ordinary' },
          { type: 'tool_result', tool_use_id: 'actual', content: 'output' },
        ],
      },
    }, undefined, AgentProvider.CODEBUDDY)
    expect(codebuddyNeedsRequest(result)).toBe(true)
    expect(codebuddyNeedsRequest(input({
      type: 'user',
      message: { content: 'ordinary' },
    }, undefined, AgentProvider.CODEBUDDY))).toBe(false)
  })

  it('pairs a stored Workflow child function call with its native result', () => {
    const request = input({
      type: 'function_call',
      id: 'request-record',
      callId: 'child-read-1',
      name: 'Read',
      arguments: '{}',
    }, undefined, AgentProvider.CODEBUDDY)
    const output = input({
      type: 'function_call_result',
      id: 'result-record',
      callId: 'child-read-1',
      status: 'completed',
      output: 'CHILD_FILE_MARKER',
    }, undefined, AgentProvider.CODEBUDDY)
    const progress = input({
      type: 'function_call_result',
      id: 'progress-record',
      callId: 'child-read-1',
      status: 'in_progress',
      output: 'partial',
    }, undefined, AgentProvider.CODEBUDDY)
    const resultAlias = input({ type: 'function_call_output', call_id: 'child-read-1', output: 'CHILD_FILE_MARKER' }, undefined, AgentProvider.CODEBUDDY)
    expect(codebuddySpanRole(request)).toBe('request')
    expect(codebuddySpanRole(output)).toBe('result')
    expect(codebuddySpanRole(progress)).toBe('other')
    expect(codebuddySpanRole(resultAlias)).toBe('result')
    expect(codebuddyNeedsRequest(request)).toBe(false)
    expect(codebuddyNeedsRequest(output)).toBe(true)
    expect(codebuddyNeedsRequest(progress)).toBe(false)
    const visible = [request, progress, output].filter(row => classifyCodebuddyMessage(row).kind !== 'hidden')
    expect(visible.map(codebuddySpanRole)).toEqual(['request', 'result'])
  })

  // A stored function call that LeapMux stored with a completion is the final row of its
  // span, as the live `tool_use` frame is. It states its own arguments.
  it('places a stored function call stored with a completion on the result side', () => {
    const retained = {
      ...input({
        type: 'function_call',
        callId: 'retained-call',
        name: 'Read',
        arguments: '{"file_path":"/work/native.txt"}',
      }, undefined, AgentProvider.CODEBUDDY),
      completion: MessageCompletion.INTERRUPTED,
    }
    expect(codebuddySpanRole(retained)).toBe('result')
    expect(codebuddyNeedsRequest(retained)).toBe(false)
  })

  // A result frame states no tool name and no arguments, whatever completion LeapMux
  // stored with it, so it always reads its request.
  it('needs the request for a result frame stored with a completion', () => {
    const result = {
      ...input({
        type: 'user',
        message: {
          content: [
            { type: 'tool_result', tool_use_id: 'call', content: 'output' },
          ],
        },
      }, undefined, AgentProvider.CODEBUDDY),
      completion: MessageCompletion.INTERRUPTED,
    }
    expect(codebuddyNeedsRequest(result)).toBe(true)
  })
})
