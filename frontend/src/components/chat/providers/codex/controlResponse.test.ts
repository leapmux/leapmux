import type { PersistedControlResponse } from '../../persistedControlResponse'
import type { CodexDecision } from './controlResponse'
import { describe, expect, it } from 'vitest'
import { codexControlResponseDisplay, codexDecisionKey, codexDecisionLabel } from './controlResponse'

function cr(request: Record<string, unknown> | undefined, response: Record<string, unknown> | undefined): PersistedControlResponse {
  return { provider: 'CODEX', requestId: '7', request, response }
}

const APPROVAL_REQUEST = { method: 'item/commandExecution/requestApproval' }

function decision(d: unknown): Record<string, unknown> {
  return { jsonrpc: '2.0', id: 7, result: { decision: d } }
}

describe('codexdecisionlabel', () => {
  it('maps the known string decisions', () => {
    expect(codexDecisionLabel('accept')).toBe('Allow')
    expect(codexDecisionLabel('acceptForSession')).toBe('Allow for Session')
    expect(codexDecisionLabel('decline')).toBe('Reject')
    expect(codexDecisionLabel('cancel')).toBe('Cancel')
  })

  it('passes an unknown string through and maps amendment objects', () => {
    expect(codexDecisionLabel('somethingElse')).toBe('somethingElse')
    expect(codexDecisionLabel({ acceptWithExecpolicyAmendment: { match: 'npm test' } })).toBe('Allow & Remember')
    expect(codexDecisionLabel({ applyNetworkPolicyAmendment: true })).toBe('Apply Network Policy')
    expect(codexDecisionLabel({ other: 1 })).toBe('Allow')
  })

  it('is total: a malformed non-string, non-object decision degrades to "Allow" without throwing', () => {
    // The live-button path (CodexControlRequest) casts params.availableDecisions straight from wire
    // bytes, so a malformed entry (null / a number) must NOT make the `in` reads throw a TypeError
    // and crash the control banner -- the isObject guard degrades it to the neutral "Allow".
    expect(() => codexDecisionLabel(null as unknown as CodexDecision)).not.toThrow()
    expect(codexDecisionLabel(null as unknown as CodexDecision)).toBe('Allow')
    expect(codexDecisionLabel(3 as unknown as CodexDecision)).toBe('Allow')
  })
})

describe('codexdecisionkey', () => {
  it('returns the string decision or the first key of an amendment object', () => {
    expect(codexDecisionKey('accept')).toBe('accept')
    expect(codexDecisionKey('decline')).toBe('decline')
    expect(codexDecisionKey({ acceptWithExecpolicyAmendment: { match: 'npm test' } })).toBe('acceptWithExecpolicyAmendment')
  })

  it('is total: a malformed non-string, non-object (or empty-object) decision degrades to "unknown" without throwing', () => {
    // Sibling of codexDecisionLabel's totality: the data-testid interpolation used to call
    // Object.keys(decision)[0] directly, which throws TypeError on a null/number entry cast straight
    // from wire bytes (params.availableDecisions) and crashes the whole control-banner <For> render.
    expect(() => codexDecisionKey(null as unknown as CodexDecision)).not.toThrow()
    expect(codexDecisionKey(null as unknown as CodexDecision)).toBe('unknown')
    expect(codexDecisionKey(3 as unknown as CodexDecision)).toBe('unknown')
    expect(codexDecisionKey({})).toBe('unknown')
  })
})

