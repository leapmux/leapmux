import { describe, expect, it, vi } from 'vitest'
import { COPILOT_APPROVAL_SCOPE, COPILOT_DECISION, COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { copilotDecisionOption, copilotPermissionOptions, sendCopilotPermissionResponse } from './permissionOptions'

function permission(request: Record<string, unknown>): Record<string, unknown> {
  return {
    jsonrpc: '2.0',
    method: 'session.event',
    params: { sessionId: 'session-1', event: { id: 'event-1', type: COPILOT_EVENT.PermissionRequested, data: { requestId: 'native-1', permissionRequest: request } } },
  }
}

describe('copilotPermissionOptions', () => {
  // Copilot states no option list of its own, so LeapMux states the decisions the
  // runtime accepts for that request.
  it('offers both wider scopes when the approval rule is expressible', () => {
    const options = copilotPermissionOptions(permission({ kind: 'read', path: '/project/main.go' }))
    expect(options.map(option => option.optionId)).toEqual([
      COPILOT_APPROVAL_SCOPE.Once,
      COPILOT_APPROVAL_SCOPE.Session,
      COPILOT_APPROVAL_SCOPE.Project,
      'reject',
    ])
  })

  // A rule this build cannot construct would claim a scope the runtime never
  // applies, so only the single approval is offered.
  it.each([
    { kind: 'write', canOfferSessionApproval: false },
    { kind: 'shell', canOfferSessionApproval: true, commands: [] },
    { kind: 'shell', canOfferSessionApproval: true, commands: [{ identifier: '' }] },
    { kind: 'mcp' },
    { kind: 'future_kind' },
  ])('offers one approval alone for %o', (request) => {
    expect(copilotPermissionOptions(permission(request)).map(option => option.optionId))
      .toEqual([COPILOT_APPROVAL_SCOPE.Once, 'reject'])
  })

  it('sends the chosen scope with the approval', async () => {
    const sent: string[] = []
    const respond = vi.fn(async (bytes: Uint8Array) => {
      sent.push(new TextDecoder().decode(bytes))
    })
    for (const scope of [COPILOT_APPROVAL_SCOPE.Once, COPILOT_APPROVAL_SCOPE.Session, COPILOT_APPROVAL_SCOPE.Project])
      await sendCopilotPermissionResponse(respond, 'request-1', scope)
    await sendCopilotPermissionResponse(respond, 'request-1', 'reject')
    const answers = sent.map(text => JSON.parse(text).response.response)
    expect(answers.slice(0, 3)).toEqual([
      { behavior: 'allow', scope: COPILOT_APPROVAL_SCOPE.Once },
      { behavior: 'allow', scope: COPILOT_APPROVAL_SCOPE.Session },
      { behavior: 'allow', scope: COPILOT_APPROVAL_SCOPE.Project },
    ])
    expect(answers[3].behavior).toBe('deny')
  })
})

describe('copilotDecisionOption', () => {
  // A saved answer holds the runtime's own decision word. Reading it back to the
  // option its button drew is what lets the finished row show the words the reader
  // clicked.
  it('reads each decision word back to the option that sends it', () => {
    const payload = permission({ kind: 'read' })
    const idFor = (decision: string) => copilotDecisionOption(payload, decision)?.optionId
    expect(idFor(COPILOT_DECISION.ApproveOnce)).toBe(COPILOT_APPROVAL_SCOPE.Once)
    expect(idFor(COPILOT_DECISION.ApproveForSession)).toBe(COPILOT_APPROVAL_SCOPE.Session)
    expect(idFor(COPILOT_DECISION.ApproveForLocation)).toBe(COPILOT_APPROVAL_SCOPE.Project)
    expect(idFor(COPILOT_DECISION.Reject)).toBe('reject')
  })

  // The request offered no session rule, so no button carried those words. The kind
  // still states what the answer was, which beats showing the wire word.
  it('keeps the kind when the request offered no such option', () => {
    const option = copilotDecisionOption(permission({ kind: 'write', canOfferSessionApproval: false }), COPILOT_DECISION.ApproveForSession)
    expect(option).toEqual({ optionId: COPILOT_APPROVAL_SCOPE.Session, kind: 'allow_always' })
  })

  it('answers undefined for a decision word this build does not know', () => {
    expect(copilotDecisionOption(permission({ kind: 'read' }), 'a-word-a-later-runtime-adds')).toBeUndefined()
  })

  // The decision word arrives off a persisted row, so a member of `Object.prototype`
  // reaches the two lookup tables. Each answers a FUNCTION there, which an index read
  // hands back as a real option -- and the caller then calls `startsWith` on it.
  it.each(['toString', '__proto__', 'constructor', 'valueOf'])('answers undefined for the prototype member %s', (decision) => {
    expect(() => copilotDecisionOption(permission({ kind: 'read' }), decision)).not.toThrow()
    expect(copilotDecisionOption(permission({ kind: 'read' }), decision)).toBeUndefined()
  })
})
