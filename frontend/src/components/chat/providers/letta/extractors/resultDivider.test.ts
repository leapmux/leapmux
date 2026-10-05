import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { lettaResultDivider } from './resultDivider'

/** One native `turn_finished` frame, as the App Server sends it. */
function turnFinished(stopReason = 'end_turn'): Record<string, unknown> {
  return {
    type: 'turn_finished',
    turn_id: 'batch-direct-native-turn',
    stop_reason: stopReason,
    run_id: 'local-run-2',
    runtime: { agent_id: 'agent-local-1', conversation_id: 'local-conv-1' },
  }
}

describe('lettaResultDivider', () => {
  // The Worker records how the native `stop_reason` ended the turn, so the divider
  // reads that record and never the stop words of one Letta Code release.
  it('reads a turn that the Worker recorded as interrupted as an interrupted turn', () => {
    expect(lettaResultDivider(turnFinished('cancelled'), MessageCompletion.INTERRUPTED)).toEqual({ label: 'Turn interrupted' })
  })

  it('reads a turn that the Worker recorded as failed as a failed turn', () => {
    expect(lettaResultDivider(turnFinished('error'), MessageCompletion.ERROR)).toEqual({ label: 'Turn failed', isError: true })
  })

  it.each([
    ['a complete turn', MessageCompletion.COMPLETE],
    ['a turn with no record', undefined],
    ['a turn with an unspecified record', MessageCompletion.UNSPECIFIED],
  ])('reads %s as an ended turn', (_name, completion) => {
    expect(lettaResultDivider(turnFinished(), completion)).toEqual({ label: 'Turn ended' })
  })

  it('reads the discriminator from kind when type is absent', () => {
    const { type: _type, ...frame } = turnFinished()
    expect(lettaResultDivider({ ...frame, kind: 'turn_finished' }, MessageCompletion.INTERRUPTED)).toEqual({ label: 'Turn interrupted' })
  })

  it.each([
    ['a stream delta', { type: 'stream_delta', delta: { message_type: 'status', message: 'Interrupted', level: 'warning' } }],
    ['a loop status', { type: 'update_loop_status', loop_status: { status: 'WAITING_ON_INPUT' } }],
    ['an array', [turnFinished()]],
    ['a string', 'turn_finished'],
    ['null', null],
  ])('returns null for %s', (_name, frame) => {
    expect(lettaResultDivider(frame, MessageCompletion.INTERRUPTED)).toBeNull()
  })
})
