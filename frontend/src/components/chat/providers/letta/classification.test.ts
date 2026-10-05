import type { NotificationEntry } from '../../model/notification'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { LETTA_MODEL_ERROR_MARKER, lettaLoopError, lettaModelErrorThread, lettaSubagentSnapshot } from '~/test-support/lettaFixtures'
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
      { kind: 'text', text: open.message },
      { kind: 'text', text: final.message },
    ])
  })

  // The first member of a failed turn is the snapshot that opens every turn, so the
  // error never sits first.
  it('draws the error that follows the snapshot which opens a failed turn', () => {
    const [, open, final] = lettaModelErrorThread(marker)

    expect(threadEntries(lettaModelErrorThread(marker))).toEqual([
      { kind: 'text', text: open?.message },
      { kind: 'text', text: final?.message },
    ])
  })

  it('draws the words that Letta Code states for a loop error, not the frame', () => {
    const frame = lettaLoopError(marker, true)

    expect(classifyLettaMessage(input(frame, null, AgentProvider.LETTA))).toEqual({
      kind: 'notification',
      entries: [{ kind: 'text', text: frame.message }],
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
  ])('draws a loop error with %s as its raw frame', (_name, frame) => {
    expect(classifyLettaMessage(input(frame, null, AgentProvider.LETTA))).toEqual({
      kind: 'notification',
      entries: [{ kind: 'text', text: JSON.stringify(frame) }],
    })
  })

  it('draws a notice that this build does not know as its raw frame', () => {
    const frame = { message_type: 'future_notice', detail: 'A later release sends this.' }

    expect(classifyLettaMessage(input(frame, null, AgentProvider.LETTA))).toEqual({
      kind: 'notification',
      entries: [{ kind: 'text', text: JSON.stringify(frame) }],
    })
  })

  // The snapshot is protocol state. The Worker draws each child of it as the child's
  // own rows and tab. It arrives at the start of every turn and at each state change
  // of a child, so drawing every member of a thread would show them all.
  it('hides the subagent snapshot, alone and in a thread', () => {
    const snapshot = lettaSubagentSnapshot()

    expect(classifyLettaMessage(input(snapshot, null, AgentProvider.LETTA))).toEqual({ kind: 'hidden' })
    expect(classifyLettaMessage(input(snapshot, { old_seqs: [], messages: [snapshot, snapshot] }, AgentProvider.LETTA))).toEqual({ kind: 'hidden' })
  })

  it('hides a thread that holds no member', () => {
    expect(classifyLettaMessage(input(undefined, { old_seqs: [], messages: [] }, AgentProvider.LETTA))).toEqual({ kind: 'hidden' })
  })
})
