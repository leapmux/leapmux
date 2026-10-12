import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { DEEPSEEK_HARNESS_CONTROL_EVENT } from '~/generated/contracts/deepseek-harness-protocol'
import { buildControlResponseEnvelope, buildDenyResponse } from '~/utils/controlResponse'
import { deepseekHarnessControlResponseSummary } from './controlResponse'

/** One saved answer to a native approval, as the browser sent it and the worker stored it. */
function savedApproval(response: Record<string, unknown>): PersistedControlResponse {
  return {
    requestId: 'native-event',
    claimToken: 'native-claim',
    request: { event: DEEPSEEK_HARNESS_CONTROL_EVENT.Approval, eventId: 'native-event', request: { toolName: 'bash', callId: 'native-call' } },
    response,
  }
}

describe('deepseekHarnessControlResponseSummary', () => {
  it('reads an allowed approval as the native option it selected', () => {
    expect(deepseekHarnessControlResponseSummary(savedApproval(buildControlResponseEnvelope('native-event', { behavior: 'allow' }))))
      .toStrictEqual({ kind: 'label', text: 'Allow once' })
  })

  it('reads a bare denial as the word its button carried', () => {
    expect(deepseekHarnessControlResponseSummary(savedApproval(buildDenyResponse('native-event'))))
      .toStrictEqual({ kind: 'label', text: 'Deny' })
  })

  // The native approval reply carries no reason. The Worker answers `rejected` alone.
  // The browser sends the reason as the reader's next message.
  // The transcript draws that message as a separate row.
  // The saved row states the decision alone to prevent duplicate feedback.
  it('reads a denial with a typed reason as the decision alone', () => {
    expect(deepseekHarnessControlResponseSummary(savedApproval(buildDenyResponse('native-event', 'Use a dry run.'))))
      .toStrictEqual({ kind: 'label', text: 'Deny' })
  })

  it('reads nothing for an answer to another request', () => {
    expect(deepseekHarnessControlResponseSummary(savedApproval(buildDenyResponse('other-event')))).toBeNull()
  })
})
