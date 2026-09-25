import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { grokControlResponseSummary, grokPermissionRejectReason } from './controlResponse'

function record(request: Record<string, unknown> | undefined, result: Record<string, unknown> | undefined): PersistedControlResponse {
  return { requestId: 'jsonrpc:1', claimToken: 'claim', request, response: result === undefined ? undefined : { jsonrpc: '2.0', id: 1, result } }
}

const PLAN = { jsonrpc: '2.0', id: 1, method: '_x.ai/exit_plan_mode', params: { toolCallId: 'c', planContent: '1. X' } }
const QUESTION = { jsonrpc: '2.0', id: 1, method: '_x.ai/ask_user_question', params: { questions: [{ question: 'Pick', options: [{ label: 'A' }, { label: 'B' }] }] } }
const ELICIT = { jsonrpc: '2.0', id: 1, method: '_x.ai/mcp/elicit', params: { serverName: 'docs', mode: 'form', message: 'Choose', requestedSchema: { type: 'object', properties: { size: { type: 'string', title: 'Size' } } } } }
const TRUST = { jsonrpc: '2.0', id: 1, method: '_x.ai/folder_trust/request', params: { cwd: '/w', workspace: '/w', configKinds: ['mcp'] } }
const PERMISSION = { jsonrpc: '2.0', id: 1, method: 'session/request_permission', params: {
  toolCall: { toolCallId: 'c' },
  options: [{ optionId: 'allow-once', name: 'Yes, proceed', kind: 'allow_once' }, { optionId: 'reject-once', name: 'No, and tell Grok what to do differently', kind: 'reject_once' }],
} }

describe('grokPermissionRejectReason', () => {
  it('adds the reason as the follow-up message and keeps the rest', () => {
    const result = { outcome: { outcome: 'selected', optionId: 'reject-once' }, _meta: { other: 1 } }
    expect(grokPermissionRejectReason(result, 'Use pnpm')).toEqual({
      outcome: { outcome: 'selected', optionId: 'reject-once' },
      _meta: { other: 1, followup_message: 'Use pnpm' },
    })
  })

  it('adds a meta object to a result that has none', () => {
    expect(grokPermissionRejectReason({ outcome: { outcome: 'cancelled' } }, 'why')).toEqual({ outcome: { outcome: 'cancelled' }, _meta: { followup_message: 'why' } })
  })

  it('replaces a meta value that is no object, and a follow-up message that was already there', () => {
    expect(grokPermissionRejectReason({ _meta: 'x' }, 'why')).toEqual({ _meta: { followup_message: 'why' } })
    expect(grokPermissionRejectReason({ _meta: { followup_message: 'old' } }, 'new')).toEqual({ _meta: { followup_message: 'new' } })
  })

  it('leaves the result it received unchanged', () => {
    const result = { outcome: { outcome: 'selected', optionId: 'reject-once' }, _meta: { other: 1 } }
    grokPermissionRejectReason(result, 'why')
    expect(result).toEqual({ outcome: { outcome: 'selected', optionId: 'reject-once' }, _meta: { other: 1 } })
  })
})

describe('grokControlResponseSummary', () => {
  it('shows a plan decision in the words of its buttons', () => {
    expect(grokControlResponseSummary(record(PLAN, { outcome: 'approved' }))).toEqual({ kind: 'label', text: 'Approve' })
    expect(grokControlResponseSummary(record(PLAN, { outcome: 'cancelled' }))).toEqual({ kind: 'label', text: 'Reject' })
    expect(grokControlResponseSummary(record(PLAN, { outcome: 'cancelled', feedback: 'Split step 2.' }))).toEqual({ kind: 'feedback', message: 'Split step 2.' })
    expect(grokControlResponseSummary(record(PLAN, { outcome: 'something-new' }))).toBeNull()
  })

  it('shows the button word for a rejected plan whose feedback is only whitespace', () => {
    expect(grokControlResponseSummary(record(PLAN, { outcome: 'cancelled', feedback: '   ' }))).toEqual({ kind: 'label', text: 'Reject' })
  })

  it('shows the answers of a question', () => {
    expect(grokControlResponseSummary(record(QUESTION, { outcome: 'accepted', answers: { Pick: ['B'] } }))).toEqual({ kind: 'label', text: 'Pick: B' })
    expect(grokControlResponseSummary(record(QUESTION, { outcome: 'cancelled' }))).toEqual({ kind: 'label', text: 'Cancel' })
    expect(grokControlResponseSummary(record(QUESTION, { outcome: 'accepted', answers: {} }))).toBeNull()
  })

  it('shows an MCP form answer through the shared form display', () => {
    expect(grokControlResponseSummary(record(ELICIT, { outcome: 'accept', content: { size: 'Large' } }))).toEqual({ kind: 'label', text: 'Approved\nSize: Large' })
    expect(grokControlResponseSummary(record(ELICIT, { outcome: 'decline' }))).toEqual({ kind: 'label', text: 'Rejected' })
    expect(grokControlResponseSummary(record(ELICIT, { outcome: 'cancel' }))).toEqual({ kind: 'label', text: 'Cancelled' })
    expect(grokControlResponseSummary(record(ELICIT, { outcome: 'other' }))).toBeNull()
  })

  it('shows the folder-trust answer in the words of its button', () => {
    expect(grokControlResponseSummary(record(TRUST, { outcome: 'trust' }))).toEqual({ kind: 'label', text: 'Trust this workspace' })
    expect(grokControlResponseSummary(record(TRUST, { outcome: 'reject' }))).toEqual({ kind: 'label', text: 'Do not trust' })
    expect(grokControlResponseSummary(record(TRUST, { outcome: 'x' }))).toBeNull()
  })

  it('shows the reason a rejected permission carried', () => {
    expect(grokControlResponseSummary(record(PERMISSION, { outcome: { outcome: 'selected', optionId: 'reject-once' }, _meta: { followup_message: ' Use pnpm ' } })))
      .toEqual({ kind: 'feedback', message: 'Use pnpm' })
  })

  it('shows the chosen option of a permission', () => {
    expect(grokControlResponseSummary(record(PERMISSION, { outcome: { outcome: 'selected', optionId: 'allow-once' } }))).toEqual({ kind: 'label', text: 'Yes, proceed' })
  })

  // A reason of only whitespace is no reason, so the row states the button instead.
  it('shows the chosen option when the follow-up message is only whitespace', () => {
    expect(grokControlResponseSummary(record(PERMISSION, { outcome: { outcome: 'selected', optionId: 'reject-once' }, _meta: { followup_message: '  ' } })))
      .toEqual({ kind: 'label', text: 'No, and tell Grok what to do differently' })
  })

  it('falls back to the shared display when the reply is no result', () => {
    expect(grokControlResponseSummary(record(PLAN, undefined))).toBeNull()
  })
})
