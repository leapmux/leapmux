import { describe, expect, it } from 'vitest'
import { LETTA_MODEL_ERROR_MARKER, lettaLoopError, lettaSubagentSnapshot } from '~/test-support/lettaFixtures'
import { lettaNotificationEntry } from './notification'

describe('lettaNotificationEntry', () => {
  it('draws the words that Letta Code states for a loop error', () => {
    const delta = lettaLoopError(LETTA_MODEL_ERROR_MARKER, true)

    expect(lettaNotificationEntry(delta)).toEqual([{ kind: 'text', text: delta.message }])
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
  ])('draws the raw frame of a loop error that %s', (_name, delta) => {
    expect(lettaNotificationEntry(delta)).toEqual([{ kind: 'text', text: JSON.stringify(delta) }])
  })

  it('draws nothing for the subagent snapshot', () => {
    expect(lettaNotificationEntry(lettaSubagentSnapshot())).toEqual([])
    expect(lettaNotificationEntry({ ...lettaSubagentSnapshot(), subagents: [{ id: 'subagent-1', status: 'running' }] })).toEqual([])
  })

  it('draws the raw frame of every other notice, retries included', () => {
    const retry = { message_type: 'retry', message: 'Retrying request (1/3)', attempt: 1, max_attempts: 3, delay_ms: 1000 }
    const unknown = { message_type: 'future_notice', detail: 'A later release sends this.' }

    expect(lettaNotificationEntry(retry)).toEqual([{ kind: 'text', text: JSON.stringify(retry) }])
    expect(lettaNotificationEntry(unknown)).toEqual([{ kind: 'text', text: JSON.stringify(unknown) }])
  })

  it('does not read a snapshot word from a frame that states another type', () => {
    const other = { type: 'update_loop_status', subagents: [] }

    expect(lettaNotificationEntry(other)).toEqual([{ kind: 'text', text: JSON.stringify(other) }])
  })
})
