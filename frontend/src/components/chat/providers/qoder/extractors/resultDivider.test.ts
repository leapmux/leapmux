import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { qoderResultDivider } from './resultDivider'

// The frames are the shapes that qodercli 1.1.65 writes to stdout. The usage
// blocks are left out.
const ABORTED_RESULT = {
  type: 'result',
  subtype: 'error_during_execution',
  duration_ms: 228,
  is_error: true,
  num_turns: 1,
  stop_reason: 'stop_sequence',
  errors: ['Operation aborted'],
}

describe('qoderResultDivider', () => {
  it('returns null for a row that is not a result', () => {
    expect(qoderResultDivider({ type: 'assistant' })).toBeNull()
    expect(qoderResultDivider('result')).toBeNull()
    expect(qoderResultDivider(null)).toBeNull()
  })

  it('labels a finished turn ended', () => {
    const divider = qoderResultDivider({ type: 'result', subtype: 'success', duration_ms: 377, is_error: false, result: 'done', stop_reason: 'end_turn' })
    expect(divider).toEqual({ label: 'Turn ended (377ms)' })
  })

  it('labels the turn that the worker marked interrupted, with no detail, under the abort shape', () => {
    expect(qoderResultDivider(ABORTED_RESULT, MessageCompletion.INTERRUPTED)).toEqual({ label: 'Turn interrupted (228ms)' })
  })

  it('reads the abort shape as a failure when the worker marked no interrupt', () => {
    expect(qoderResultDivider(ABORTED_RESULT)).toEqual({ label: 'Turn failed (228ms)', isError: true, detail: 'Operation aborted' })
  })

  it('shows the provider error that the CLI relays after a stream failure as the detail', () => {
    // A stream that fails after a partial answer: Qoder keeps the provider's
    // own words in `errors` and nowhere else in the transcript.
    const divider = qoderResultDivider({
      type: 'result',
      subtype: 'error_during_execution',
      duration_ms: 251,
      is_error: true,
      num_turns: 1,
      stop_reason: 'stop_sequence',
      errors: ['NATIVEERRORsseprobe'],
      error_code: 500,
    })
    expect(divider).toEqual({ label: 'Turn failed (251ms)', isError: true, detail: 'NATIVEERRORsseprobe' })
  })

  it('shows the generic text that the CLI writes in place of an HTTP status error', () => {
    const divider = qoderResultDivider({
      type: 'result',
      subtype: 'error_during_execution',
      duration_ms: 136,
      is_error: true,
      errors: ['The request could not be completed. Please try again.'],
      error_code: 'invalid_request_error',
    })
    expect(divider?.detail).toBe('The request could not be completed. Please try again.')
  })

  it('labels a failure that states the success subtype by its error flag', () => {
    // The CLI writes this shape for an authentication failure. The assistant
    // row before it already states the text, so the divider adds no detail.
    const divider = qoderResultDivider({ type: 'result', subtype: 'success', is_error: true, duration_ms: 0, num_turns: 1, result: 'Sign in again.', stop_reason: 'stop_sequence' })
    expect(divider).toEqual({ label: 'Turn failed (0ms)', isError: true })
  })

  it('puts each error on a line of its own', () => {
    const divider = qoderResultDivider({ ...ABORTED_RESULT, errors: ['first', 'second'] })
    expect(divider?.detail).toBe('first\nsecond')
  })

  it.each([
    ['absent', undefined],
    ['empty', []],
    ['blank', ['', '  ']],
    ['not text', [{ message: 'x' }, 42]],
    ['not an array', 'Operation aborted'],
  ])('omits the detail when the errors are %s', (_case, errors) => {
    const divider = qoderResultDivider({ type: 'result', subtype: 'error_during_execution', is_error: true, duration_ms: 5, errors })
    expect(divider).toEqual({ label: 'Turn failed (5ms)', isError: true })
  })
})
