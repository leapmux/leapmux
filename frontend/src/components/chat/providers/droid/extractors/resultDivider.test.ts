import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { droidResultDivider } from './resultDivider'

function turnCompleted(reason: string | undefined): Record<string, unknown> {
  return { type: 'agent_turn_completed', ...(reason === undefined ? {} : { reason }), turnId: 'turn-1' }
}

describe('droidResultDivider', () => {
  it.each(['permission_rejected', 'structured_output_missing', 'structured_output_invalid', 'structured_output_schema_invalid', 'model_usage_exhausted', 'model_authentication_failed', 'model_request_rejected', 'model_provider_unreachable', 'model_provider_unavailable', 'model_rate_limited', 'prompt_rejected', 'completion_persistence_failed', 'no_approver_available'])('reports native reason %s as a failure', (reason) => {
    expect(droidResultDivider(turnCompleted(reason))).toEqual({ label: 'Turn failed', isError: true })
  })

  it('reports an early process exit as an interruption', () => {
    expect(droidResultDivider(turnCompleted('process_exit'))).toEqual({ label: 'Turn interrupted', isError: true })
  })

  it('keeps a native handoff successful', () => {
    expect(droidResultDivider(turnCompleted('spec_handoff'))).toEqual({ label: 'Turn ended' })
  })

  it('reports an unknown native outcome or a recorded failure as failed', () => {
    expect(droidResultDivider(turnCompleted('unknown-outcome'))).toEqual({ label: 'Turn failed', isError: true })
    expect(droidResultDivider(turnCompleted('completed'), MessageCompletion.ERROR)).toEqual({ label: 'Turn failed', isError: true })
  })

  it('says a completed turn ended', () => {
    expect(droidResultDivider(turnCompleted('completed'))).toEqual({ label: 'Turn ended' })
  })

  it('says a turn ended when the frame states no reason', () => {
    expect(droidResultDivider(turnCompleted(undefined))).toEqual({ label: 'Turn ended' })
  })

  // Droid 0.233.0 answers droid.interrupt_session with a turn end whose reason is
  // `cancelled`, for a held model request and for a running tool alike. The Worker
  // stores the frame as it arrived, so this word is the only record of the stop.
  it('states a turn that Droid cancelled as an interruption', () => {
    expect(droidResultDivider(turnCompleted('cancelled'))).toEqual({ label: 'Turn interrupted', isError: true })
  })

  it('states a stop that LeapMux recorded as an interruption, whatever the frame says', () => {
    expect(droidResultDivider(turnCompleted('completed'), MessageCompletion.INTERRUPTED)).toEqual({ label: 'Turn interrupted', isError: true })
  })

  // `error` is the reason that Droid states for a turn that failed. Its reason list
  // has no word `failed`, so a reader that tested for that word never saw a failure.
  it('states a turn that Droid ended with an error as a failure', () => {
    expect(droidResultDivider(turnCompleted('error'))).toEqual({ label: 'Turn failed', isError: true })
  })

  it('returns null for a row that is no turn end', () => {
    expect(droidResultDivider({ type: 'settings_updated', reason: 'cancelled' })).toBeNull()
    expect(droidResultDivider('agent_turn_completed')).toBeNull()
    expect(droidResultDivider(null)).toBeNull()
  })
})
