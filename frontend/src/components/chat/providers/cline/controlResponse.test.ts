import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { CLINE_DECLINE_REASON, CLINE_PERMISSION_MODE } from '~/generated/contracts/cline-protocol'
import { clineControlResponseSummary } from './controlResponse'

/** The prompts that the Worker reads from a tool approval, a plan approval and a question. */
const TOOL_APPROVAL: ControlPrompt = {
  kind: 'permission',
  permission: { title: 'editor', options: [] },
}
const PLAN_APPROVAL: ControlPrompt = { kind: 'plan' }
const QUESTION: ControlPrompt = {
  kind: 'question',
  questions: [
    { question: 'Which color?', options: [] },
  ],
}

function summary(prompt: ControlPrompt, response: unknown) {
  return clineControlResponseSummary({
    requestId: 'r1',
    claimToken: '',
    request: { version: 'v1', event: 'approval.requested', sessionId: 's1' },
    response,
    prompt,
  } as PersistedControlResponse)
}

describe('clineControlResponseSummary', () => {
  // `plugin.test.ts` covers the words of each answer through the plugin. The cases here
  // are the replies whose fields hold nothing, or a value of the wrong type.
  it('words a refusal whose reason is blank as a plain refusal', () => {
    expect(summary(TOOL_APPROVAL, { approvalId: 'approval_1', approved: false, reason: '   ' })).toEqual({ kind: 'label', text: 'Deny' })
    expect(summary(TOOL_APPROVAL, { approvalId: 'approval_1', approved: false })).toEqual({ kind: 'label', text: 'Deny' })
    expect(summary(PLAN_APPROVAL, { approvalId: 'approval_1', approved: false, reason: ` ${CLINE_DECLINE_REASON.Tool} ` })).toEqual({ kind: 'label', text: 'Reject' })
  })

  it('words an approved plan whose mode it does not know as a plain approval', () => {
    expect(summary(PLAN_APPROVAL, { approvalId: 'approval_1', approved: true, permissionMode: CLINE_PERMISSION_MODE.Plan })).toEqual({ kind: 'label', text: 'Approve' })
    expect(summary(PLAN_APPROVAL, { approvalId: 'approval_1', approved: true, permissionMode: CLINE_PERMISSION_MODE.Act })).toEqual({ kind: 'label', text: 'Approve (Act)' })
  })

  it('reads a decision that is not a boolean as no answer', () => {
    expect(summary(TOOL_APPROVAL, { approvalId: 'approval_1', approved: 'true' })).toBeNull()
    expect(summary(TOOL_APPROVAL, { approvalId: 'approval_1', approved: 1 })).toBeNull()
  })

  it('words a question answered with no text', () => {
    expect(summary(QUESTION, {
      requestId: 'capreq_1',
      ok: true,
      payload: { result: '  ' },
    })).toEqual({ kind: 'label', text: 'No answer' })
    expect(summary(QUESTION, { requestId: 'capreq_1', ok: true })).toEqual({ kind: 'label', text: 'No answer' })
    expect(summary(QUESTION, {
      requestId: 'capreq_1',
      ok: true,
      payload: { result: 3 },
    })).toEqual({ kind: 'label', text: 'No answer' })
  })

  it('words a declined question whose error is blank as a plain decline', () => {
    expect(summary(QUESTION, { requestId: 'capreq_1', ok: false, error: '  ' })).toEqual({ kind: 'label', text: 'Declined' })
    expect(summary(QUESTION, { requestId: 'capreq_1', ok: false })).toEqual({ kind: 'label', text: 'Declined' })
  })

  it('reads a question reply whose outcome is not a boolean as no answer', () => {
    expect(summary(QUESTION, { requestId: 'capreq_1', ok: 'yes' })).toBeNull()
  })

  it('reads nothing for a prompt that is neither an approval nor a question', () => {
    const dialog: ControlPrompt = {
      kind: 'dialog',
      dialog: { title: 'Continue?', variant: 'confirm' },
    }
    expect(summary(dialog, { approvalId: 'approval_1', approved: true })).toBeNull()
  })

  it('reads a reply that is not a record as no answer', () => {
    for (const response of [
      'Allow',
      3,
      null,
      ['ok'],
    ])
      expect(summary(TOOL_APPROVAL, response), JSON.stringify(response)).toBeNull()
  })
})
