import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { copilotControlResponseSummary } from './controlResponse'

/** One native permission request, as the runtime sends it. */
function permissionRequest(request: Record<string, unknown>): Record<string, unknown> {
  return {
    jsonrpc: '2.0',
    method: 'session.event',
    params: {
      sessionId: 'session-1',
      event: { id: 'event-1', type: COPILOT_EVENT.PermissionRequested, data: { permissionRequest: request } },
    },
  }
}

/** One stored answer, as the worker persists it: the runtime's own decision word. */
function answered(kind: string): Record<string, unknown> {
  return { type: 'control_response', response: { subtype: 'success', request_id: 'request-1', response: { kind } } }
}

describe('copilotControlResponseSummary', () => {
  // The saved row reads the words the decision BUTTON carried, exactly as the Agent
  // Client Protocol providers do. Before this, Copilot alone spoke in the past tense
  // ("Allowed once") and the same decision read two ways across providers.
  it('reads a saved permission decision in the words its button carried', () => {
    const request = permissionRequest({ kind: 'read', canOfferSessionApproval: true })
    const display = (kind: string) => copilotControlResponseSummary({
      claimToken: 'claim-1',
      requestId: 'request-1',
      request,
      response: answered(kind),
    } satisfies PersistedControlResponse)

    expect(display('approve-once')).toEqual({ kind: 'label', text: 'Allow once' })
    expect(display('approve-for-session')).toEqual({ kind: 'label', text: 'Allow for this session' })
    expect(display('approve-for-location')).toEqual({ kind: 'label', text: 'Allow for this project' })
    expect(display('reject')).toEqual({ kind: 'label', text: 'Reject' })
  })

  // A request that offers no session-wide rule draws no session button, so the words
  // come from the decision kind instead of from an option the row never showed.
  it('labels a scope the request itself could not offer', () => {
    const display = copilotControlResponseSummary({
      claimToken: 'claim-1',
      requestId: 'request-1',
      request: permissionRequest({ kind: 'write', canOfferSessionApproval: false }),
      response: answered('approve-for-session'),
    } satisfies PersistedControlResponse)

    expect(display).toEqual({ kind: 'label', text: 'Allow always' })
  })

  // A request the runtime withdrew was answered by nobody.
  it('states a cancelled request rather than a decision', () => {
    const display = copilotControlResponseSummary({
      claimToken: 'claim-1',
      requestId: 'request-1',
      request: permissionRequest({ kind: 'read' }),
      response: answered('cancelled'),
    } satisfies PersistedControlResponse)

    expect(display).toEqual({ kind: 'label', text: 'Cancelled' })
  })

  it('answers null for a decision word this build does not know', () => {
    const display = copilotControlResponseSummary({
      claimToken: 'claim-1',
      requestId: 'request-1',
      request: permissionRequest({ kind: 'read' }),
      response: answered('a-word-a-later-runtime-adds'),
    } satisfies PersistedControlResponse)

    expect(display).toBeNull()
  })

  // The decision word is wire data off a persisted row, so a member of
  // `Object.prototype` reaches the two decision tables. An index read answers a
  // FUNCTION for each of these four, which `option.kind.startsWith` then calls -- and
  // the whole message goes to the error boundary rather than the row.
  it.each(['toString', '__proto__', 'constructor', 'valueOf'])('answers null for the prototype member %s', (decision) => {
    const read = () => copilotControlResponseSummary({
      claimToken: 'claim-1',
      requestId: 'request-1',
      request: permissionRequest({ kind: 'read' }),
      response: answered(decision),
    } satisfies PersistedControlResponse)

    expect(read).not.toThrow()
    expect(read()).toBeNull()
  })
})
