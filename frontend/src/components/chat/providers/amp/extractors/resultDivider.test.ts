import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { ampResultDivider } from './resultDivider'

describe('ampResultDivider', () => {
  it('reads a turn that ended', () => {
    expect(ampResultDivider({ type: 'result', subtype: 'success', is_error: false, result: 'done', duration_ms: 2000 })).toEqual({ label: 'Turn ended (2.0s)' })
    expect(ampResultDivider({ type: 'result', subtype: 'success', is_error: false })).toEqual({ label: 'Turn ended' })
  })

  it('reads a turn that failed, with Amp\'s reason', () => {
    expect(ampResultDivider({ type: 'result', subtype: 'error_during_execution', is_error: true, error: 'Model Provider Overloaded' }))
      .toEqual({ label: 'Turn failed — Model Provider Overloaded', isError: true })
    expect(ampResultDivider({ type: 'result', subtype: 'error_during_execution' })).toEqual({ label: 'Turn failed', isError: true })
  })

  // Either signal states a failure, so the error flag alone is enough.
  it('reads a row flagged as an error as failed, whatever its subtype', () => {
    expect(ampResultDivider({ type: 'result', subtype: 'success', is_error: true, error: 'boom' }))
      .toEqual({ label: 'Turn failed — boom', isError: true })
  })

  it('states no duration that is not a number', () => {
    expect(ampResultDivider({ type: 'result', subtype: 'success', is_error: false, duration_ms: '2000' })).toEqual({ label: 'Turn ended' })
  })

  // Amp reports a stop that the reader asked for as an error. LeapMux knows that it asked.
  it('reads a turn the reader stopped as interrupted, whatever the row says', () => {
    expect(ampResultDivider({ type: 'result', subtype: 'error_during_execution', is_error: true, error: 'User cancelled (SIGINT/SIGTERM)' }, MessageCompletion.INTERRUPTED))
      .toEqual({ label: 'Turn interrupted' })
  })

  it('answers null for another row', () => {
    expect(ampResultDivider({ type: 'assistant' })).toBeNull()
    expect(ampResultDivider(null)).toBeNull()
    expect(ampResultDivider('result')).toBeNull()
  })
})
