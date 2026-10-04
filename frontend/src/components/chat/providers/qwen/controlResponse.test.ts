import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { qwenControlResponseSummary } from './controlResponse'

function record(request: Record<string, unknown> | undefined, result: Record<string, unknown>): PersistedControlResponse {
  return { requestId: 'jsonrpc:5', claimToken: 'claim', request, response: { jsonrpc: '2.0', id: 5, result } }
}

function selected(optionId: string, extra: Record<string, unknown> = {}) {
  return { outcome: { outcome: 'selected', optionId }, ...extra }
}

const PLAN = { method: 'session/request_permission', params: {
  options: [
    { optionId: 'restore_previous', name: 'Yes, restore previous mode (default)', kind: 'allow_once' },
    { optionId: 'proceed_always', name: 'Yes, and auto-accept edits', kind: 'allow_always' },
    { optionId: 'proceed_once', name: 'Yes, and manually approve edits', kind: 'allow_once' },
    { optionId: 'cancel', name: 'No, keep planning (esc)', kind: 'reject_once' },
  ],
  toolCall: { toolCallId: 'p', _meta: { toolName: 'exit_plan_mode' } },
} }

const QUESTION = { method: 'session/request_permission', params: {
  options: [{ optionId: 'proceed_once', name: 'Submit', kind: 'allow_once' }, { optionId: 'cancel', name: 'Cancel', kind: 'reject_once' }],
  toolCall: { toolCallId: 'q', _meta: { toolName: 'ask_user_question', qwenInteractionKind: 'user_question', qwenQuestions: [{ question: 'Color?', header: 'Color', options: [{ label: 'Blue' }] }] } },
} }

const SHELL = { method: 'session/request_permission', params: {
  options: [{ optionId: 'proceed_once', name: 'Allow', kind: 'allow_once' }, { optionId: 'cancel', name: 'Reject', kind: 'reject_once' }],
  toolCall: { toolCallId: 's', _meta: { toolName: 'run_shell_command' } },
} }

describe('qwenControlResponseSummary', () => {
  it('shows a plan decision in the words of the plan buttons', () => {
    expect(qwenControlResponseSummary(record(PLAN, selected('proceed_once')))).toEqual({ kind: 'label', text: 'Approve' })
    expect(qwenControlResponseSummary(record(PLAN, selected('proceed_always')))).toEqual({ kind: 'label', text: 'Approve' })
    expect(qwenControlResponseSummary(record(PLAN, selected('restore_previous')))).toEqual({ kind: 'label', text: 'Approve' })
    expect(qwenControlResponseSummary(record(PLAN, selected('cancel')))).toEqual({ kind: 'label', text: 'Reject' })
  })

  it('reads an option the request does not list from its well-known kind', () => {
    const planWithoutOptions = { ...PLAN, params: { ...PLAN.params, options: [] } }
    expect(qwenControlResponseSummary(record(planWithoutOptions, selected('proceed_once')))).toEqual({ kind: 'label', text: 'Approve' })
    expect(qwenControlResponseSummary(record(planWithoutOptions, selected('mystery')))).toEqual({ kind: 'label', text: 'mystery' })
  })

  it('shows the answers of a question, and the option that dismissed one', () => {
    expect(qwenControlResponseSummary(record(QUESTION, selected('proceed_once', { answers: { 0: 'Blue' } })))).toEqual({ kind: 'label', text: 'Color: Blue' })
    expect(qwenControlResponseSummary(record(QUESTION, selected('cancel')))).toEqual({ kind: 'label', text: 'Cancel' })
  })

  it('shows the chosen option of an ordinary permission', () => {
    expect(qwenControlResponseSummary(record(SHELL, selected('proceed_once')))).toEqual({ kind: 'label', text: 'Allow' })
  })

  // Only Qwen's question reply carries the answers field, so the reply alone states
  // that it answered a question. Without the request, the row read the submit
  // option's kind, "Allow once", for a question that the reader answered.
  it('shows the answers of a question whose request is gone', () => {
    expect(qwenControlResponseSummary(record(undefined, selected('proceed_once', { answers: { 0: 'Blue', 1: 'Cache, Metrics' } }))))
      .toEqual({ kind: 'label', text: 'Question 1: Blue\nQuestion 2: Cache, Metrics' })
  })

  it('shows the option of a reply with no answers whose request is gone', () => {
    expect(qwenControlResponseSummary(record(undefined, selected('cancel')))).toEqual({ kind: 'label', text: 'Reject' })
    expect(qwenControlResponseSummary(record(undefined, selected('proceed_once', { answers: {} })))).toEqual({ kind: 'label', text: 'Allow once' })
  })

  // A stop withdraws a plan approval with the protocol's cancel, which selects no
  // option, so the plan words cannot apply.
  it('shows a withdrawn plan approval as cancelled', () => {
    expect(qwenControlResponseSummary(record(PLAN, { outcome: { outcome: 'cancelled' } }))).toEqual({ kind: 'label', text: 'Cancelled' })
  })

  it('shows a plan option that states no kind by its own name', () => {
    const planWithCustomOption = { ...PLAN, params: { ...PLAN.params, options: [{ optionId: 'later', name: 'Decide later' }] } }
    expect(qwenControlResponseSummary(record(planWithCustomOption, selected('later')))).toEqual({ kind: 'label', text: 'Decide later' })
  })

  it('answers null for a response that holds no result', () => {
    expect(qwenControlResponseSummary({ requestId: 'jsonrpc:5', claimToken: 'claim', request: PLAN, response: undefined })).toBeNull()
    expect(qwenControlResponseSummary({ requestId: 'jsonrpc:5', claimToken: 'claim', request: QUESTION, response: { jsonrpc: '2.0', id: 5 } })).toBeNull()
  })
})
