import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { deriveToolCallStatus } from '../../../model/toolCallLifecycle'
import { museItemLifecycle } from './toolCommon'

describe('museItemLifecycle', () => {
  it.each([
    ['inProgress', false, 'in_progress', null, false],
    ['completed', true, 'completed', 'succeeded', true],
    ['failed', true, 'completed', 'failed', true],
    ['timedOut', true, 'completed', 'failed', true],
    ['rejected', true, 'completed', 'declined', true],
    ['cancelled', true, 'completed', 'interrupted', true],
  ])('reads the declared native %s status once', (status, final, frameStatus, outcome, landed) => {
    expect(museItemLifecycle(status, undefined)).toEqual({
      nativeFinal: final,
      facts: {
        frameStatus,
        providerOutcome: outcome,
        retainedOutcome: null,
        rowFinal: final,
        resultFrameLanded: landed,
      },
    })
  })

  it.each([undefined, null, 0, false, true, [], {}, '', 'futureFinal'])('keeps unknown or malformed status without an invented outcome: %j', (status) => {
    const lifecycle = museItemLifecycle(status, undefined)
    expect(lifecycle).toEqual({
      nativeFinal: true,
      facts: { frameStatus: 'incomplete', providerOutcome: null, retainedOutcome: null, rowFinal: true, resultFrameLanded: false },
    })
    expect(deriveToolCallStatus(lifecycle.facts, false)).toBe('incomplete')
  })

  it.each([
    [undefined, false, null, 'in_progress'],
    [MessageCompletion.UNSPECIFIED, false, null, 'in_progress'],
    [MessageCompletion.COMPLETE, true, 'succeeded', 'incomplete'],
    [MessageCompletion.INTERRUPTED, true, 'interrupted', 'cancelled'],
    [MessageCompletion.ERROR, true, 'failed', 'failed'],
    [MessageCompletion.FINISHED, true, null, 'incomplete'],
  ] as const)('keeps retained completion %s separate from a native result', (completion, final, outcome, status) => {
    const lifecycle = museItemLifecycle('inProgress', completion)
    expect(lifecycle).toEqual({
      nativeFinal: false,
      facts: { frameStatus: 'in_progress', providerOutcome: null, retainedOutcome: outcome, rowFinal: final, resultFrameLanded: false },
    })
    expect(deriveToolCallStatus(lifecycle.facts, false)).toBe(status)
  })

  it('requires an actual result before a native completed item claims success', () => {
    const { facts } = museItemLifecycle('completed', undefined)
    expect(deriveToolCallStatus(facts, false)).toBe('incomplete')
    expect(deriveToolCallStatus(facts, true)).toBe('completed')
  })
})
