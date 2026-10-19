import type { NotificationEntry } from '../../model/notification'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { LETTA_MODEL_ERROR_MARKER, lettaLoopError, lettaMixedErrorThread, lettaSubagentSnapshot } from '~/test-support/lettaFixtures'
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

describe('classifyLettaMessage notices', () => {
  const marker = LETTA_MODEL_ERROR_MARKER
  const words = `400: {"type":"invalid_request_error","code":"invalid_request_error","message":"${marker}"}`

  /** The entries that one stored thread draws. */
  function threadEntries(messages: Record<string, unknown>[]): NotificationEntry[] {
    const category = classifyLettaMessage(input(messages[0], { old_seqs: [], messages }, AgentProvider.LETTA))
    if (category.kind !== 'notification')
      throw new Error(`The thread is a ${category.kind} row, not a notification.`)
    return category.entries
  }

  // The Worker threads notices that arrive back to back into one stored row. A reader
  // that classifies the first member alone drops every member behind it.
  it('keeps every notice of a stored thread, not the first alone', () => {
    const open = lettaLoopError(marker, false)
    const final = lettaLoopError(marker, true)

    expect(threadEntries([open, final])).toEqual([
      { kind: 'text', text: words },
      { kind: 'text', text: words },
    ])
  })

  // A protocol snapshot must not prevent the reader from reading a later stored error.
  it('draws a retained error after a protocol snapshot', () => {
    expect(threadEntries(lettaMixedErrorThread(marker))).toEqual([
      { kind: 'text', text: words },
    ])
  })

  it('draws the words that Letta Code states for a loop error, not the frame', () => {
    const frame = lettaLoopError(marker, true)

    expect(classifyLettaMessage(input(frame, null, AgentProvider.LETTA))).toEqual({
      kind: 'notification',
      entries: [{ kind: 'text', text: words }],
    })
  })

  it('draws the words of the structured error when the loop error states no message', () => {
    const { message: _message, ...frame } = lettaLoopError(marker, true)
    const apiError = frame.api_error as { message: string }

    expect(classifyLettaMessage(input(frame, null, AgentProvider.LETTA))).toEqual({
      kind: 'notification',
      entries: [{ kind: 'text', text: apiError.message }],
    })
  })

  it.each([
    ['no words', { message_type: 'loop_error', run_id: 'local-run-1', is_terminal: true }],
    ['a blank message and no structured error', { message_type: 'loop_error', message: '  ', is_terminal: true }],
    ['a message that is not text', { message_type: 'loop_error', message: 42, is_terminal: true }],
  ])('keeps a loop error with %s visible without raw JSON', (_name, frame) => {
    expect(classifyLettaMessage(input(frame, null, AgentProvider.LETTA))).toEqual({
      kind: 'notification',
      entries: [],
    })
  })

  it('keeps an unknown notice visible without raw JSON', () => {
    const frame = { message_type: 'future_notice', detail: 'A later release sends this.' }

    expect(classifyLettaMessage(input(frame, null, AgentProvider.LETTA))).toEqual({
      kind: 'notification',
      entries: [],
    })
  })

  // The worker stores no snapshot row. An older stored snapshot receives the diagnostic policy.
  it('keeps an old subagent snapshot visible as an unrecognized notice', () => {
    const snapshot = lettaSubagentSnapshot()

    expect(classifyLettaMessage(input(snapshot, null, AgentProvider.LETTA))).toEqual({ kind: 'notification', entries: [] })
    expect(classifyLettaMessage(input(snapshot, { old_seqs: [], messages: [snapshot, snapshot] }, AgentProvider.LETTA))).toEqual({ kind: 'notification', entries: [] })
  })

  it('keeps an absent payload and an invalid native tool call unknown', () => {
    expect(classifyLettaMessage(input(undefined, null, AgentProvider.LETTA))).toEqual({ kind: 'unknown' })
    expect(classifyLettaMessage(input({ message_type: 'tool_call_message', tool_calls: [{ name: 'Bash' }] }, null, AgentProvider.LETTA))).toEqual({ kind: 'unknown' })
  })

  it('does not claim an old payload wrapper as native output', () => {
    const parsed = input({ payload: { message_type: 'client_tool_start', tool_name: 'Bash', tool_call_id: 'old' } }, null, AgentProvider.LETTA)
    expect(classifyLettaMessage(parsed)).toEqual({ kind: 'notification', entries: [] })
    expect(lettaSpanRole(parsed)).toBe('other')
  })

  it('hides a thread that holds no member', () => {
    expect(classifyLettaMessage(input(undefined, { old_seqs: [], messages: [] }, AgentProvider.LETTA))).toEqual({ kind: 'hidden' })
  })
})
