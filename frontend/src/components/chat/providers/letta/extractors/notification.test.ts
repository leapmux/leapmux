import { describe, expect, it } from 'vitest'
import { LETTA_MODEL_ERROR_MARKER, lettaLoopError, lettaSubagentSnapshot } from '~/test-support/lettaFixtures'
import { lettaNotificationEntry } from './notification'

describe('lettaNotificationEntry', () => {
  it('reads a summary as a compaction boundary with valid sizes', () => {
    const frame = { message_type: 'summary_message', compaction_stats: { trigger: 'context_window_limit', context_tokens_before: 759, context_tokens_after: 396 } }
    expect(lettaNotificationEntry(frame)).toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'context_window_limit', pre: 759, post: 396 } }])
    expect(lettaNotificationEntry({ ...frame, compaction_stats: { context_tokens_before: -1, context_tokens_after: NaN } }))
      .toEqual([{ kind: 'compaction', phase: 'end', detail: {} }])
    expect(lettaNotificationEntry({ message_type: 'event_message', event_type: 'compaction' })).toEqual([])
  })
  it('reads a stored warning as a status without raw protocol fields', () => {
    expect(lettaNotificationEntry({ message_type: 'status', level: 'warning', message: ' Stream recovered. ' }))
      .toEqual([{ kind: 'status', text: 'Stream recovered.' }])
    expect(lettaNotificationEntry({ message_type: 'status', message: ' ' })).toEqual([])
  })
  it('draws the words that Letta Code states for a loop error', () => {
    const delta = lettaLoopError(LETTA_MODEL_ERROR_MARKER, true)

    expect(lettaNotificationEntry(delta)).toEqual([{ kind: 'text', text: `400: {"type":"invalid_request_error","code":"invalid_request_error","message":"${LETTA_MODEL_ERROR_MARKER}"}` }])
    expect(delta.message).toContain(LETTA_MODEL_ERROR_MARKER)
  })

  it('prefers the notice of Letta Code to the structured error', () => {
    const delta = { message_type: 'loop_error', message: 'Connection to Letta service failed. Please retry.', api_error: { message: 'The raw service error.' } }

    expect(lettaNotificationEntry(delta)).toEqual([{ kind: 'text', text: 'Connection to Letta service failed. Please retry.' }])
  })

  it('trims the words and reads the structured error when the notice is blank', () => {
    expect(lettaNotificationEntry({ message_type: 'loop_error', message: '\n  Words with edges.  \n' })).toEqual([{ kind: 'text', text: 'Words with edges.' }])
    expect(lettaNotificationEntry({ message_type: 'loop_error', message: ' ', api_error: { message: ' Structured words. ' } })).toEqual([{ kind: 'text', text: 'Structured words.' }])
  })

  it.each([
    ['states no words', { message_type: 'loop_error', is_terminal: true }],
    ['states a blank notice and a blank structured error', { message_type: 'loop_error', message: '', api_error: { message: '  ' } }],
    ['states a notice that is not text', { message_type: 'loop_error', message: { nested: 'words' } }],
    ['states a structured error that is not an object', { message_type: 'loop_error', api_error: 'words' }],
  ])('returns no entry for a loop error that %s', (_name, delta) => {
    expect(lettaNotificationEntry(delta)).toEqual([])
  })

  it('draws nothing for the subagent snapshot', () => {
    expect(lettaNotificationEntry(lettaSubagentSnapshot())).toEqual([])
    expect(lettaNotificationEntry({ ...lettaSubagentSnapshot(), subagents: [{ id: 'subagent-1', status: 'running' }] })).toEqual([])
  })

  it('reads a retry delta and returns no entry for an unknown notice', () => {
    const retry = { message_type: 'retry', message: 'Retrying request (1/3)', attempt: 1, max_attempts: 3, delay_ms: 1000 }
    const unknown = { message_type: 'future_notice', detail: 'A later release sends this.' }

    expect(lettaNotificationEntry(retry)).toEqual([{ kind: 'retry', scope: 'api', attempt: 1, maxAttempts: 3, delayMs: 1000, error: 'Retrying request (1/3)' }])
    expect(lettaNotificationEntry(unknown)).toEqual([])
  })

  it('reads the cloud retry form without transport metadata in the row', () => {
    const delta = { message_type: 'retry', retry_kind: 'transport', provider: 'cloud', from_transport: 'websocket', to_transport: 'sse', error_code: 'disconnected', step_id: 'step-1', attempt: 0, max_attempts: 3, delay_ms: 0, message: 'Transport retry.' }
    expect(lettaNotificationEntry(delta)).toEqual([{ kind: 'retry', scope: 'api', attempt: 0, maxAttempts: 3, delayMs: 0, error: 'Transport retry.' }])
  })

  it('reads a retry event message and validates its fields', () => {
    expect(lettaNotificationEntry({ message_type: 'event_message', event_type: 'retry', event_data: { attempt: 1, max_attempts: 3, delay_ms: 1000, message: 'Retry.' } }))
      .toEqual([{ kind: 'retry', scope: 'api', attempt: 1, maxAttempts: 3, delayMs: 1000, error: 'Retry.' }])
    expect(lettaNotificationEntry({ message_type: 'event_message', event_type: 'retry', event_data: { attempt: -1, max_attempts: 1.5, delay_ms: Infinity, message: ' ' } }))
      .toEqual([{ kind: 'retry', scope: 'api' }])
    expect(lettaNotificationEntry({ message_type: 'event_message', event_type: 'future' })).toEqual([])
  })

  it('does not read a snapshot word from a frame that states another type', () => {
    const other = { type: 'update_loop_status', subagents: [] }

    expect(lettaNotificationEntry(other)).toEqual([])
  })
})
