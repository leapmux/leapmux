import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyLettaMessage } from './classification'
import { lettaRelatedMessages, lettaSpanRole } from './spanRole'

describe('classifyLettaMessage', () => {
  it('recognizes a native child tool_call_message as a request', () => {
    const frame = {
      type: 'message',
      message_type: 'tool_call_message',
      tool_calls: [{ tool_call_id: 'call-read-native', name: 'Read', arguments: '{"file_path":"note.txt"}' }],
    }
    const resolved = input(frame, undefined, AgentProvider.LETTA)

    expect(classifyLettaMessage(resolved)).toEqual({ kind: 'tool_use' })
    expect(lettaSpanRole(resolved)).toBe('request')
  })
})

describe('letta native tool phases', () => {
  it('keeps current-window progress out of the finished result side', () => {
    const frame = {
      type: 'message',
      id: 'synthetic-tool-return-stream-call-1',
      message_type: 'tool_return_message',
      run_id: 'run-1',
      status: 'success',
      tool_call_id: 'call-1',
      tool_return: 'current native window',
      tool_returns: [{
        tool_call_id: 'call-1',
        status: 'success',
        tool_return: 'current native window',
        stdout: ['current native window'],
      }],
    }
    const parsed = input(frame, undefined, AgentProvider.LETTA)
    expect(classifyLettaMessage(parsed)).toEqual({ kind: 'hidden' })
    expect(lettaSpanRole(parsed)).toBe('none')
    expect(lettaRelatedMessages(parsed)).toEqual([])
  })

  it('keeps an empty lifecycle end out of the result side', () => {
    const parsed = input({ id: 'native-start', message_type: 'client_tool_end', run_id: 'run-1', tool_call_id: 'call-1', status: 'success' }, undefined, AgentProvider.LETTA)
    expect(classifyLettaMessage(parsed)).toEqual({ kind: 'hidden' })
    expect(lettaSpanRole(parsed)).toBe('none')
    expect(lettaRelatedMessages(parsed)).toEqual([])
  })

  it.each(['', 0, false, null])('retains real client end data without a falsy fallback: %j', (tool_return) => {
    const parsed = input({ id: 'native-end', message_type: 'client_tool_end', run_id: 'run-1', tool_call_id: 'call-1', status: 'success', tool_return }, undefined, AgentProvider.LETTA)
    expect(classifyLettaMessage(parsed)).toEqual({ kind: 'tool_result' })
    expect(lettaSpanRole(parsed)).toBe('result')
  })

  it('preserves the actual synthetic final return', () => {
    const parsed = input({ type: 'message', id: 'synthetic-tool-return-native-final', message_type: 'tool_return_message', run_id: 'run-1', tool_call_id: 'call-1', status: 'success', tool_return: 'complete final result' }, undefined, AgentProvider.LETTA)
    expect(classifyLettaMessage(parsed)).toEqual({ kind: 'tool_result' })
    expect(lettaSpanRole(parsed)).toBe('result')
  })

  it.each([
    '',
    'synthetic-tool-return-stream-call-1-extra',
    'synthetic-tool-return-stream-foreign-call',
    'synthetic-tool-return-native-final',
    'synthetic-interrupt-tool-return-native-final',
  ])('preserves a genuine return whose ID differs from the exact progress ID: %s', (id) => {
    const parsed = input({ type: 'message', id, message_type: 'tool_return_message', run_id: 'run-1', tool_call_id: 'call-1', status: 'success', tool_return: 'Native result.' }, undefined, AgentProvider.LETTA)
    expect(classifyLettaMessage(parsed)).toEqual({ kind: 'tool_result' })
    expect(lettaSpanRole(parsed)).toBe('result')
    expect(lettaRelatedMessages(parsed)).toEqual(['request'])
  })

  it.each(['', 0, false, null])('keeps error result data on the result side without a falsy fallback: %j', (tool_return) => {
    const parsed = input({ id: 'native-end', message_type: 'client_tool_end', run_id: 'run-1', tool_call_id: 'call-1', status: 'error', tool_return }, undefined, AgentProvider.LETTA)
    expect(classifyLettaMessage(parsed)).toEqual({ kind: 'tool_result' })
    expect(lettaSpanRole(parsed)).toBe('result')
    expect(lettaRelatedMessages(parsed)).toEqual(['request'])
  })

  it('keeps an error lifecycle without returned data outside the pair', () => {
    const parsed = input({ id: 'native-end', message_type: 'client_tool_end', run_id: 'run-1', tool_call_id: 'call-1', status: 'error' }, undefined, AgentProvider.LETTA)
    expect(classifyLettaMessage(parsed)).toEqual({ kind: 'hidden' })
    expect(lettaSpanRole(parsed)).toBe('none')
    expect(lettaRelatedMessages(parsed)).toEqual([])
  })

  it('keeps unknown native records on the unknown-role fallback', () => {
    const parsed = input({ message_type: 'future-native-kind', tool_call_id: 'call-1' }, undefined, AgentProvider.LETTA)
    expect(lettaSpanRole(parsed)).toBe('other')
  })
})