describe('codexcontrolresponsedisplay', () => {
  it('labels string decisions', () => {
    expect(codexControlResponseDisplay(cr(APPROVAL_REQUEST, decision('accept')))).toEqual({ kind: 'label', text: 'Allow' })
    expect(codexControlResponseDisplay(cr(APPROVAL_REQUEST, decision('decline')))).toEqual({ kind: 'label', text: 'Reject' })
  })

  it('labels amendment-object decisions', () => {
    expect(codexControlResponseDisplay(cr(APPROVAL_REQUEST, decision({ acceptWithExecpolicyAmendment: { match: 'touch' } }))))
      .toEqual({ kind: 'label', text: 'Allow & Remember' })
    expect(codexControlResponseDisplay(cr(APPROVAL_REQUEST, decision({ applyNetworkPolicyAmendment: true }))))
      .toEqual({ kind: 'label', text: 'Apply Network Policy' })
  })

  it('returns null for a missing/empty decision (caller degrades)', () => {
    expect(codexControlResponseDisplay(cr(APPROVAL_REQUEST, decision(null)))).toBeNull()
    expect(codexControlResponseDisplay(cr(APPROVAL_REQUEST, decision({})))).toBeNull()
    expect(codexControlResponseDisplay(cr(APPROVAL_REQUEST, { result: {} }))).toBeNull()
  })

  it('renders a deny-with-feedback as a feedback block, collapsing the sentinel', () => {
    const deny = { type: 'control_response', response: { request_id: '7', response: { behavior: 'deny', message: 'Add tests first.' } } }
    expect(codexControlResponseDisplay(cr(APPROVAL_REQUEST, deny))).toEqual({ kind: 'feedback', message: 'Add tests first.' })
    const bare = { response: { response: { behavior: 'deny', message: 'Rejected by user.' } } }
    // Sentinel collapses to no feedback -> the decision path yields nothing -> null (fallback -> Rejected).
    expect(codexControlResponseDisplay(cr(APPROVAL_REQUEST, bare))).toBeNull()
  })

  describe('requestuserinput answers', () => {
    const request = {
      method: 'item/tool/requestUserInput',
      params: { questions: [{ id: 'task', header: 'Task' }, { id: 'reason', header: 'Reason' }] },
    }

    it('renders request-ordered header-labeled answer lines', () => {
      const response = { result: { answers: { task: { answers: ['Inspect'] }, reason: { answers: ['Parity'] } } } }
      expect(codexControlResponseDisplay(cr(request, response))).toEqual({ kind: 'label', text: 'Task: Inspect\nReason: Parity' })
    })

    it('drops all-empty answers and joins multiple values', () => {
      const response = { result: { answers: { task: { answers: ['A', ' ', 'B'] }, reason: { answers: ['', '  '] } } } }
      expect(codexControlResponseDisplay(cr(request, response))).toEqual({ kind: 'label', text: 'Task: A, B' })
    })

    it('appends answer keys not in the request in sorted order', () => {
      const req = { method: 'item/tool/requestUserInput', params: { questions: [{ id: 'task', header: 'Task' }] } }
      const response = { result: { answers: { task: { answers: ['T'] }, zebra: { answers: ['Z'] }, alpha: { answers: ['A'] } } } }
      expect(codexControlResponseDisplay(cr(req, response))).toEqual({ kind: 'label', text: 'Task: T\nalpha: A\nzebra: Z' })
    })

    it('returns null when there are no answers', () => {
      expect(codexControlResponseDisplay(cr(request, { result: { answers: {} } }))).toBeNull()
    })

    it('falls through to the decision label when a requestUserInput is declined', () => {
      // A denied/stopped requestUserInput arrives as a JSON-RPC decision ({result:{decision:'decline'}})
      // while the request method is still requestUserInput. It must read "Reject", NOT degrade to the
      // generic "Responded" label -- the answers branch returning null falls through to the decision path.
      expect(codexControlResponseDisplay(cr(request, decision('decline')))).toEqual({ kind: 'label', text: 'Reject' })
      expect(codexControlResponseDisplay(cr(request, decision('cancel')))).toEqual({ kind: 'label', text: 'Cancel' })
    })

    it('renders a requestUserInput deny-with-feedback as a feedback block on fall-through', () => {
      const deny = { type: 'control_response', response: { request_id: '7', response: { behavior: 'deny', message: 'Need more detail.' } } }
      expect(codexControlResponseDisplay(cr(request, deny))).toEqual({ kind: 'feedback', message: 'Need more detail.' })
    })

    it('renders request-gone answers from the response alone, labeled by key', () => {
      // The pruned request is absent (request-gone), but the answer VALUES live entirely in the
      // response, so they still render -- labeled by the answer key (sorted, no request order) rather
      // than the missing question header, instead of degrading to the generic "Responded" label.
      const response = { result: { answers: { task: { answers: ['Inspect'] }, reason: { answers: ['Parity'] } } } }
      expect(codexControlResponseDisplay(cr(undefined, response))).toEqual({ kind: 'label', text: 'reason: Parity\ntask: Inspect' })
    })
  })
})
