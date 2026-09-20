import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { claudeResultDivider } from './resultDivider'

/** The envelope the command line interface closes a turn with. */
function resultFrame(extra: Record<string, unknown> = {}): Record<string, unknown> {
  return { type: 'result', duration_ms: 1200, is_error: true, ...extra }
}

describe('claudeResultDivider', () => {
  it('answers null for a frame that is no result envelope', () => {
    expect(claudeResultDivider({ type: 'assistant' })).toBeNull()
    expect(claudeResultDivider('not an object')).toBeNull()
  })

  it('joins the stated reasons into the detail of a non-success subtype', () => {
    const divider = claudeResultDivider(resultFrame({ subtype: 'error_during_execution', errors: ['first', 'second'] }))
    expect(divider?.detail).toBe('first\nsecond')
    expect(divider?.isError).toBe(true)
  })

  // `errors` arrives off the wire, and both branches JOIN the list into the words a
  // reader sees. A cast let an object through, and the join wrote "[object Object]"
  // where the reason belongs.
  it('drops a reason entry that is no string', () => {
    const errors = ['first', { code: 500 }, 42, null, 'second']
    expect(claudeResultDivider(resultFrame({ subtype: 'error_during_execution', errors }))?.detail).toBe('first\nsecond')
    expect(claudeResultDivider(resultFrame({ errors }))?.label).toContain('first; second')
  })

  it('falls back to the result text when every stated reason is unreadable', () => {
    const divider = claudeResultDivider(resultFrame({ subtype: 'error_during_execution', errors: [{ code: 500 }], result: 'the raw reason' }))
    expect(divider?.detail).toBe('the raw reason')
  })

  it('answers an interrupted turn ahead of the error branch', () => {
    const divider = claudeResultDivider(resultFrame({ errors: ['ignored'] }), MessageCompletion.INTERRUPTED)
    expect(divider?.isError).toBeUndefined()
    expect(divider?.detail).toBeUndefined()
  })
})
