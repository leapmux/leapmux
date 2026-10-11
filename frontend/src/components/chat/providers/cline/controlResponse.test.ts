import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { CLINE_DECLINE_REASON, CLINE_PERMISSION_MODE } from '~/generated/contracts/cline-protocol'
import { clineControlResponseSummary } from './controlResponse'

const approval = (toolName: string) => ({ version: 'v1', event: 'approval.requested', sessionId: 's1', payload: { approvalId: 'approval_1', toolName } })

const question = { version: 'v1', event: 'capability.requested', sessionId: 's1', payload: { requestId: 'capreq_1', capabilityName: 'tool_executor.askQuestion' } }

function summary(request: Record<string, unknown>, response: unknown) {
  return clineControlResponseSummary({ requestId: 'r1', claimToken: '', request, response } as PersistedControlResponse)
}

describe('clineControlResponseSummary', () => {
  // `plugin.test.ts` covers the words of each answer through the plugin. The cases here
  // are the replies whose fields hold nothing, or a value of the wrong type.
  it('words a refusal whose reason is blank as a plain refusal', () => {
    expect(summary(approval('editor'), { approvalId: 'approval_1', approved: false, reason: '   ' })).toEqual({ kind: 'label', text: 'Deny' })
    expect(summary(approval('editor'), { approvalId: 'approval_1', approved: false })).toEqual({ kind: 'label', text: 'Deny' })
    expect(summary(approval('switch_to_act_mode'), { approvalId: 'approval_1', approved: false, reason: ` ${CLINE_DECLINE_REASON.Tool} ` })).toEqual({ kind: 'label', text: 'Reject' })
  })

  it('words an approved plan whose mode it does not know as a plain approval', () => {
    expect(summary(approval('switch_to_act_mode'), { approvalId: 'approval_1', approved: true, permissionMode: CLINE_PERMISSION_MODE.Plan })).toEqual({ kind: 'label', text: 'Approve' })
    expect(summary(approval('switch_to_act_mode'), { approvalId: 'approval_1', approved: true, permissionMode: CLINE_PERMISSION_MODE.Act })).toEqual({ kind: 'label', text: 'Approve (Act)' })
  })

  it('reads a decision that is not a boolean as no answer', () => {
    expect(summary(approval('editor'), { approvalId: 'approval_1', approved: 'true' })).toBeNull()
    expect(summary(approval('editor'), { approvalId: 'approval_1', approved: 1 })).toBeNull()
  })

  it('words a question answered with no text', () => {
    expect(summary(question, { requestId: 'capreq_1', ok: true, payload: { result: '  ' } })).toEqual({ kind: 'label', text: 'No answer' })
    expect(summary(question, { requestId: 'capreq_1', ok: true })).toEqual({ kind: 'label', text: 'No answer' })
    expect(summary(question, { requestId: 'capreq_1', ok: true, payload: { result: 3 } })).toEqual({ kind: 'label', text: 'No answer' })
  })

  it('words a declined question whose error is blank as a plain decline', () => {
    expect(summary(question, { requestId: 'capreq_1', ok: false, error: '  ' })).toEqual({ kind: 'label', text: 'Declined' })
    expect(summary(question, { requestId: 'capreq_1', ok: false })).toEqual({ kind: 'label', text: 'Declined' })
  })

  it('reads a question reply whose outcome is not a boolean as no answer', () => {
    expect(summary(question, { requestId: 'capreq_1', ok: 'yes' })).toBeNull()
  })

  it('reads a reply that is not a record as no answer', () => {
    for (const response of ['Allow', 3, null, ['ok']])
      expect(summary(approval('editor'), response), JSON.stringify(response)).toBeNull()
  })
})
